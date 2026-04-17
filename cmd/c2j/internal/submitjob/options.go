package submitjob

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/colony-2/c2j/cmd/c2j/internal/defaults"
	"github.com/colony-2/c2j/pkg/worker/compiler"
)

type Options struct {
	TenantID   string
	SWFURL     string
	Recipe     string
	RecipeFile string

	InputsJSON string
	InputsFile string

	ActorEmail string
	TicketID   string
	Self       bool
	Cell       string

	JSONOutput bool
	WorkingDir string
	Stdout     io.Writer
	Stderr     io.Writer
}

func (o *Options) Complete() {
	if o.SWFURL == "" {
		o.SWFURL = strings.TrimSpace(os.Getenv(defaults.SWFEnv))
	}
	if o.SWFURL == "" {
		o.SWFURL = defaults.SWFURL
	}
	if o.TenantID == "" {
		o.TenantID = strings.TrimSpace(os.Getenv(defaults.TenantEnv))
	}
	if o.TenantID == "" {
		o.TenantID = defaults.TenantID
	}
	if strings.TrimSpace(o.Recipe) == "" && strings.TrimSpace(o.RecipeFile) == "" {
		o.Recipe = compiler.DefaultRecipeName
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
}

func (o Options) Validate() error {
	if strings.TrimSpace(o.TenantID) == "" {
		return fmt.Errorf("--tenant-id is required (or %s)", defaults.TenantEnv)
	}
	if strings.TrimSpace(o.SWFURL) == "" {
		return fmt.Errorf("--swf-url is required (or %s)", defaults.SWFEnv)
	}
	if strings.TrimSpace(o.Recipe) != "" && strings.TrimSpace(o.RecipeFile) != "" {
		return fmt.Errorf("--recipe and --recipe-file are mutually exclusive")
	}
	switch {
	case o.Self && strings.TrimSpace(o.Cell) != "":
		return fmt.Errorf("--self and --cell are mutually exclusive")
	case !o.Self && strings.TrimSpace(o.Cell) == "":
		return fmt.Errorf("exactly one of --self or --cell is required")
	}
	if strings.TrimSpace(o.InputsJSON) != "" && strings.TrimSpace(o.InputsFile) != "" {
		return fmt.Errorf("--inputs-json and --inputs-file are mutually exclusive")
	}
	return nil
}
