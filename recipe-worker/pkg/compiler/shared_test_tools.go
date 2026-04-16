package compiler

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/colony-2/c2j/recipe-core/pkg/contextual"
)

var (
	testRepoOnce sync.Once
	testRepoPath string
	testRepoHash string
)

func ensureTestRepo() (string, string) {
	testRepoOnce.Do(func() {
		dir, err := os.MkdirTemp("", "recipe-worker-test-repo-*")
		if err != nil {
			panic(err)
		}
		if err := runGit(dir, "git", "init"); err != nil {
			panic(err)
		}
		if err := runGit(dir, "git", "config", "user.email", "test@example.com"); err != nil {
			panic(err)
		}
		if err := runGit(dir, "git", "config", "user.name", "Test User"); err != nil {
			panic(err)
		}
		readme := filepath.Join(dir, "README.md")
		if err := os.WriteFile(readme, []byte("initial\n"), 0o644); err != nil {
			panic(err)
		}
		cells := []string{"cells/test-cell", "cells/alpha", "cells/beta", "cells/cell-a"}
		for _, rel := range cells {
			full := filepath.Join(dir, rel)
			if err := os.MkdirAll(full, 0o755); err != nil {
				panic(err)
			}
			seed := filepath.Join(full, "README.md")
			if err := os.WriteFile(seed, []byte(rel+"\n"), 0o644); err != nil {
				panic(err)
			}
		}
		if err := runGit(dir, "git", "add", "."); err != nil {
			panic(err)
		}
		if err := runGit(dir, "git", "commit", "-m", "init"); err != nil {
			panic(err)
		}
		output, err := exec.Command("git", "-C", dir, "rev-parse", "HEAD").CombinedOutput()
		if err != nil {
			panic(fmt.Errorf("rev-parse HEAD failed: %w (%s)", err, output))
		}
		testRepoPath = dir
		testRepoHash = strings.TrimSpace(string(output))
	})
	return testRepoPath, testRepoHash
}

func GenerateTestContext() (contextual.JobContext, contextual.GitCommitContext) {
	baseRepo, baseHash := ensureTestRepo()

	job := contextual.JobContext{
		Actor: contextual.ActorContext{
			TicketID:   "TEST-TICKET",
			ActorName:  "test-actor",
			ActorEmail: "test-actor@colony2",
		},
		Environment: contextual.EnvironmentContext{},
		Workflow: contextual.WorkflowContext{
			CellName: "cells/test-cell",
			CellPath: "cells/test-cell",
		},
		GitBase: contextual.GitBaseContext{
			BaseRepo:         baseRepo,
			BaseRef:          baseHash,
			ResolvedBaseHash: baseHash,
			GitAuthor:        "",
		},
	}

	g := contextual.GitCommitContext{
		PersistHash: "",
		ParentRef:   baseHash,
		ParentHash:  "",
	}

	return job, g
}

func runGit(dir string, name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git %v failed: %w (%s)", args, err, out)
	}
	return nil
}
