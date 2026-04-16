package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/colony-2/c2j/recipe-core/pkg/contextual"
	coreops "github.com/colony-2/c2j/recipe-core/pkg/ops"
	"github.com/colony-2/c2j/recipe-core/pkg/recipe"
	"github.com/colony-2/c2j/recipe-worker/pkg/executor"
	"github.com/colony-2/c2j/recipe-worker/pkg/ops"
	"go.uber.org/zap/zaptest"
	"gopkg.in/yaml.v3"
)

var (
	debugRepoOnce sync.Once
	debugRepoPath string
	debugRepoHash string
)

func ensureDebugRepo() (string, string) {
	debugRepoOnce.Do(func() {
		dir, err := os.MkdirTemp("", "debug-repo-*")
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
		if err := runGit(dir, "git", "add", "."); err != nil {
			panic(err)
		}
		if err := runGit(dir, "git", "commit", "-m", "init"); err != nil {
			panic(err)
		}
		// Add a second commit to ensure persist flows have a valid parent/root hash.
		if err := os.WriteFile(filepath.Join(dir, "SECOND.txt"), []byte("second\n"), 0o644); err != nil {
			panic(err)
		}
		if err := runGit(dir, "git", "add", "."); err != nil {
			panic(err)
		}
		if err := runGit(dir, "git", "commit", "-m", "second"); err != nil {
			panic(err)
		}
		output, err := exec.Command("git", "-C", dir, "rev-parse", "HEAD").CombinedOutput()
		if err != nil {
			panic(fmt.Errorf("rev-parse HEAD failed: %w (%s)", err, output))
		}
		debugRepoPath = dir
		debugRepoHash = strings.TrimSpace(string(output))
	})
	return debugRepoPath, debugRepoHash
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

func TestDebugExecutor(t *testing.T) {
	yamlStr := `id: hello_world
desc: A minimal recipe that demonstrates basic structure  
version: "1.0"
op: echo_activity
inputs:
  message: "Hello, World!"`

	var r recipe.Recipe
	err := yaml.Unmarshal([]byte(yamlStr), &r)
	if err != nil {
		t.Fatalf("Parse Error: %v", err)
	}

	t.Logf("Recipe ID: %s", r.GetMetdata().ID)

	// Create executor
	logger := zaptest.NewLogger(t)
	a, err := ops.NewActivityRegistry()
	if err != nil {
		t.Fatalf("Registry Error: %v", err)
	}

	standaloneExecutor, err := executor.NewStandaloneExecutor(coreops.NewServiceDepsBuilder().Build(), a, logger)
	if err != nil {
		t.Fatalf("Executor Error: %v", err)
	}

	repo, hash := ensureDebugRepo()
	jobCtx := contextual.JobContext{
		Actor: contextual.ActorContext{
			TicketID:   "TEST-TICKET",
			ActorName:  "test-user",
			ActorEmail: "test-user@colony2",
		},
		Environment: contextual.EnvironmentContext{},
		Workflow: contextual.WorkflowContext{
			CellName: "cells/test-cell",
			CellPath: "cells/test-cell",
		},
		GitBase: contextual.GitBaseContext{
			BaseRepo:         repo,
			BaseRef:          hash,
			ResolvedBaseHash: hash,
		},
	}

	// Execute recipe
	result, err := standaloneExecutor.Execute(
		context.Background(),
		r,
		map[string]interface{}{},
		jobCtx,
		hash,
	)

	if err != nil {
		t.Fatalf("Execution Error: %v", err)
	}

	t.Logf("Result: %v", result)
}

// Reproduces the minimal executor path when the cell working path is set to ".".
// A nil-pointer panic was reported in production; this test guards against it.
func TestDebugExecutorRootCellPath(t *testing.T) {
	yamlStr := `id: hello_world
desc: A minimal recipe that demonstrates basic structure  
version: "1.0"
op: echo_activity
inputs:
  message: "Hello, World!"`

	var r recipe.Recipe
	err := yaml.Unmarshal([]byte(yamlStr), &r)
	if err != nil {
		t.Fatalf("Parse Error: %v", err)
	}

	logger := zaptest.NewLogger(t)
	a, err := ops.NewActivityRegistry()
	if err != nil {
		t.Fatalf("Registry Error: %v", err)
	}

	standaloneExecutor, err := executor.NewStandaloneExecutor(coreops.NewServiceDepsBuilder().Build(), a, logger)
	if err != nil {
		t.Fatalf("Executor Error: %v", err)
	}

	repo, hash := ensureDebugRepo()
	jobCtx := contextual.JobContext{
		Actor: contextual.ActorContext{
			TicketID:   "TEST-TICKET",
			ActorName:  "test-user",
			ActorEmail: "test-user@colony2",
		},
		Environment: contextual.EnvironmentContext{},
		Workflow: contextual.WorkflowContext{
			CellName: "root-cell",
			CellPath: ".",
		},
		GitBase: contextual.GitBaseContext{
			BaseRepo:         repo,
			BaseRef:          hash,
			ResolvedBaseHash: hash,
		},
	}

	_, err = standaloneExecutor.Execute(
		context.Background(),
		r,
		map[string]interface{}{},
		jobCtx,
		hash,
	)

	if err != nil {
		t.Fatalf("Execution Error: %v", err)
	}
}
