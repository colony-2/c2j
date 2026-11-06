package jobinspect

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/colony-2/c2j/internal/repository"
	"github.com/colony-2/c2j/pkg/gitprofile"
)

// Export writes an isolated checkout and all selected artifacts. The destination
// must not exist; failures remove only the directory created by this call.
// snapshot.json is written last and is the completion marker.
func Export(ctx context.Context, api API, q Request, directory string) (snapshot *Snapshot, err error) {
	q.Operation = "snapshot"
	response, err := api.Inspect(ctx, q)
	if err != nil {
		return nil, err
	}
	snapshot = response.Snapshot
	if snapshot == nil {
		return nil, fmt.Errorf("snapshot response missing")
	}
	for _, w := range snapshot.Workspaces {
		if w.Availability != "available" {
			return nil, fmt.Errorf("workspace %s unavailable: %s", w.Cell, w.Reason)
		}
	}
	directory, err = filepath.Abs(directory)
	if err != nil {
		return nil, err
	}
	if err = os.Mkdir(directory, 0700); err != nil {
		return nil, fmt.Errorf("create snapshot directory (must not exist): %w", err)
	}
	defer func() {
		if err != nil {
			_ = os.RemoveAll(directory)
		}
	}()
	for i := range snapshot.Artifacts {
		a := &snapshot.Artifacts[i]
		if a.Availability != "stored" && a.Availability != "available" {
			return nil, fmt.Errorf("artifact %s is %s; download selected artifacts individually", a.Name, a.Availability)
		}
		target, e := exportPath(directory, a.Path)
		if e != nil {
			return nil, e
		}
		request := Request{JobID: q.JobID, Task: a.Task, Attempt: a.Attempt, Name: a.Name}
		if a.Reference.Stored != nil {
			request.Reference = &a.Reference.Stored.Key
		}
		if err = downloadFile(ctx, api, request, target, a.Size); err != nil {
			return nil, err
		}
	}
	for i := range snapshot.Workspaces {
		w := &snapshot.Workspaces[i]
		checkout, e := exportPath(directory, w.Directory)
		if e != nil {
			return nil, e
		}
		bundle := ""
		if w.Bundle != nil {
			w.Bundle.Path = "bundles/" + shortID(workspaceKey(*w)) + ".bundle"
			bundle = filepath.Join(directory, w.Bundle.Path)
			if w.Bundle.Reference.Stored == nil {
				return nil, fmt.Errorf("snapshot bundle has no stored reference")
			}
			if err = downloadFile(ctx, api, Request{JobID: q.JobID, Reference: &w.Bundle.Reference.Stored.Key}, bundle, w.Bundle.Size); err != nil {
				return nil, err
			}
		}
		if err = materialize(ctx, *w, checkout, bundle); err != nil {
			return nil, fmt.Errorf("restore workspace %s: %w", w.Cell, err)
		}
	}
	raw, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return nil, err
	}
	err = os.WriteFile(filepath.Join(directory, "snapshot.json"), append(raw, '\n'), 0600)
	return snapshot, err
}

func exportPath(root, name string) (string, error) {
	if name == "" || strings.Contains(name, "\\") || !filepath.IsLocal(name) || filepath.Clean(name) != name {
		return "", fmt.Errorf("unsafe snapshot path %q", name)
	}
	return filepath.Join(root, name), nil
}
func downloadFile(ctx context.Context, api API, q Request, target string, size int64) (err error) {
	if err = os.MkdirAll(filepath.Dir(target), 0700); err != nil {
		return err
	}
	f, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer func() {
		errClose := f.Close()
		if err == nil {
			err = errClose
		}
		if err != nil {
			_ = os.Remove(target)
		}
	}()
	r, err := api.Download(ctx, q)
	if err != nil {
		return err
	}
	defer r.Close()
	n, err := io.Copy(f, r)
	if err != nil {
		return err
	}
	if size >= 0 && n != size {
		return fmt.Errorf("artifact size mismatch: expected %d bytes, received %d", size, n)
	}
	return nil
}

func materialize(ctx context.Context, w Workspace, dir, bundle string) error {
	if w.HeadCommit != "" && !fullHash(w.HeadCommit) {
		return fmt.Errorf("invalid target commit")
	}
	if w.BaseCommit != "" && !fullHash(w.BaseCommit) {
		return fmt.Errorf("invalid base commit")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	git := func(args ...string) ([]byte, error) {
		cmd := gitprofile.Command(ctx, append([]string{"-c", "core.hooksPath=/dev/null", "-c", "protocol.ext.allow=never"}, args...)...)
		cmd.Dir = dir
		env := cmd.Env[:0]
		for _, value := range cmd.Env {
			name, _, _ := strings.Cut(value, "=")
			switch name {
			case "GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_COMMON_DIR", "GIT_OBJECT_DIRECTORY", "GIT_ALTERNATE_OBJECT_DIRECTORIES":
				continue
			}
			env = append(env, value)
		}
		cmd.Env = env
		out, err := gitprofile.CombinedOutput(ctx, cmd)
		if err != nil {
			return nil, fmt.Errorf("git %s: %w: %s", args[0], err, out)
		}
		return out, nil
	}
	initArgs := []string{"init", "--quiet"}
	if len(w.HeadCommit) == 64 || len(w.BaseCommit) == 64 {
		initArgs = append(initArgs, "--object-format=sha256")
	}
	if _, err := git(initArgs...); err != nil {
		return err
	}
	repo := w.Repository
	if strings.HasPrefix(repo, "-") || strings.Contains(repo, "::") {
		return fmt.Errorf("unsupported repository location")
	}
	var err error
	repo, err = repository.Normalize(repo)
	if err != nil {
		return err
	}
	if _, err := git("remote", "add", "origin", repo); err != nil {
		return err
	}
	prereqs := append([]string(nil), w.Prerequisites...)
	if bundle == "" && w.BaseCommit != "" && !contains(prereqs, w.BaseCommit) {
		prereqs = append(prereqs, w.BaseCommit)
	}
	// Fetch exact prerequisites and their histories. A shallow base can be
	// insufficient for a bundle's excluded ancestry and delta objects.
	for _, hash := range prereqs {
		if !fullHash(hash) {
			return fmt.Errorf("invalid prerequisite commit")
		}
		if _, err := git("fetch", "--no-tags", "origin", hash); err != nil {
			return fmt.Errorf("fetch prerequisite %s: %w", hash, err)
		}
	}
	if bundle != "" {
		if _, err := git("bundle", "verify", bundle); err != nil {
			return err
		}
		if _, err := git("bundle", "unbundle", bundle); err != nil {
			return err
		}
	}
	// A self-contained bundle already carries its base. Avoid requiring a
	// reachable origin solely to create the convenient base ref.
	if w.BaseCommit != "" {
		if _, err := git("cat-file", "-e", w.BaseCommit+"^{commit}"); err != nil {
			if _, err = git("fetch", "--no-tags", "origin", w.BaseCommit); err != nil {
				return err
			}
		}
	}
	if w.HeadCommit == "" {
		if w.EmptyBase {
			return nil
		}
		return fmt.Errorf("snapshot has no target commit")
	}
	if _, err := git("cat-file", "-e", w.HeadCommit+"^{commit}"); err != nil {
		return fmt.Errorf("target commit unavailable: %w", err)
	}
	if w.BaseCommit != "" {
		if _, err := git("update-ref", "refs/c2j/base", w.BaseCommit); err != nil {
			return err
		}
	}
	if _, err := git("update-ref", "refs/c2j/snapshot", w.HeadCommit); err != nil {
		return err
	}
	if _, err := git("checkout", "--detach", w.HeadCommit); err != nil {
		return err
	}
	head, err := git("rev-parse", "HEAD")
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(head)) != w.HeadCommit {
		return fmt.Errorf("restored HEAD does not match snapshot")
	}
	return nil
}
