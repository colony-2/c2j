package testjob

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/colony-2/c2j/cmd/c2j/internal/defaults"
	"github.com/colony-2/c2j/pkg/worker/compiler"
)

const (
	exitCodeFailure = 1
	exitCodeCompile = 2
	exitCodeRun     = 3
	exitCodeCases   = 4
	exitCodeUsage   = 5
)

type Options struct {
	Recipe     string
	RecipeFile string

	FilePath string
	UseStdin bool
	Format   string
	CaseIDs  []string
	Strict   bool

	Self bool
	Cell string

	Parallelism   int
	FailFast      bool
	StopOnFailure bool

	Execution ExecutionOptions

	OutPath     string
	OutDir      string
	JSONLEvents string
	JSONOutput  bool

	TenantID   string
	WorkingDir string
	Stdin      io.Reader
	Stdout     io.Writer
	Stderr     io.Writer
}

type ExecutionOptions struct {
	Timeout          string
	ArtifactMode     string
	ArtifactMaxBytes int64
	EvaluationMode   string
}

func (o *Options) Complete(ctx context.Context) error {
	if o.Stdin == nil {
		o.Stdin = os.Stdin
	}
	if o.Stdout == nil {
		o.Stdout = os.Stdout
	}
	if o.Stderr == nil {
		o.Stderr = os.Stderr
	}
	if strings.TrimSpace(o.WorkingDir) == "" {
		if cwd, err := os.Getwd(); err == nil {
			o.WorkingDir = cwd
		}
	}
	if strings.TrimSpace(o.WorkingDir) != "" {
		if absPath, err := filepath.Abs(o.WorkingDir); err == nil {
			o.WorkingDir = absPath
		}
	}
	if strings.TrimSpace(o.Recipe) == "" && strings.TrimSpace(o.RecipeFile) == "" {
		o.Recipe = compiler.DefaultRecipeName
	}
	if o.Parallelism <= 0 {
		o.Parallelism = 4
	}
	if strings.TrimSpace(o.Execution.ArtifactMode) == "" {
		o.Execution.ArtifactMode = "none"
	}
	if o.Execution.ArtifactMaxBytes <= 0 {
		o.Execution.ArtifactMaxBytes = 64 * 1024
	}
	if strings.TrimSpace(o.Execution.EvaluationMode) == "" {
		o.Execution.EvaluationMode = "enforce"
	}
	o.TenantID = defaults.EmbeddedTenantID
	return nil
}

func (o Options) ValidateSuiteInput() error {
	if strings.TrimSpace(o.Recipe) != "" && strings.TrimSpace(o.RecipeFile) != "" {
		return fmt.Errorf("--recipe and --recipe-file are mutually exclusive")
	}
	if o.Self && strings.TrimSpace(o.Cell) != "" {
		return fmt.Errorf("--self and --cell are mutually exclusive")
	}
	if o.UseStdin && strings.TrimSpace(o.FilePath) != "" {
		return fmt.Errorf("--stdin and --file are mutually exclusive")
	}
	if !o.UseStdin && strings.TrimSpace(o.FilePath) == "" {
		return fmt.Errorf("one of --file or --stdin is required")
	}
	return nil
}

func defaultOutDir() string {
	return filepath.Join(".c2j", "test-results", time.Now().UTC().Format("20060102T150405Z"))
}

type exitError struct {
	code int
	err  error
}

func (e exitError) Error() string {
	if e.err == nil {
		return fmt.Sprintf("exit %d", e.code)
	}
	return e.err.Error()
}

func (e exitError) Unwrap() error { return e.err }
func (e exitError) ExitCode() int { return e.code }
