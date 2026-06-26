package recipejob

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	configpkg "github.com/colony-2/c2j/pkg/config"
	"github.com/colony-2/c2j/pkg/worker/compiler"
)

type TargetSource string

const (
	TargetSourceSelf       TargetSource = "self"
	TargetSourceConfig     TargetSource = "config"
	TargetSourceRepository TargetSource = "repository"
	TargetSourceLocalPath  TargetSource = "local_path"
)

type ResolveTargetRequest struct {
	WorkingDir string
	Cell       string
	Self       bool
	TenantID   string
}

type ResolvedTarget struct {
	OriginalInput    string       `json:"original_input,omitempty"`
	ResolvedRepo     string       `json:"resolved_repo,omitempty"`
	RepositorySource string       `json:"repository_source"`
	DefaultRef       string       `json:"default_ref"`
	CellName         string       `json:"cell_name,omitempty"`
	TenantID         string       `json:"tenant_id,omitempty"`
	Source           TargetSource `json:"source"`
}

func ResolveTarget(ctx context.Context, req ResolveTargetRequest) (ResolvedTarget, error) {
	if req.Self && strings.TrimSpace(req.Cell) != "" {
		return ResolvedTarget{}, fmt.Errorf("self and cell are mutually exclusive")
	}

	workingDir, err := cleanWorkingDir(req.WorkingDir)
	if err != nil {
		return ResolvedTarget{}, err
	}

	if req.Self || strings.TrimSpace(req.Cell) == "" {
		return resolveSelfTarget(ctx, workingDir, strings.TrimSpace(req.TenantID))
	}
	return resolveExplicitTarget(ctx, workingDir, strings.TrimSpace(req.Cell), strings.TrimSpace(req.TenantID))
}

func NormalizeRepositorySource(source string) (string, error) {
	return compiler.NormalizeGitRepositorySource(source)
}

func RepositorySourcesEqual(left string, right string) (bool, error) {
	leftSource, err := compiler.NormalizeGitRepositorySource(left)
	if err != nil {
		return false, err
	}
	rightSource, err := compiler.NormalizeGitRepositorySource(right)
	if err != nil {
		return false, err
	}
	return leftSource == rightSource, nil
}

func resolveSelfTarget(ctx context.Context, workingDir string, tenantID string) (ResolvedTarget, error) {
	cfg, err := configpkg.LoadProjectConfig(workingDir)
	if err != nil {
		if err == configpkg.ErrConfigNotFound {
			return ResolvedTarget{}, fmt.Errorf("current cell requires %s/%s or a supported auto-detected base", ".c2j", "config.yaml")
		}
		return ResolvedTarget{}, err
	}

	selfRepo, err := cfg.SelfRepo(ctx)
	if err != nil {
		return ResolvedTarget{}, err
	}
	selfRepo = strings.TrimSpace(selfRepo)
	if selfRepo == "" {
		if path := strings.TrimSpace(cfg.Path()); path != "" {
			return ResolvedTarget{}, fmt.Errorf("current cell requires self.repo to resolve from %s", path)
		}
		return ResolvedTarget{}, fmt.Errorf("current cell requires self.repo to resolve")
	}

	defaultRef, err := cfg.SelfRef(ctx)
	if err != nil {
		return ResolvedTarget{}, err
	}
	defaultRef = strings.TrimSpace(defaultRef)
	if defaultRef == "" {
		defaultRef = compiler.DefaultRecipeRef
	}

	repositorySource, err := compiler.NormalizeGitRepositorySource(selfRepo)
	if err != nil {
		return ResolvedTarget{}, err
	}

	cellName, ok := cfg.CellNameFromRepo(ctx, selfRepo)
	if !ok || strings.TrimSpace(cellName) == "" {
		cellName = compiler.RepositoryNameFromSource(selfRepo)
	}
	if strings.TrimSpace(cellName) == "" {
		cellName = compiler.RepositoryNameFromSource(repositorySource)
	}

	return ResolvedTarget{
		ResolvedRepo:     selfRepo,
		RepositorySource: repositorySource,
		DefaultRef:       defaultRef,
		CellName:         cellName,
		TenantID:         tenantID,
		Source:           TargetSourceSelf,
	}, nil
}

func resolveExplicitTarget(ctx context.Context, workingDir string, cell string, tenantID string) (ResolvedTarget, error) {
	if strings.TrimSpace(cell) == "" {
		return ResolvedTarget{}, fmt.Errorf("--cell is required")
	}

	cfg, cfgErr := configpkg.LoadProjectConfig(workingDir)
	if cfgErr != nil && cfgErr != configpkg.ErrConfigNotFound {
		return ResolvedTarget{}, cfgErr
	}

	resolvedRepo, source, err := resolveCellInput(ctx, cfg, workingDir, cell)
	if err != nil {
		return ResolvedTarget{}, err
	}

	repositorySource, err := compiler.NormalizeGitRepositorySource(resolvedRepo)
	if err != nil {
		return ResolvedTarget{}, err
	}

	defaultRef := compiler.DefaultRecipeRef
	if cfg != nil {
		defaultRef, err = defaultRefForRepo(ctx, cfg, resolvedRepo)
		if err != nil {
			return ResolvedTarget{}, err
		}
	}
	defaultRef = strings.TrimSpace(defaultRef)
	if defaultRef == "" {
		defaultRef = compiler.DefaultRecipeRef
	}

	cellName := ""
	if cfg != nil {
		if name, ok := cfg.CellNameFromRepo(ctx, resolvedRepo); ok {
			cellName = name
		}
	}
	if strings.TrimSpace(cellName) == "" && cfg != nil && isConfiguredShortName(cell) {
		cellName = cell
	}
	if strings.TrimSpace(cellName) == "" {
		cellName = compiler.RepositoryNameFromSource(resolvedRepo)
	}
	if strings.TrimSpace(cellName) == "" {
		cellName = compiler.RepositoryNameFromSource(repositorySource)
	}

	return ResolvedTarget{
		OriginalInput:    cell,
		ResolvedRepo:     resolvedRepo,
		RepositorySource: repositorySource,
		DefaultRef:       defaultRef,
		CellName:         cellName,
		TenantID:         tenantID,
		Source:           source,
	}, nil
}

func resolveCellInput(ctx context.Context, cfg *configpkg.ProjectConfig, workingDir string, value string) (string, TargetSource, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", "", fmt.Errorf("cell value is required")
	}
	if isLocalPathInput(value) {
		resolved, err := resolveRepositoryInput(workingDir, value)
		return resolved, TargetSourceLocalPath, err
	}
	if cfg != nil {
		resolved, err := cfg.ExpandCellName(ctx, value)
		if err != nil {
			return "", "", err
		}
		if isConfiguredShortName(value) && !isRepositoryInput(value) {
			return resolved, TargetSourceConfig, nil
		}
		return resolved, TargetSourceRepository, nil
	}
	resolved, err := resolveRepositoryInput(workingDir, value)
	return resolved, TargetSourceRepository, err
}

func defaultRefForRepo(ctx context.Context, cfg *configpkg.ProjectConfig, repo string) (string, error) {
	rootRepo, err := cfg.RootRepo(ctx)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(rootRepo) != "" {
		rootSource, rootNormErr := compiler.NormalizeGitRepositorySource(rootRepo)
		repoSource, repoNormErr := compiler.NormalizeGitRepositorySource(repo)
		if rootNormErr == nil && repoNormErr == nil && rootSource == repoSource {
			return cfg.RootRef(ctx)
		}
	}

	selfRepo, err := cfg.SelfRepo(ctx)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(selfRepo) != "" {
		selfSource, selfNormErr := compiler.NormalizeGitRepositorySource(selfRepo)
		repoSource, repoNormErr := compiler.NormalizeGitRepositorySource(repo)
		if selfNormErr == nil && repoNormErr == nil && selfSource == repoSource {
			return cfg.SelfRef(ctx)
		}
	}

	return compiler.DefaultRecipeRef, nil
}

func resolveRepositoryInput(workingDir string, value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", fmt.Errorf("repository source is required")
	}
	if compiler.IsGitRecipeSelector(value) {
		return "", fmt.Errorf("repository source %q must be a git repository, not a recipe selector", value)
	}
	if strings.Contains(value, "://") || strings.HasPrefix(value, "git@") {
		return value, nil
	}
	if isLocalPathInput(value) {
		return absPathFromWorkingDir(workingDir, value)
	}
	if strings.Contains(value, "/") || strings.Contains(value, ":") {
		return value, nil
	}
	return "", fmt.Errorf("cell %q looks like a short name; define pattern in .c2j/config.yaml or use an explicit repo/path", value)
}

func absPathFromWorkingDir(workingDir string, value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", fmt.Errorf("path is required")
	}
	if filepath.IsAbs(value) {
		return value, nil
	}
	if strings.TrimSpace(workingDir) == "" {
		return "", fmt.Errorf("working directory is required to resolve %q", value)
	}
	return filepath.Abs(filepath.Join(workingDir, value))
}

func cleanWorkingDir(workingDir string) (string, error) {
	workingDir = strings.TrimSpace(workingDir)
	if workingDir == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("resolve working directory: %w", err)
		}
		workingDir = cwd
	}
	absPath, err := filepath.Abs(workingDir)
	if err != nil {
		return "", fmt.Errorf("resolve working directory: %w", err)
	}
	return absPath, nil
}

func isLocalPathInput(value string) bool {
	value = strings.TrimSpace(value)
	return filepath.IsAbs(value) || strings.HasPrefix(value, "./") || strings.HasPrefix(value, "../") || value == "." || value == ".."
}

func isRepositoryInput(value string) bool {
	value = strings.TrimSpace(value)
	return strings.Contains(value, "/") || strings.Contains(value, ":")
}

func isConfiguredShortName(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	if strings.Contains(value, "/") || strings.Contains(value, ":") {
		return false
	}
	for _, ch := range value {
		switch {
		case ch >= 'a' && ch <= 'z':
		case ch >= 'A' && ch <= 'Z':
		case ch >= '0' && ch <= '9':
		case ch == '_' || ch == '-' || ch == '.':
		default:
			return false
		}
	}
	return true
}
