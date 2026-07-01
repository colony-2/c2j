package submitjob

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/colony-2/c2j/cmd/c2j/internal/defaults"
	"github.com/colony-2/c2j/pkg/worker/compiler"
)

type Options struct {
	JobDBURI   string
	TenantID   string
	SWFURL     string
	Recipe     string
	RecipeFile string

	Prompt    string
	PromptSet bool

	InputsJSON string
	InputsFile string

	ArtifactSpecs []string

	Self bool
	Cell string

	RunAfterSubmit bool
	JSONOutput     bool
	WorkingDir     string
	Stdin          io.Reader
	Stdout         io.Writer
	Stderr         io.Writer
}

func (o *Options) Complete(ctx context.Context) error {
	if strings.TrimSpace(o.Recipe) == "" && strings.TrimSpace(o.RecipeFile) == "" {
		o.Recipe = compiler.DefaultRecipeName
	}
	if o.Stdout == nil {
		o.Stdout = os.Stdout
	}
	if o.Stderr == nil {
		o.Stderr = os.Stderr
	}
	if o.Stdin == nil {
		o.Stdin = os.Stdin
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
	target, err := defaults.ResolveJobDBTarget(ctx, o.WorkingDir, o.JobDBURI)
	if err != nil {
		return err
	}
	o.JobDBURI = target.URI
	o.SWFURL = target.RuntimeURL
	o.TenantID = target.TenantID
	return nil
}

func (o Options) Validate() error {
	if strings.TrimSpace(o.TenantID) == "" {
		return fmt.Errorf("--jobdb is required (or %s, or project jobdb)", defaults.JobDBEnv)
	}
	if strings.TrimSpace(o.SWFURL) == "" {
		return fmt.Errorf("--jobdb is required (or %s, or project jobdb)", defaults.JobDBEnv)
	}
	if strings.TrimSpace(o.Recipe) != "" && strings.TrimSpace(o.RecipeFile) != "" {
		return fmt.Errorf("--recipe and --recipe-file are mutually exclusive")
	}
	if o.Self && strings.TrimSpace(o.Cell) != "" {
		return fmt.Errorf("--self and --cell are mutually exclusive")
	}
	if strings.TrimSpace(o.InputsJSON) != "" && strings.TrimSpace(o.InputsFile) != "" {
		return fmt.Errorf("--inputs-json and --inputs-file are mutually exclusive")
	}
	if o.JSONOutput && o.RunAfterSubmit {
		return fmt.Errorf("--json and --run are mutually exclusive")
	}
	return nil
}
