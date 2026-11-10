package submitjob

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/colony-2/c2j/cmd/c2j/internal/defaults"
	"github.com/colony-2/c2j/cmd/c2j/internal/executionflags"
	"github.com/colony-2/c2j/pkg/execution"
)

type Options struct {
	GoalID                string
	JobID                 string
	RequestFile           string
	ReplayFile            string
	ExecutionRequirements execution.Requirements
	ExecutionFlags        executionflags.Options
	JobDBURI              string
	TenantID              string
	SWFURL                string
	Recipe                string
	RecipeFile            string

	Prompt        string
	PromptSet     bool
	PromptFile    string
	PromptFileSet bool
	Title         string
	TitleSet      bool

	InputsJSON string
	InputsFile string

	ArtifactSpecs []string

	FromWorkspace    bool
	FromWorkspaceNow bool
	Self             bool
	CellRepo         string
	Cell             string

	RunAfterSubmit bool
	JSONOutput     bool
	WorkingDir     string
	Stdin          io.Reader
	Stdout         io.Writer
	Stderr         io.Writer
}

func (o *Options) Complete(ctx context.Context) error {
	r, err := o.ExecutionRequirements.Normalize()
	if err != nil {
		return err
	}
	o.ExecutionRequirements = r
	if o.RunAfterSubmit {
		if _, err := o.ExecutionFlags.Parse(os.LookupEnv); err != nil {
			return err
		}
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
	if o.ReplayFile != "" && (o.GoalID != "" || o.JobID != "" || o.RequestFile != "" || o.PromptSet || o.Prompt != "" || o.PromptFile != "" || o.Title != "" || o.PromptFileSet || o.TitleSet || o.InputsJSON != "" || o.InputsFile != "" || o.Recipe != "" || o.RecipeFile != "" || len(o.ArtifactSpecs) > 0 || o.FromWorkspace || o.FromWorkspaceNow || o.Self || o.Cell != "" || o.CellRepo != "" || !o.ExecutionRequirements.Empty()) {
		return fmt.Errorf("--replay cannot override the retained semantic request")
	}
	if o.GoalID != "" && (strings.TrimSpace(o.JobID) == "" || o.RequestFile == "") {
		return fmt.Errorf("--goal requires --job-id and --request-file for reconciliation")
	}
	if o.RequestFile != "" && o.GoalID == "" {
		return fmt.Errorf("--request-file requires --goal")
	}
	if o.FromWorkspace && o.FromWorkspaceNow {
		return fmt.Errorf("--from-workspace and --from-workspace-now are mutually exclusive")
	}
	if !o.RunAfterSubmit && o.ExecutionFlags.HasArguments() {
		return fmt.Errorf("allocation flags require --run on submit; use --require-* to override job requirements")
	}
	if strings.TrimSpace(o.TenantID) == "" {
		return fmt.Errorf("--jobdb is required (or %s, or project jobdb)", defaults.JobDBEnv)
	}
	if strings.TrimSpace(o.SWFURL) == "" {
		return fmt.Errorf("--jobdb is required (or %s, or project jobdb)", defaults.JobDBEnv)
	}
	if strings.TrimSpace(o.Recipe) != "" && strings.TrimSpace(o.RecipeFile) != "" {
		return fmt.Errorf("--advanced-recipe and --advanced-recipe-file are mutually exclusive")
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
