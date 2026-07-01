package submitjob

import (
	"context"
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
	"github.com/colony-2/c2j/pkg/childbroker"
	"github.com/colony-2/c2j/pkg/jobcontext"
	"github.com/colony-2/c2j/pkg/recipe"
	"github.com/colony-2/c2j/pkg/recipejob"
	"github.com/colony-2/c2j/pkg/starter"
	"github.com/colony-2/c2j/pkg/worker/compiler"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"gopkg.in/yaml.v3"
)

type submitResult struct {
	TenantID string `json:"tenant_id"`
	JobID    string `json:"job_id"`
	Recipe   string `json:"recipe"`
}

type targetCell = recipejob.ResolvedTarget

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

	parentContext, err := parentContextFromEnv(opts.TenantID)
	if err != nil {
		return err
	}
	broker, hasBroker, err := jobcontext.ChildJobBrokerFromEnv(os.Getenv)
	if err != nil {
		return err
	}
	if hasBroker && parentContext == nil {
		return fmt.Errorf("child job broker context requires current job context env")
	}

	submittedAt := time.Now().UTC()
	start, err := recipejob.BuildStartJob(recipejob.BuildStartJobRequest{
		TenantID:    opts.TenantID,
		Target:      target,
		Recipe:      recipeName,
		Inputs:      inputs,
		Artifacts:   submitArtifacts,
		Parent:      parentContext,
		SubmittedAt: &submittedAt,
	})
	if err != nil {
		return err
	}

	var result submitResult
	if hasBroker {
		var recipes []recipe.Recipe
		if embeddedRecipe != nil {
			recipes = append(recipes, *embeddedRecipe)
		}
		req, err := childbroker.NewSubmitRequest(ctx, start, submitArtifacts, recipes...)
		if err != nil {
			return err
		}
		resp, err := childbroker.Submit(ctx, broker, req)
		if err != nil {
			return err
		}
		result = submitResult{
			TenantID: resp.TenantID,
			JobID:    resp.JobID,
			Recipe:   resp.Recipe,
		}
		if err := writeSubmitResult(opts, result); err != nil {
			return err
		}
	} else if err := func() error {
		handle, err := swfruntime.Open(ctx, opts.SWFURL)
		if err != nil {
			return fmt.Errorf("open JobDB runtime: %w", err)
		}
		closeHandle := func(err error) error {
			return errors.Join(err, handle.Cleanup())
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
		JobDBURI: opts.JobDBURI,
		Stdin:    opts.Stdin,
		Stdout:   opts.Stdout,
		Stderr:   opts.Stderr,
	})
}

func parentContextFromEnv(tenantID string) (*jobcontext.Parent, error) {
	if parent, ok, err := jobcontext.ParentFromEnv(os.Getenv); err != nil {
		return nil, err
	} else if ok {
		if strings.TrimSpace(parent.TenantID) != strings.TrimSpace(tenantID) {
			return nil, fmt.Errorf("child job submission must use the current tenant %q; got %q", parent.TenantID, tenantID)
		}
		return &parent, nil
	}
	return nil, nil
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

func resolveTargetCell(ctx context.Context, opts Options) (targetCell, error) {
	return recipejob.ResolveTarget(ctx, recipejob.ResolveTargetRequest{
		WorkingDir: opts.WorkingDir,
		Cell:       opts.Cell,
		Self:       opts.Self,
		TenantID:   opts.TenantID,
	})
}

func resolveSelfTarget(ctx context.Context, workingDir string) (targetCell, error) {
	return recipejob.ResolveTarget(ctx, recipejob.ResolveTargetRequest{
		WorkingDir: workingDir,
		Self:       true,
	})
}

func resolveExplicitTarget(ctx context.Context, workingDir string, cell string) (targetCell, error) {
	return recipejob.ResolveTarget(ctx, recipejob.ResolveTargetRequest{
		WorkingDir: workingDir,
		Cell:       cell,
	})
}
