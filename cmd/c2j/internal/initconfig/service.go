package initconfig

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/colony-2/c2j/cmd/c2j/internal/skillinstall"
	configpkg "github.com/colony-2/c2j/pkg/config"
	skillsbundle "github.com/colony-2/c2j/skills"
)

type Options struct {
	WorkingDir      string
	Force           bool
	StdoutOnly      bool
	InstallSkills   bool
	NoSkills        bool
	SkillsForce     bool
	SkillsScope     string
	InstallOpSkills bool
	Stdout          io.Writer
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

	if _, err := fmt.Fprintf(stdout, "wrote %s\n", configPath); err != nil {
		return err
	}

	if !opts.InstallSkills || opts.NoSkills {
		return nil
	}

	summary, err := skillinstall.InstallBundled(ctx, skillinstall.Options{
		Bundle:          skillsbundle.FS,
		WorkingDir:      workingDir,
		Scope:           opts.SkillsScope,
		Force:           opts.SkillsForce,
		IncludeOpSkills: opts.InstallOpSkills,
	})
	printSkillSummary(stdout, summary)
	if err != nil {
		return err
	}
	return nil
}

func printSkillSummary(stdout io.Writer, summary skillinstall.Summary) {
	if len(summary.Results) == 0 {
		return
	}
	if installed := summary.Installed(); len(installed) > 0 {
		fmt.Fprintf(stdout, "installed skills: %s\n", resultNames(installed, false))
	}
	if skipped := summary.Skipped(); len(skipped) > 0 {
		fmt.Fprintf(stdout, "skipped skills: %s\n", resultNames(skipped, true))
	}
	if failed := summary.Failed(); len(failed) > 0 {
		fmt.Fprintf(stdout, "failed skills: %s\n", resultNames(failed, true))
	}
}

func resultNames(results []skillinstall.SkillResult, includeReason bool) string {
	parts := make([]string, 0, len(results))
	for _, result := range results {
		name := result.Name
		reason := strings.TrimSpace(result.Reason)
		if result.Err != nil {
			reason = result.Err.Error()
		}
		if includeReason && reason != "" {
			name += " (" + reason + ")"
		}
		parts = append(parts, name)
	}
	return strings.Join(parts, ", ")
}
