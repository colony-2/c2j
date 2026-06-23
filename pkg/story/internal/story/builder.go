package story

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	"github.com/colony-2/c2j/pkg/starter"
	"github.com/colony-2/c2j/pkg/story/internal/model"
	storylive "github.com/colony-2/c2j/pkg/story/live"
	"github.com/colony-2/c2j/pkg/template"
	"github.com/colony-2/c2j/pkg/worker/compiler"
	"github.com/colony-2/jobdb/pkg/jobdb"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
)

type replayJobRunner interface {
	ReplayJobRun(ctx context.Context, req jobworkflow.ReplayRunRequest) (jobdb.JobData, error)
}

// BuildJobRunStory replays a job using swf.ReplayJobRun and collects a recipe-centric story
// by decorating the recipe executor + observing SWF replay events.
func BuildJobRunStory(ctx context.Context, engine replayJobRunner, jobKey jobdb.JobKey, celProvider template.CELOptionsProvider, logger *slog.Logger, rootResolvers ...compiler.RecipeSourceResolver) (*model.JobRunStory, error) {
	return storylive.BuildJobRunStory(ctx, engine, jobKey, celProvider, logger, rootResolvers...)
}

func mapStoryStatusFromReplayErr(err error) model.WorkflowStatus {
	if err == nil {
		return model.WorkflowStatusCompleted
	}
	if isReplayCacheMissErr(err) {
		return model.WorkflowStatusRunning
	}
	var te jobdb.TimeoutError
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

func isReplayCacheMissErr(err error) bool {
	if err == nil {
		return false
	}
	var miss jobworkflow.ReplayCacheMissError
	if errors.As(err, &miss) {
		return true
	}
	return strings.Contains(strings.ToLower(err.Error()), "replay cache miss:")
}
