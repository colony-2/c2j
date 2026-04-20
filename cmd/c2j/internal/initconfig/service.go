package initconfig

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	configpkg "github.com/colony-2/c2j/pkg/config"
)

type Options struct {
	WorkingDir string
	Force      bool
	StdoutOnly bool
	Stdout     io.Writer
}

func Run(ctx context.Context, opts Options) error {
	workingDir := strings.TrimSpace(opts.WorkingDir)
	if workingDir == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("resolve working directory: %w", err)
		}
		workingDir = cwd
	}

	stdout := opts.Stdout
	if stdout == nil {
		stdout = os.Stdout
	}

	rendered, err := configpkg.RenderInitConfigTemplate(ctx, workingDir)
	if err != nil {
		return err
	}

	if opts.StdoutOnly {
		_, err := stdout.Write(rendered)
		return err
	}

	configPath := filepath.Join(workingDir, ".c2j", "config.yaml")
	if !opts.Force {
		if _, err := os.Stat(configPath); err == nil {
			return fmt.Errorf("%s already exists", configPath)
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("stat %s: %w", configPath, err)
		}
	}

	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", filepath.Dir(configPath), err)
	}
	if err := os.WriteFile(configPath, rendered, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", configPath, err)
	}

	_, err = fmt.Fprintf(stdout, "wrote %s\n", configPath)
	return err
}
