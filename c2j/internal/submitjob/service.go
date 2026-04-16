package submitjob

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/colony-2/c2j/c2j/pkg/c2jops"
	configpkg "github.com/colony-2/c2j/c2jconfig/pkg/config"
	"github.com/colony-2/c2j/recipe-core/pkg/contextual"
	"github.com/colony-2/c2j/recipe-core/pkg/recipe"
	"github.com/colony-2/c2j/recipe-core/pkg/starter"
	"github.com/colony-2/c2j/recipe-core/pkg/workflowctl"
	"github.com/colony-2/c2j/recipe-worker/pkg/compiler"
	"github.com/colony-2/swf-go/pkg/swf"
	remoteruntime "github.com/colony-2/swf-go/pkg/swf/runtime/remote"
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
	opts.Complete()
	if err := opts.Validate(); err != nil {
		return err
	}

	c2jops.Register()

	target, err := resolveTargetCell(ctx, opts)
	if err != nil {
		return err
	}

	recipeName, embeddedRecipe, cleanup, err := loadRecipeStart(opts, target)
	if err != nil {
		return err
	}
	defer cleanup()

	inputs, err := loadInputs(opts)
	if err != nil {
		return err
	}

	runtime, err := remoteruntime.New(opts.SWFURL, &http.Client{Timeout: 30 * time.Second})
	if err != nil {
		return fmt.Errorf("create remote runtime: %w", err)
	}

	engine, err := swf.NewEngineBuilder().WithRuntime(runtime).BuildEngine()
	if err != nil {
		return fmt.Errorf("build engine: %w", err)
	}

	submittedAt := time.Now().UTC()
	start := workflowctl.StartJob{
		TenantId:   opts.TenantID,
		RecipeName: recipeName,
		Inputs:     inputs,
		JobContext: contextual.JobContext{
			Actor: contextual.ActorContext{
				TicketID:   strings.TrimSpace(opts.TicketID),
				ActorEmail: strings.TrimSpace(opts.ActorEmail),
			},
			Workflow: contextual.WorkflowContext{
				CellName:  target.CellName,
				CellPath:  ".",
				ProjectId: opts.TenantID,
			},
			GitBase: contextual.GitBaseContext{
				BaseRepo: target.RepositorySource,
				BaseRef:  target.DefaultRef,
			},
		},
		GitRef:      target.DefaultRef,
		SubmittedAt: &submittedAt,
		InputHash:   hashInputs(inputs),
	}

	var jobKey swf.JobKey
	if embeddedRecipe != nil {
		jobKey, err = starter.StartRecipeJob(ctx, start, engine, *embeddedRecipe)
	} else {
		jobKey, err = starter.StartRecipeJob(ctx, start, engine)
	}
	if err != nil {
		return fmt.Errorf("submit job: %w", err)
	}

	result := submitResult{
		TenantID: opts.TenantID,
		JobID:    jobKey.JobId,
		Recipe:   recipeName,
	}
	if opts.JSONOutput {
		return json.NewEncoder(opts.Stdout).Encode(result)
	}
	_, err = fmt.Fprintf(opts.Stdout, "submitted job tenant=%s job_id=%s recipe=%s\n", result.TenantID, result.JobID, result.Recipe)
	return err
}

func loadRecipeStart(opts Options, target targetCell) (string, *recipe.Recipe, func(), error) {
	if recipeFile := strings.TrimSpace(opts.RecipeFile); recipeFile != "" {
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
		return rec.GetMetdata().ID, rec, func() {}, nil
	}

	selector := strings.TrimSpace(opts.Recipe)
	if !compiler.IsGitRecipeSelector(selector) {
		builtSelector, err := compiler.BuildCellRecipeSelector(target.RepositorySource, selector, target.DefaultRef)
		if err != nil {
			return "", nil, nil, err
		}
		selector = builtSelector
	}
	if err := compiler.ValidateRecipeSelector(selector); err != nil {
		return "", nil, nil, err
	}
	return selector, nil, func() {}, nil
}

func loadInputs(opts Options) (map[string]interface{}, error) {
	if strings.TrimSpace(opts.InputsJSON) == "" && strings.TrimSpace(opts.InputsFile) == "" {
		return map[string]interface{}{}, nil
	}

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

	inputs := map[string]interface{}{}
	if err := json.Unmarshal(raw, &inputs); err == nil {
		return inputs, nil
	}
	if err := yaml.Unmarshal(raw, &inputs); err == nil {
		return inputs, nil
	}
	return nil, fmt.Errorf("decode inputs: expected a JSON or YAML object")
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
	if opts.Self {
		return resolveSelfTarget(ctx, opts.WorkingDir)
	}
	return resolveExplicitTarget(opts.WorkingDir, opts.Cell)
}

func resolveSelfTarget(ctx context.Context, workingDir string) (targetCell, error) {
	cfg, err := configpkg.LoadProjectConfig(workingDir)
	if err != nil {
		if err == configpkg.ErrConfigNotFound {
			return targetCell{}, fmt.Errorf("--self requires %s/%s", ".c2j", "config.yaml")
		}
		return targetCell{}, err
	}

	canonicalRepo, err := cfg.CanonicalRepo(ctx)
	if err != nil {
		return targetCell{}, err
	}
	canonicalRepo = strings.TrimSpace(canonicalRepo)
	if canonicalRepo == "" {
		return targetCell{}, fmt.Errorf("--self requires canonical_repo to resolve from %s", cfg.Path())
	}

	defaultRef, err := cfg.DefaultRef(ctx)
	if err != nil {
		return targetCell{}, err
	}
	defaultRef = strings.TrimSpace(defaultRef)
	if defaultRef == "" {
		defaultRef = compiler.DefaultRecipeRef
	}

	repositorySource, err := compiler.NormalizeGitRepositorySource(canonicalRepo)
	if err != nil {
		return targetCell{}, err
	}

	cellName := compiler.RepositoryNameFromSource(canonicalRepo)
	if cellName == "" {
		cellName = compiler.RepositoryNameFromSource(repositorySource)
	}

	return targetCell{
		RepositorySource: repositorySource,
		DefaultRef:       defaultRef,
		CellName:         cellName,
	}, nil
}

func resolveExplicitTarget(workingDir string, cell string) (targetCell, error) {
	cell = strings.TrimSpace(cell)
	if cell == "" {
		return targetCell{}, fmt.Errorf("--cell is required")
	}

	resolvedCell, err := resolveRepositoryInput(workingDir, cell)
	if err != nil {
		return targetCell{}, err
	}

	repositorySource, err := compiler.NormalizeGitRepositorySource(resolvedCell)
	if err != nil {
		return targetCell{}, err
	}

	cellName := compiler.RepositoryNameFromSource(cell)
	if cellName == "" {
		cellName = compiler.RepositoryNameFromSource(repositorySource)
	}

	return targetCell{
		RepositorySource: repositorySource,
		DefaultRef:       compiler.DefaultRecipeRef,
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
	if filepath.IsAbs(value) || value == "." || value == ".." || strings.HasPrefix(value, "./") || strings.HasPrefix(value, "../") {
		return absPathFromWorkingDir(workingDir, value)
	}

	candidate := filepath.Join(workingDir, value)
	if _, err := os.Stat(candidate); err == nil {
		return filepath.Abs(candidate)
	}
	return value, nil
}
