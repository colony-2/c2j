package defaults

import (
	"context"
	"errors"
	"os"
	"strings"

	configpkg "github.com/colony-2/c2j/pkg/config"
)

func ResolveTenantID(ctx context.Context, workingDir string) (string, error) {
	workingDir = strings.TrimSpace(workingDir)
	if workingDir == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		workingDir = cwd
	}

	cfg, err := configpkg.LoadProjectConfig(workingDir)
	if err != nil {
		if errors.Is(err, configpkg.ErrConfigNotFound) {
			return "", nil
		}
		return "", err
	}

	tenantID, err := cfg.SelfTenantID(ctx)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(tenantID), nil
}
