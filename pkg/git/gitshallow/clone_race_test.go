//go:build linux

package gitshallow_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/colony-2/c2j/pkg/git/gitshallow"
	"github.com/colony-2/c2j/pkg/git/gitstate"
)

// TestCloneDuringRepack synchronizes real Git at loose-object access, then
// replaces the loose store with a valid pack. No Git failure is fabricated.
// The filesystem control proves the interleaving is harmful; the transport
// control distinguishes it from deletion of the repository itself.
func TestCloneDuringRepack(t *testing.T) {
	cc, err := exec.LookPath("cc")
	if err != nil {
		t.Skip("clone interposition requires a C compiler")
	}
	library := filepath.Join(t.TempDir(), "clone-barrier.so")
	if out, err := exec.Command(cc, "-shared", "-fPIC", "-Wall", "-Werror", "-o", library, "testdata/clone_barrier.c", "-ldl").CombinedOutput(); err != nil {
		t.Fatalf("build object-access barrier: %v\n%s", err, out)
	}
	for _, tc := range []struct {
		name                            string
		helper, transport, deleteSource bool
		cancel, managed                 bool
	}{
		{name: "filesystem-control"},
		{name: "transport-control", transport: true},
		{name: "helper", helper: true},
		{name: "deleted-filesystem-source", deleteSource: true},
		{name: "deleted-transport-source", transport: true, deleteSource: true},
		{name: "cancelled-helper", helper: true, cancel: true},
		{name: "managed-repack", managed: true, transport: true},
		{name: "deleted-managed-source", managed: true, transport: true, deleteSource: true},
		{name: "cancelled-managed", managed: true, transport: true, cancel: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			source, target := filepath.Join(root, "source"), filepath.Join(root, "target")
			if err := os.Mkdir(source, 0755); err != nil {
				t.Fatal(err)
			}
			git := func(dir string, args ...string) string {
				t.Helper()
				ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
				defer cancel()
				cmd := exec.CommandContext(ctx, "git", args...)
				cmd.Dir = dir
				out, err := cmd.CombinedOutput()
				if err != nil {
					t.Fatalf("git %v: %v\n%s", args, err, out)
				}
				return strings.TrimSpace(string(out))
			}
			git(source, "init", "-b", "main")
			git(source, "config", "user.name", "Clone Test")
			git(source, "config", "user.email", "clone@example.test")
			git(source, "config", "gc.auto", "0")
			content := "an object that must survive source repacking\n"
			if err := os.WriteFile(filepath.Join(source, "required.txt"), []byte(content), 0644); err != nil {
				t.Fatal(err)
			}
			git(source, "add", ".")
			git(source, "commit", "-m", "required object")
			commit := git(source, "rev-parse", "HEAD")
			blob := git(source, "rev-parse", "HEAD:required.txt")
			object := filepath.Join(source, ".git", "objects", blob[:2], blob[2:])
			git(source, "fsck", "--full")
			ready, release := filepath.Join(root, "ready"), filepath.Join(root, "release")
			t.Setenv("LD_PRELOAD", library)
			t.Setenv("C2J_CLONE_OBJECT", object)
			t.Setenv("C2J_CLONE_READY", ready)
			t.Setenv("C2J_CLONE_RELEASE", release)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			finished := make(chan error, 1)
			// Always release and join Git before t.TempDir removes either root.
			defer func() {
				_ = os.WriteFile(release, nil, 0600)
				cancel()
				<-finished
			}()
			var cloneErr error
			done := make(chan error, 1)
			go func() {
				if tc.managed {
					done <- gitstate.NewController(nil).Restore(ctx, &gitstate.GitTaskContext{
						GlobalGitTaskContext: &gitstate.GlobalGitTaskContext{BaseRepo: source, BaseRef: commit, ResolvedBaseHash: commit, ParentHash: commit, PersistHash: commit},
						WorktreePath:         target,
					}, nil)
				} else if tc.helper {
					_, err := gitshallow.GitShallowClone(ctx, gitshallow.GitShallowCloneInput{SourceDir: source, TargetDir: target, CommitHash: commit})
					done <- err
				} else {
					args := []string{"clone", "--depth", "1", "--no-single-branch"}
					if tc.transport {
						args = append(args, "--no-local")
					}
					cmd := exec.CommandContext(ctx, "git", append(args, source, target)...)
					out, err := cmd.CombinedOutput()
					if err != nil {
						err = &cloneFailure{err: err, output: string(out)}
					}
					done <- err
				}
				finished <- nil
			}()
			deadline := time.NewTimer(10 * time.Second)
			defer deadline.Stop()
			ticker := time.NewTicker(5 * time.Millisecond)
			defer ticker.Stop()
		wait:
			for {
				if event, err := os.ReadFile(ready); err == nil && len(event) > 0 {
					t.Logf("commit=%s destination=%s object access: %s", commit, target, event)
					break wait
				}
				select {
				case err := <-done:
					t.Fatalf("clone finished before object barrier: %v", err)
				case <-deadline.C:
					t.Fatal("Git did not reach object-access barrier")
				case <-ticker.C:
				}
			}
			if tc.cancel {
				cancel()
				t.Log("cancellation sent while Git is paused; source retained until clone joins")
			} else if tc.deleteSource {
				if err := os.RemoveAll(source); err != nil {
					t.Fatal(err)
				}
				t.Log("source deleted while clone is paused (negative lifetime control)")
			} else {
				git(source, "repack", "-ad")
				git(source, "prune-packed")
				if _, err := os.Stat(object); !os.IsNotExist(err) {
					t.Fatalf("loose object was not removed: %v", err)
				}
				if got := git(source, "cat-file", "-p", blob); got != strings.TrimSpace(content) {
					t.Fatalf("source object changed: %q", got)
				}
				git(source, "fsck", "--full")
				t.Log("repack/prune finished; source remains valid and required blob is packed")
			}
			if err := os.WriteFile(release, nil, 0600); err != nil {
				t.Fatal(err)
			}
			cloneErr = <-done
			if tc.cancel || tc.deleteSource || (!tc.helper && !tc.transport) {
				if cloneErr == nil {
					t.Fatal("expected real clone failure")
				}
				if strings.Contains(cloneErr.Error(), "exit status 99") {
					t.Fatalf("barrier timed out: %v", cloneErr)
				}
				if !tc.cancel && !tc.transport && !strings.Contains(cloneErr.Error(), "failed to copy file") {
					t.Fatalf("filesystem control did not reproduce the object copy failure: %v", cloneErr)
				}
				if tc.cancel {
					git(source, "fsck", "--full")
				}
				if tc.managed {
					if tc.cancel && !errors.Is(cloneErr, context.Canceled) {
						t.Fatalf("managed clone lost cancellation cause: %v", cloneErr)
					}
					if _, err := os.Stat(target); !os.IsNotExist(err) {
						t.Fatalf("failed managed clone did not clean destination: %v", err)
					}
				}
				t.Logf("expected clone failure: %v", cloneErr)
				return
			}
			if cloneErr != nil {
				t.Fatalf("valid source repack broke clone: %v", cloneErr)
			}
			if err := os.RemoveAll(source); err != nil {
				t.Fatal(err)
			}
			git(target, "fsck", "--full")
			if got := git(target, "rev-parse", "HEAD"); got != commit {
				t.Fatalf("HEAD=%s, want %s", got, commit)
			}
			if got := git(target, "cat-file", "-p", blob); got != strings.TrimSpace(content) {
				t.Fatalf("cloned bytes=%q", got)
			}
			if got := git(target, "status", "--porcelain"); got != "" {
				t.Fatalf("dirty clone: %s", got)
			}
			t.Log("clone joined; source cleanup completed; clone independently retains requested objects")
		})
	}
}

type cloneFailure struct {
	err    error
	output string
}

func (e *cloneFailure) Error() string { return e.err.Error() + ": " + e.output }
