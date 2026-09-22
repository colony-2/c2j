package defaults

import (
	"context"
	"errors"
	"os"
	"strings"

	"github.com/colony-2/c2j/internal/jobdbtarget"
	configpkg "github.com/colony-2/c2j/pkg/config"
)

type JobDBTarget = jobdbtarget.Target

func ResolveJobDBTarget(ctx context.Context, workingDir string, explicit string) (JobDBTarget, error) {
	raw := strings.TrimSpace(explicit)
	if raw == "" {
		raw = strings.TrimSpace(os.Getenv(JobDBEnv))
	}
	if raw == "" {
		cfg, err := loadProjectConfig(workingDir)
		if err != nil {
			return JobDBTarget{}, err
		}
		if cfg != nil {
			raw, err = cfg.JobDBURI(ctx)
			if err != nil {
				return JobDBTarget{}, err
			}
		}
	}
	if strings.TrimSpace(raw) == "" {
		return JobDBTarget{}, nil
	}
	return ParseJobDBURI(raw)
}

func ParseJobDBURI(raw string) (JobDBTarget, error) { return jobdbtarget.Parse(raw) }

func IsEmbeddedJobDBURI(raw string) bool { return jobdbtarget.IsEmbeddedJobDBURI(raw) }

func loadProjectConfig(workingDir string) (*configpkg.ProjectConfig, error) {
	workingDir = strings.TrimSpace(workingDir)
	if workingDir == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return nil, err
		}
		workingDir = cwd
	}

	cfg, err := configpkg.LoadProjectConfig(workingDir)
	if err != nil {
		if errors.Is(err, configpkg.ErrConfigNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return cfg, nil
}
