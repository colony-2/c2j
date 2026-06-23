package submitjob

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/colony-2/c2j/cmd/c2j/internal/c2jops"
	"github.com/colony-2/c2j/cmd/c2j/internal/runjob"
	"github.com/colony-2/c2j/cmd/c2j/internal/swfruntime"
	configpkg "github.com/colony-2/c2j/pkg/config"
	"github.com/colony-2/c2j/pkg/contextual"
	"github.com/colony-2/c2j/pkg/recipe"
	"github.com/colony-2/c2j/pkg/starter"
	"github.com/colony-2/c2j/pkg/worker/compiler"
	"github.com/colony-2/c2j/pkg/workflowctl"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"gopkg.in/yaml.v3"
)

type submitResult struct {
	TenantID string `json:"tenant_id"`
	JobID    string `json:"job_id"`
	Recipe   string `json:"recipe"`
}

type targetCell struct {
	RepositorySource string
	DefaultRef       string
	CellName         string
}

func Run(ctx context.Context, opts Options) error {
	if err := opts.Complete(ctx); err != nil {
		return err
	}
	if err := opts.Validate(); err != nil {
		return err
	}

	c2jops.Register()

	target, err := resolveTargetCell(ctx, opts)
	if err != nil {
		return err
	}

	recipeName, embeddedRecipe, cleanup, err := loadRecipeStartContext(ctx, opts)
	if err != nil {
		return err
	}
	defer cleanup()

	submitArtifacts, err := loadSubmitArtifacts(opts, recipeName, embeddedRecipe != nil)
	if err != nil {
		return err
	}

	inputs, err := loadInputs(opts)
	if err != nil {
		return err
	}

	var result submitResult
	if err := func() error {
		handle, err := swfruntime.Open(ctx, opts.SWFURL)
		if err != nil {
			return fmt.Errorf("open SWF runtime: %w", err)
		}
		closeHandle := func(err error) error {
			return errors.Join(err, handle.Cleanup())
		}

		submittedAt := time.Now().UTC()
		start := workflowctl.StartJob{
			TenantId:   opts.TenantID,
			RecipeName: recipeName,
			Inputs:     inputs,
			Artifacts:  submitArtifacts,
			JobContext: contextual.JobContext{
				Workflow: contextual.WorkflowContext{
					CellName:  target.CellName,
					ProjectId: opts.TenantID,
				},
				GitBase: contextual.GitBaseContext{
					BaseRepo: target.RepositorySource,
					BaseRef:  target.DefaultRef,
				},
				RecipeSource: contextual.RecipeSourceContext{
					Repo: target.RepositorySource,
					Ref:  target.DefaultRef,
				},
			},
			GitRef:      target.DefaultRef,
			SubmittedAt: &submittedAt,
			InputHash:   hashInputs(inputs),
		}

		var jobKey jobdb.JobKey
		if embeddedRecipe != nil {
			jobKey, err = starter.StartRecipeJob(ctx, start, handle.Engine, *embeddedRecipe)
		} else {
			jobKey, err = starter.StartRecipeJob(ctx, start, handle.Engine)
		}
		if err != nil {
			return closeHandle(fmt.Errorf("submit job: %w", err))
		}

		result = submitResult{
			TenantID: opts.TenantID,
			JobID:    jobKey.JobId,
			Recipe:   recipeName,
		}
		return closeHandle(writeSubmitResult(opts, result))
	}(); err != nil {
		return err
	}

	if !opts.RunAfterSubmit {
		return nil
	}

	return runjob.Run(ctx, runjob.Options{
		JobID:    result.JobID,
		TenantID: opts.TenantID,
		SWFURL:   opts.SWFURL,
		Stdin:    opts.Stdin,
		Stdout:   opts.Stdout,
		Stderr:   opts.Stderr,
	})
}

func writeSubmitResult(opts Options, result submitResult) error {
	if opts.JSONOutput {
		return json.NewEncoder(opts.Stdout).Encode(result)
	}
	_, err := fmt.Fprintf(opts.Stdout, "submitted job tenant=%s job_id=%s recipe=%s\n", result.TenantID, result.JobID, result.Recipe)
	return err
}

func loadRecipeStart(opts Options) (string, *recipe.Recipe, func(), error) {
	return loadRecipeStartContext(context.Background(), opts)
}

func loadRecipeStartContext(ctx context.Context, opts Options) (string, *recipe.Recipe, func(), error) {
	recipeFile := strings.TrimSpace(opts.RecipeFile)
	if recipeFile == "" && compiler.IsLocalRecipeFileReference(opts.Recipe) {
		recipeFile = strings.TrimSpace(opts.Recipe)
	}
	if recipeFile != "" {
		absPath, err := absPathFromWorkingDir(opts.WorkingDir, recipeFile)
		if err != nil {
			return "", nil, nil, fmt.Errorf("resolve recipe file: %w", err)
		}
		f, err := os.Open(absPath)
		if err != nil {
			return "", nil, nil, fmt.Errorf("open recipe file: %w", err)
		}
		defer func() {
			_ = f.Close()
		}()
		rec, err := recipe.LoadRecipeFromReader(f)
		if err != nil {
			return "", nil, nil, fmt.Errorf("load recipe file: %w", err)
		}
		expanded, err := compiler.ResolveInlineRecipes(ctx, *rec, compiler.InlineResolutionOptions{
			ProjectID:  opts.TenantID,
			RootFile:   absPath,
			WorkingDir: opts.WorkingDir,
			Resolver:   compiler.NewRecipeSourceResolver(compiler.RecipeSourceResolverOptions{}),
		})
		if err != nil {
			return "", nil, nil, fmt.Errorf("resolve inline recipes: %w", err)
		}
		rec = &expanded.Recipe
		return rec.GetMetdata().ID, rec, func() {}, nil
	}

	selector := strings.TrimSpace(opts.Recipe)
	if err := compiler.ValidateRecipeSelector(selector); err != nil {
		return "", nil, nil, err
	}
	return selector, nil, func() {}, nil
}

func loadInputs(opts Options) (map[string]interface{}, error) {
	inputs := map[string]interface{}{}
	if strings.TrimSpace(opts.InputsJSON) != "" || strings.TrimSpace(opts.InputsFile) != "" {
		var raw []byte
		switch {
		case strings.TrimSpace(opts.InputsJSON) != "":
			raw = []byte(opts.InputsJSON)
		default:
			absPath, err := absPathFromWorkingDir(opts.WorkingDir, opts.InputsFile)
			if err != nil {
				return nil, fmt.Errorf("resolve inputs file: %w", err)
			}
			data, err := os.ReadFile(absPath)
			if err != nil {
				return nil, fmt.Errorf("read inputs file: %w", err)
			}
			raw = data
		}

		if err := json.Unmarshal(raw, &inputs); err == nil {
			goto mergePrompt
		}
		if err := yaml.Unmarshal(raw, &inputs); err == nil {
			goto mergePrompt
		}
		return nil, fmt.Errorf("decode inputs: expected a JSON or YAML object")
	}

mergePrompt:
	if opts.PromptSet || opts.Prompt != "" {
		if _, exists := inputs["prompt"]; exists {
			return nil, fmt.Errorf("prompt was provided both positionally and in recipe inputs")
		}
		inputs["prompt"] = opts.Prompt
	}
	return inputs, nil
}

func hashInputs(inputs map[string]interface{}) string {
	if len(inputs) == 0 {
		return ""
	}
	raw, err := json.Marshal(inputs)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func resolveTargetCell(ctx context.Context, opts Options) (targetCell, error) {
	if opts.Self || strings.TrimSpace(opts.Cell) == "" {
		return resolveSelfTarget(ctx, opts.WorkingDir)
	}
	return resolveExplicitTarget(ctx, opts.WorkingDir, opts.Cell)
}

func resolveSelfTarget(ctx context.Context, workingDir string) (targetCell, error) {
	cfg, err := configpkg.LoadProjectConfig(workingDir)
	if err != nil {
		if err == configpkg.ErrConfigNotFound {
			return targetCell{}, fmt.Errorf("current cell requires %s/%s or a supported auto-detected base", ".c2j", "config.yaml")
		}
		return targetCell{}, err
	}

	selfRepo, err := cfg.SelfRepo(ctx)
	if err != nil {
		return targetCell{}, err
	}
	selfRepo = strings.TrimSpace(selfRepo)
	if selfRepo == "" {
		if path := strings.TrimSpace(cfg.Path()); path != "" {
			return targetCell{}, fmt.Errorf("current cell requires self.repo to resolve from %s", path)
		}
		return targetCell{}, fmt.Errorf("current cell requires self.repo to resolve")
	}

	defaultRef, err := cfg.SelfRef(ctx)
	if err != nil {
		return targetCell{}, err
	}
	defaultRef = strings.TrimSpace(defaultRef)
	if defaultRef == "" {
		defaultRef = compiler.DefaultRecipeRef
	}

	repositorySource, err := compiler.NormalizeGitRepositorySource(selfRepo)
	if err != nil {
		return targetCell{}, err
	}

	cellName, ok := cfg.CellNameFromRepo(ctx, selfRepo)
	if !ok || strings.TrimSpace(cellName) == "" {
		cellName = compiler.RepositoryNameFromSource(selfRepo)
	}
	if cellName == "" {
		cellName = compiler.RepositoryNameFromSource(repositorySource)
	}

	return targetCell{
		RepositorySource: repositorySource,
		DefaultRef:       defaultRef,
		CellName:         cellName,
	}, nil
}

func resolveExplicitTarget(ctx context.Context, workingDir string, cell string) (targetCell, error) {
	cell = strings.TrimSpace(cell)
	if cell == "" {
		return targetCell{}, fmt.Errorf("--cell is required")
	}

	cfg, cfgErr := configpkg.LoadProjectConfig(workingDir)
	if cfgErr != nil && cfgErr != configpkg.ErrConfigNotFound {
		return targetCell{}, cfgErr
	}

	resolvedRepo, err := resolveCellInput(ctx, cfg, workingDir, cell)
	if err != nil {
		return targetCell{}, err
	}

	repositorySource, err := compiler.NormalizeGitRepositorySource(resolvedRepo)
	if err != nil {
		return targetCell{}, err
	}

	defaultRef := compiler.DefaultRecipeRef
	if cfg != nil {
		defaultRef, err = defaultRefForRepo(ctx, cfg, resolvedRepo)
		if err != nil {
			return targetCell{}, err
		}
	}

	cellName := ""
	if cfg != nil {
		if name, ok := cfg.CellNameFromRepo(ctx, resolvedRepo); ok {
			cellName = name
		}
	}
	if cellName == "" && cfg != nil && isConfiguredShortName(cell) {
		cellName = cell
	}
	if cellName == "" {
		cellName = compiler.RepositoryNameFromSource(resolvedRepo)
	}
	if cellName == "" {
		cellName = compiler.RepositoryNameFromSource(repositorySource)
	}

	return targetCell{
		RepositorySource: repositorySource,
		DefaultRef:       defaultRef,
		CellName:         cellName,
	}, nil
}

func absPathFromWorkingDir(workingDir string, value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", fmt.Errorf("path is required")
	}
	if filepath.IsAbs(value) {
		return value, nil
	}
	return filepath.Abs(filepath.Join(workingDir, value))
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
	if filepath.IsAbs(value) || strings.HasPrefix(value, "./") || strings.HasPrefix(value, "../") {
		return absPathFromWorkingDir(workingDir, value)
	}
	if strings.Contains(value, "/") || strings.Contains(value, ":") {
		return value, nil
	}
	return "", fmt.Errorf("cell %q looks like a short name; define pattern in .c2j/config.yaml or use an explicit repo/path", value)
}

func resolveCellInput(ctx context.Context, cfg *configpkg.ProjectConfig, workingDir string, value string) (string, error) {
	if cfg != nil {
		return cfg.ExpandCellName(ctx, value)
	}
	return resolveRepositoryInput(workingDir, value)
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
