package configinspect

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"

	configpkg "github.com/colony-2/c2j/pkg/config"
)

type CellsOptions struct {
	WorkingDir string
	JSONOutput bool
	Stdout     io.Writer
}

type SelfOptions struct {
	WorkingDir string
	JSONOutput bool
	Stdout     io.Writer
}

type CellInfo struct {
	ShortName string `json:"short_name"`
	Repo      string `json:"repo"`
}

type SelfInfo struct {
	ShortName string `json:"short_name"`
	Repo      string `json:"repo"`
	TenantID  string `json:"tenant_id"`
	Ref       string `json:"ref"`
	RootRepo  string `json:"root_repo"`
	RootRef   string `json:"root_ref"`
	Pattern   string `json:"pattern"`
}

func RunCells(ctx context.Context, opts CellsOptions) error {
	cfg, stdout, err := loadConfigAndOutput(opts.WorkingDir, opts.Stdout)
	if err != nil {
		return err
	}

	repos, err := cfg.AllowedDependentRepos(ctx)
	if err != nil {
		return err
	}

	rows := make([]CellInfo, 0, len(repos))
	for _, repo := range repos {
		shortName, _ := cfg.CellNameFromRepo(ctx, repo)
		rows = append(rows, CellInfo{
			ShortName: strings.TrimSpace(shortName),
			Repo:      strings.TrimSpace(repo),
		})
	}

	if opts.JSONOutput {
		return json.NewEncoder(stdout).Encode(rows)
	}

	w := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	if _, err := fmt.Fprintln(w, "SHORT NAME\tREPO"); err != nil {
		return err
	}
	for _, row := range rows {
		name := row.ShortName
		if name == "" {
			name = "-"
		}
		if _, err := fmt.Fprintf(w, "%s\t%s\n", name, row.Repo); err != nil {
			return err
		}
	}
	return w.Flush()
}

func RunSelf(ctx context.Context, opts SelfOptions) error {
	cfg, stdout, err := loadConfigAndOutput(opts.WorkingDir, opts.Stdout)
	if err != nil {
		return err
	}

	repo, err := cfg.SelfRepo(ctx)
	if err != nil {
		return err
	}
	tenantID, err := cfg.SelfTenantID(ctx)
	if err != nil {
		return err
	}
	ref, err := cfg.SelfRef(ctx)
	if err != nil {
		return err
	}
	rootRepo, err := cfg.RootRepo(ctx)
	if err != nil {
		return err
	}
	rootRef, err := cfg.RootRef(ctx)
	if err != nil {
		return err
	}
	pattern, err := cfg.Pattern(ctx)
	if err != nil {
		return err
	}
	shortName, _ := cfg.CellNameFromRepo(ctx, repo)

	info := SelfInfo{
		ShortName: strings.TrimSpace(shortName),
		Repo:      strings.TrimSpace(repo),
		TenantID:  strings.TrimSpace(tenantID),
		Ref:       strings.TrimSpace(ref),
		RootRepo:  strings.TrimSpace(rootRepo),
		RootRef:   strings.TrimSpace(rootRef),
		Pattern:   strings.TrimSpace(pattern),
	}

	if opts.JSONOutput {
		return json.NewEncoder(stdout).Encode(info)
	}

	w := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	if _, err := fmt.Fprintln(w, "FIELD\tVALUE"); err != nil {
		return err
	}
	for _, field := range []struct {
		name  string
		value string
	}{
		{name: "short_name", value: displayValue(info.ShortName)},
		{name: "repo", value: displayValue(info.Repo)},
		{name: "tenant_id", value: displayValue(info.TenantID)},
		{name: "ref", value: displayValue(info.Ref)},
		{name: "root_repo", value: displayValue(info.RootRepo)},
		{name: "root_ref", value: displayValue(info.RootRef)},
		{name: "pattern", value: displayValue(info.Pattern)},
	} {
		if _, err := fmt.Fprintf(w, "%s\t%s\n", field.name, field.value); err != nil {
			return err
		}
	}
	return w.Flush()
}

func loadConfigAndOutput(workingDir string, stdout io.Writer) (*configpkg.ProjectConfig, io.Writer, error) {
	workingDir = strings.TrimSpace(workingDir)
	if workingDir == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return nil, nil, fmt.Errorf("resolve working directory: %w", err)
		}
		workingDir = cwd
	}

	cfg, err := configpkg.LoadProjectConfig(workingDir)
	if err != nil {
		if err == configpkg.ErrConfigNotFound {
			return nil, nil, fmt.Errorf("requires %s/%s", ".c2j", "config.yaml")
		}
		return nil, nil, err
	}

	if stdout == nil {
		stdout = os.Stdout
	}
	return cfg, stdout, nil
}

func displayValue(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "-"
	}
	return value
}
