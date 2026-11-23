package submitjob

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/colony-2/c2j/cmd/c2j/internal/c2jops"
	"github.com/colony-2/c2j/cmd/c2j/internal/defaults"
	"github.com/colony-2/c2j/cmd/c2j/internal/runjob"
	"github.com/colony-2/c2j/cmd/c2j/internal/swfruntime"
	"github.com/colony-2/c2j/pkg/cellref"
	"github.com/colony-2/c2j/pkg/childbroker"
	"github.com/colony-2/c2j/pkg/executioncatalog"
	"github.com/colony-2/c2j/pkg/jobref"
	"github.com/colony-2/c2j/pkg/recipe"
	"github.com/colony-2/c2j/pkg/recipejob"
	"github.com/colony-2/c2j/pkg/starter"
	"github.com/colony-2/c2j/pkg/worker/compiler"
	"github.com/colony-2/c2j/pkg/workspacesubmit"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/segmentio/ksuid"
	"gopkg.in/yaml.v3"
)

type submitResult struct {
	Cell     string `json:"cell"`
	CellID   string `json:"cell_id"`
	CellRepo string `json:"cell_repo"`
	GoalID   string `json:"goal_id,omitempty"`
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
	if opts.ReplayFile != "" {
		return replaySelected(ctx, opts, opts.route)
	}
	inputs, err := loadInputs(opts)
	if err != nil {
		return err
	}
	if err := requirePrompt(&opts, inputs); err != nil {
		return err
	}
	dependencies, err := opts.dependencies()
	if err != nil {
		return err
	}
	var availableAt *time.Time
	if len(opts.WaitUntil) == 1 {
		clock := opts.Clock
		if clock == nil {
			clock = time.Now
		}
		resolved, zone, err := resolveWaitTime(opts.WaitUntil[0], clock(), opts.Location)
		if err != nil {
			return err
		}
		availableAt = &resolved
		if _, err := fmt.Fprintf(opts.Stderr, "Requested earliest start: %s (input zone: %s)\n", resolved.Format(time.RFC3339Nano), zone); err != nil {
			return err
		}
	}

	c2jops.Register()

	route := opts.route
	parentContext, broker, hasBroker := route.parent, route.broker, route.hasBroker

	if opts.FromWorkspace && !hasBroker {
		return fmt.Errorf("--from-workspace requires current task workspace context; use --from-workspace-now outside a running task")
	}
	var workspaceInfo *childbroker.WorkspaceInfo
	if hasBroker && (opts.FromWorkspace || opts.FromWorkspaceNow) {
		info, err := childbroker.Workspace(ctx, broker)
		if err != nil {
			return err
		}
		workspaceInfo = &info
	}
	target, workspaceSource, err := resolveSubmissionWorkspace(ctx, opts, workspaceInfo)
	if err != nil {
		return err
	}

	runtimeHandle, err := route.openRuntime(ctx, opts.SWFURL, swfruntime.Open)
	if err != nil {
		return err
	}
	defer func() {
		if runtimeHandle != nil {
			runtimeHandle.Cleanup()
		}
	}()
	if !hasBroker {
		p, e := defaults.ExecutionBasePolicy(opts.SWFURL, opts.TenantID, runtimeHandle.Configuration)
		if e != nil {
			return e
		}
		ctx = executioncatalog.WithPolicy(ctx, p)
	}
	if hasBroker {
		target, err = recipejob.ResolveRegisteredTarget(ctx, target, func(ctx context.Context, name string) (jobdb.ResolvedCell, error) {
			return childbroker.ResolveCell(ctx, broker, name)
		})
	} else {
		target, err = recipejob.ResolveRegisteredTarget(ctx, target, cellref.Registry(runtimeHandle.Runtime, opts.TenantID))
	}
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

	submittedAt := time.Now().UTC()
	jobID := opts.JobID
	if jobID == "" {
		jobID = ksuid.New().String()
	}
	start, err := recipejob.BuildStartJob(recipejob.BuildStartJobRequest{
		Dependencies:          dependencies,
		AvailableAt:           availableAt,
		ExecutionRequirements: opts.ExecutionRequirements,
		TenantID:              opts.TenantID,
		JobID:                 jobID,
		GoalID:                opts.GoalID,
		Target:                target,
		Recipe:                recipeName,
		Inputs:                inputs,
		Artifacts:             submitArtifacts,
		Parent:                parentContext,
		SubmittedAt:           &submittedAt,
	})
	if err != nil {
		return err
	}

	if workspaceSource != nil {
		start, err = workspaceSource.Apply(ctx, start, workspacesubmit.Now, strings.TrimSpace(opts.Cell) != "")
		if err != nil {
			return err
		}
	}
	if opts.RequestFile != "" {
		var recipes []recipe.Recipe
		if embeddedRecipe != nil {
			recipes = append(recipes, *embeddedRecipe)
		}
		var descriptor starter.Descriptor
		if hasBroker {
			req, e := childbroker.NewSubmitRequest(ctx, start, start.Artifacts, recipes...)
			if e != nil {
				return e
			}
			if opts.FromWorkspace {
				req.WorkspaceMode = workspacesubmit.Start
			}
			if opts.FromWorkspaceNow {
				req.WorkspaceMode = workspacesubmit.Now
			}
			req.ExplicitTarget = strings.TrimSpace(opts.Cell) != ""
			descriptor, err = childbroker.Prepare(ctx, broker, req)
		} else {
			var job jobdb.SubmitJob
			job, err = starter.PrepareRecipeJob(ctx, start, starter.StartRecipeJobOptions{}, recipes...)
			if err == nil {
				descriptor, err = starter.NewDescriptor(ctx, job)
			}
		}
		if err != nil {
			return err
		}
		if err := validatePreparedWaits(descriptor, dependencies, availableAt); err != nil {
			return err
		}
		retained := retainedSubmission{Version: 1, RuntimeURL: opts.SWFURL, TenantID: opts.TenantID, Broker: hasBroker, Descriptor: descriptor}
		if err := saveRetained(opts.RequestFile, retained); err != nil {
			return err
		}
		return publishRetained(ctx, opts, retained, broker, runtimeHandle)
	}
	var result submitResult
	if hasBroker {
		var recipes []recipe.Recipe
		if embeddedRecipe != nil {
			recipes = append(recipes, *embeddedRecipe)
		}
		req, err := childbroker.NewSubmitRequest(ctx, start, start.Artifacts, recipes...)
		if err != nil {
			return err
		}
		if opts.FromWorkspace {
			req.WorkspaceMode = workspacesubmit.Start
		}
		if opts.FromWorkspaceNow {
			req.WorkspaceMode = workspacesubmit.Now
		}
		req.ExplicitTarget = strings.TrimSpace(opts.Cell) != ""
		resp, err := childbroker.Submit(ctx, broker, req)
		if err != nil {
			return err
		}
		ref := jobref.Ref{Cell: resp.Cell, CellID: resp.CellID, CellRepo: resp.CellRepo, TenantID: resp.TenantID, JobID: resp.JobID, GoalID: resp.GoalID}
		if err := ref.ValidateSelected(jobdb.SubmitJob{TenantId: start.TenantId, JobID: start.JobID, GoalID: resp.GoalID}); err != nil {
			return err
		}
		if resp.Cell != target.CellName || resp.CellID != target.CellID || resp.CellRepo != target.CellRepo || resp.Recipe != recipeName {
			return jobdb.ErrSubmissionReceiptUnavailable
		}
		result = submitResult{
			Cell: resp.Cell, CellID: resp.CellID, CellRepo: resp.CellRepo,
			TenantID: resp.TenantID,
			GoalID:   resp.GoalID,
			JobID:    resp.JobID,
			Recipe:   resp.Recipe,
		}
		if err := writeSubmitResult(opts, result); err != nil {
			return err
		}
	} else if err := func() error {
		handle := runtimeHandle
		closeHandle := func(err error) error { return err }

		var jobKey jobref.Ref
		if embeddedRecipe != nil {
			jobKey, err = starter.StartRecipeJob(ctx, start, handle.Engine, *embeddedRecipe)
		} else {
			jobKey, err = starter.StartRecipeJob(ctx, start, handle.Engine)
		}
		if err != nil {
			return closeHandle(fmt.Errorf("submit job: %w", err))
		}

		result = submitResult{
			Cell: jobKey.Cell, CellID: jobKey.CellID, CellRepo: jobKey.CellRepo,
			TenantID: jobKey.TenantID,
			JobID:    jobKey.JobID,
			GoalID:   jobKey.GoalID,
			Recipe:   recipeName,
		}
		return closeHandle(writeSubmitResult(opts, result))
	}(); err != nil {
		return err
	}

	if !opts.RunAfterSubmit {
		return nil
	}

	if runtimeHandle != nil {
		runtimeHandle.Cleanup()
		runtimeHandle = nil
	}
	return runjob.Run(ctx, runjob.Options{
		ExecutionFlags: opts.ExecutionFlags,
		JobID:          result.JobID,
		JobDBURI:       opts.JobDBURI,
		Stdin:          opts.Stdin,
		Stdout:         opts.Stdout,
		Stderr:         opts.Stderr,
	})
}

func writeSubmitResult(opts Options, result submitResult) error {
	result.GoalID = ""
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

	selector := selectedRecipeName(opts)
	if err := compiler.ValidateRecipeSelector(selector); err != nil {
		return "", nil, nil, err
	}
	return selector, nil, func() {}, nil
}

func selectedRecipeName(opts Options) string {
	if name := strings.TrimSpace(opts.Recipe); name != "" {
		return name
	}
	return compiler.SharedBuildRecipeSelector
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
	if inputs == nil {
		return nil, fmt.Errorf("decode inputs: expected a JSON or YAML object, not null")
	}
	if opts.PromptFileSet || opts.PromptFile != "" {
		if opts.PromptSet || opts.Prompt != "" {
			return nil, fmt.Errorf("--prompt-file and a positional prompt are mutually exclusive")
		}
		if _, exists := inputs["prompt"]; exists {
			return nil, fmt.Errorf("prompt was provided both via --prompt-file and in recipe inputs")
		}
		prompt, err := readPromptFile(opts)
		if err != nil {
			return nil, err
		}
		inputs["prompt"] = prompt
	}
	if opts.PromptSet || opts.Prompt != "" {
		if _, exists := inputs["prompt"]; exists {
			return nil, fmt.Errorf("prompt was provided both positionally and in recipe inputs")
		}
		inputs["prompt"] = opts.Prompt
	}
	if opts.TitleSet || opts.Title != "" {
		if _, exists := inputs["title"]; exists {
			return nil, fmt.Errorf("title was provided both via --title and in recipe inputs")
		}
		inputs["title"] = opts.Title
	}
	if strings.TrimSpace(opts.RecipeFile) == "" {
		name := strings.TrimSpace(opts.Recipe)
		if name == "" || name == compiler.DefaultRecipeName {
			name = compiler.DefaultRecipeName
			if value, exists := inputs["type"]; exists {
				if supplied, ok := value.(string); !ok || supplied != name {
					return nil, fmt.Errorf("inputs.type must be %q for this submission", name)
				}
			}
			inputs["type"] = name
		}
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
		CellRepo:   opts.CellRepo,
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
