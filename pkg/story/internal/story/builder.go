package story

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/colony-2/c2j/pkg/recipe"
	"github.com/colony-2/c2j/pkg/starter"
	"github.com/colony-2/c2j/pkg/story/internal/model"
	"github.com/colony-2/c2j/pkg/template"
	"github.com/colony-2/c2j/pkg/worker/compiler"
	"github.com/colony-2/swf-go/pkg/swf"
)

type replayJobRunner interface {
	ReplayJobRun(ctx context.Context, req swf.ReplayRunRequest) (swf.JobData, error)
}

// BuildJobRunStory replays a job using swf.ReplayJobRun and collects a recipe-centric story
// by decorating the recipe executor + observing SWF replay events.
func BuildJobRunStory(ctx context.Context, engine replayJobRunner, jobKey swf.JobKey, celProvider template.CELOptionsProvider, logger *slog.Logger, rootResolvers ...compiler.RecipeSourceResolver) (*model.JobRunStory, error) {
	if engine == nil {
		return nil, fmt.Errorf("engine is required")
	}
	if err := jobKey.Validate(); err != nil {
		return nil, err
	}
	if logger == nil {
		logger = slog.Default()
	}

	var rootResolver compiler.RecipeSourceResolver
	if len(rootResolvers) > 0 {
		rootResolver = rootResolvers[0]
	}
	if rootResolver == nil {
		rootResolver = replayRootSourceResolver{}
	}

	rec := newReplayStoryRecorder(jobKey, logger)
	obs := &replayStoryObserver{rec: rec}

	jobWorker := compiler.NewRecipeJobWorker(compiler.RecipeJobWorkerOptions{
		CELOptionsProvider:     celProvider,
		OnRecipeLoaded:         rec.OnRecipeLoaded,
		OnRecipeSourceResolved: rec.OnRecipeSourceResolved,
		RootSourceResolver:     rootResolver,
		ExecutorFactory: func() compiler.RecipeExecutor {
			tree := rec.EnsureAttemptTree()
			return newRecordingExecutor(compiler.DefaultRecipeExecutor{}, tree, rec)
		},
	})

	_, replayErr := engine.ReplayJobRun(ctx, swf.ReplayRunRequest{
		JobKey:    jobKey,
		Observer:  obs,
		JobWorker: jobWorker,
	})

	story := rec.BuildStory(replayErr)

	// For jobs that ended with errors, replay returns the same error you would see at runtime.
	// The story is still useful; only propagate determinism errors so the API can surface 409.
	if replayErr == nil {
		return story, nil
	}

	if errors.Is(replayErr, context.Canceled) || errors.Is(replayErr, context.DeadlineExceeded) {
		return nil, replayErr
	}
	if errors.Is(replayErr, swf.ErrJobNotFound) {
		return nil, replayErr
	}
	if errors.Is(replayErr, swf.ErrWorkflowNotDeterministic) {
		return story, replayErr
	}
	if isReplayCacheMissErr(replayErr) {
		return story, nil
	}
	// Default: swallow runtime job errors and return the story.
	return story, nil
}

func mapStoryStatusFromReplayErr(err error) model.WorkflowStatus {
	if err == nil {
		return model.WorkflowStatusCompleted
	}
	if isReplayCacheMissErr(err) {
		return model.WorkflowStatusRunning
	}
	var te swf.TimeoutError
	if errors.As(err, &te) {
		return model.WorkflowStatusTimedOut
	}
	return model.WorkflowStatusFailed
}

func recipeSourceArtifactName(recipeName string) string {
	recipeName = strings.TrimSpace(recipeName)
	if recipeName == "" {
		return ""
	}
	return recipeName + starter.RecipeArtifactSuffix
}

func toRecipeNameFromIDFallback(recipeID, recipeName string) string {
	recipeID = strings.TrimSpace(recipeID)
	if recipeID != "" {
		return recipeID
	}
	return strings.TrimSpace(recipeName)
}

type replayRootSourceResolver struct{}

func (replayRootSourceResolver) Resolve(context.Context, string, string) (compiler.RecipeSourceResolution, error) {
	return compiler.RecipeSourceResolution{}, fmt.Errorf("recipe source resolver unavailable during replay")
}

func (replayRootSourceResolver) Load(context.Context, string, compiler.RecipeSourceResolution) (recipe.Recipe, error) {
	return recipe.Recipe{}, fmt.Errorf("recipe source loader unavailable during replay")
}
