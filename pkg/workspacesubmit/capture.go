package workspacesubmit

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/colony-2/c2j/pkg/git/common"
	"github.com/colony-2/c2j/pkg/gitprofile"
)

// Snapshot writes objects through a private index, never changing HEAD or refs.
type Snapshot struct {
	Hash       string
	Dirty      bool
	root, temp string
}

func (s *Snapshot) Close() { _ = os.RemoveAll(s.temp) }

func git(ctx context.Context, dir string, env []string, args ...string) (string, error) {
	cmd := gitprofile.Command(ctx, args...)
	cmd.Dir = dir
	cmd.Env = append(cmd.Env, "GIT_OPTIONAL_LOCKS=0")
	cmd.Env = append(cmd.Env, env...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := gitprofile.Output(ctx, cmd)
	if err != nil {
		return "", fmt.Errorf("workspace git %s: %w: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(string(out)), nil
}

func Capture(ctx context.Context, dir string) (_ *Snapshot, err error) {
	root, err := common.FindGitRootContext(ctx, dir)
	if err != nil {
		return nil, err
	}
	conflicts, err := git(ctx, root, nil, "ls-files", "--unmerged")
	if err != nil {
		return nil, err
	}
	if conflicts != "" {
		return nil, fmt.Errorf("cannot snapshot workspace with unresolved merge conflicts")
	}
	temp, err := os.MkdirTemp("", "c2j-submit-workspace-")
	if err != nil {
		return nil, err
	}
	s := &Snapshot{root: root, temp: temp}
	defer func() {
		if err != nil {
			s.Close()
		}
	}()
	head, headErr := git(ctx, root, nil, "rev-parse", "--verify", "HEAD")
	if headErr != nil {
		// Only an unborn branch is allowed; other HEAD failures must not be hidden.
		ref, e := git(ctx, root, nil, "symbolic-ref", "-q", "HEAD")
		if e != nil || ref == "" {
			return nil, headErr
		}
		head = ""
	}
	index, err := git(ctx, root, nil, "rev-parse", "--path-format=absolute", "--git-path", "index")
	if err != nil {
		return nil, err
	}
	env := []string{"GIT_INDEX_FILE=" + filepath.Join(temp, "index")}
	if data, e := os.ReadFile(index); e == nil {
		if err = os.WriteFile(filepath.Join(temp, "index"), data, 0600); err != nil {
			return nil, err
		}
	} else if !os.IsNotExist(e) {
		return nil, e
	}
	if _, err = git(ctx, root, env, "add", "-A", "--", "."); err != nil {
		return nil, err
	}
	tree, err := git(ctx, root, env, "write-tree")
	if err != nil {
		return nil, err
	}
	s.Hash = head
	oldTree, err := git(ctx, root, nil, "hash-object", "-t", "tree", "--stdin")
	if err != nil {
		return nil, err
	}
	if head != "" {
		oldTree, err = git(ctx, root, nil, "rev-parse", head+"^{tree}")
		if err != nil {
			return nil, err
		}
	}
	s.Dirty = tree != oldTree
	if s.Dirty {
		env = append(env, "GIT_AUTHOR_NAME=c2j", "GIT_AUTHOR_EMAIL=c2j@localhost", "GIT_COMMITTER_NAME=c2j", "GIT_COMMITTER_EMAIL=c2j@localhost")
		args := []string{"commit-tree", tree, "-m", "Workspace snapshot for c2j submit"}
		if head != "" {
			args = append(args, "-p", head)
		}
		s.Hash, err = git(ctx, root, env, args...)
		if err != nil {
			return nil, err
		}
	}
	return s, nil
}

func (s *Snapshot) Bundle(ctx context.Context) ([]byte, error) {
	// A private repository supplies a named ref for bundle creation. All source
	// refs and the user's index remain untouched; only unreachable objects are added.
	objectDir, err := git(ctx, s.root, nil, "rev-parse", "--path-format=absolute", "--git-path", "objects")
	if err != nil {
		return nil, err
	}
	format, err := git(ctx, s.root, nil, "rev-parse", "--show-object-format")
	if err != nil {
		return nil, err
	}
	bare := filepath.Join(s.temp, "bundle.git")
	if _, err = git(ctx, s.temp, nil, "init", "--bare", "--object-format="+format, bare); err != nil {
		return nil, err
	}
	if err = os.WriteFile(filepath.Join(bare, "objects", "info", "alternates"), []byte(objectDir+"\n"), 0600); err != nil {
		return nil, err
	}
	// Shallow boundaries must be carried into the temporary repository.
	shallow, err := git(ctx, s.root, nil, "rev-parse", "--path-format=absolute", "--git-path", "shallow")
	if err != nil {
		return nil, err
	}
	if data, e := os.ReadFile(shallow); e == nil {
		if err = os.WriteFile(filepath.Join(bare, "shallow"), data, 0600); err != nil {
			return nil, err
		}
	} else if !os.IsNotExist(e) {
		return nil, e
	}
	if _, err = git(ctx, bare, nil, "update-ref", "refs/heads/snapshot", s.Hash); err != nil {
		return nil, err
	}
	pack := filepath.Join(s.temp, "snapshot.pack")
	if _, err = git(ctx, bare, nil, "bundle", "create", pack, "refs/heads/snapshot"); err != nil {
		return nil, err
	}
	return os.ReadFile(pack)
}
