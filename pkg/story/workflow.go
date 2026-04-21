package story

import (
	"context"
	"errors"

	workflowapi "github.com/colony-2/c2j/pkg/story/api"
	"github.com/colony-2/c2j/pkg/story/internal/model"
	"github.com/colony-2/c2j/pkg/story/internal/service"
	"github.com/colony-2/c2j/pkg/template"
	"github.com/colony-2/c2j/pkg/worker/compiler"
	"github.com/colony-2/strata-go/pkg/client"
	"github.com/colony-2/swf-go/pkg/swf"
)

type (
	WorkflowStatus                = model.WorkflowStatus
	ChapterStatus                 = model.ChapterStatus
	Actor                         = model.Actor
	ActorType                     = model.ActorType
	ActorUser                     = model.ActorUser
	ActorAgent                    = model.ActorAgent
	ArtifactReference             = model.ArtifactReference
	ChapterDetail                 = model.ChapterDetail
	WorkflowSummary               = model.WorkflowSummary
	WorkflowDetail                = model.WorkflowDetail
	WorkflowOutcome               = model.WorkflowOutcome
	JobRunStory                   = model.JobRunStory
	JobRunStoryRecipe             = model.JobRunStoryRecipe
	JobRunStoryRecipeSource       = model.JobRunStoryRecipeSource
	JobRunStoryNode               = model.JobRunStoryNode
	JobRunStoryNodeKind           = model.JobRunStoryNodeKind
	JobRunStoryNodeStatus         = model.JobRunStoryNodeStatus
	JobRunStoryError              = model.JobRunStoryError
	JobRunStoryTransitionEval     = model.JobRunStoryTransitionEval
	JobRunStoryTransitionDecision = model.JobRunStoryTransitionDecision
	Project                       = workflowapi.Project
	Cell                          = workflowapi.Cell
	CellSearchFilter              = workflowapi.SearchFilter
	ProjectService                = workflowapi.ProjectService
	CellService                   = workflowapi.CellService
	CellIterator                  = workflowapi.CellIterator
	MockProjectService            = workflowapi.MockProjectService
	MockCellService               = workflowapi.MockCellService
	ListWorkflowsRequest          = model.ListWorkflowsRequest
	GetWorkflowRequest            = model.GetWorkflowRequest
	GetWorkflowOutcomeRequest     = model.GetWorkflowOutcomeRequest
	GetJobRunStoryRequest         = model.GetJobRunStoryRequest
	RestartRecipeJobRequest       = model.RestartRecipeJobRequest
	RestartRecipeJobResponse      = model.RestartRecipeJobResponse
	GetArtifactByOrdinalRequest   = model.GetArtifactByOrdinalRequest
	GetWorkflowArtifactRequest    = model.GetWorkflowArtifactRequest
	StartWorkflowRequest          = model.StartWorkflowRequest
	ArtifactData                  = model.ArtifactData
	RecipeProvider                = service.RecipeProvider
	RecipeSourceResolver          = compiler.RecipeSourceResolver
)

const (
	WorkflowStatusRunning    = model.WorkflowStatusRunning
	WorkflowStatusCompleted  = model.WorkflowStatusCompleted
	WorkflowStatusFailed     = model.WorkflowStatusFailed
	WorkflowStatusCanceled   = model.WorkflowStatusCanceled
	WorkflowStatusTerminated = model.WorkflowStatusTerminated
	WorkflowStatusTimedOut   = model.WorkflowStatusTimedOut
	WorkflowStatusUnknown    = model.WorkflowStatusUnknown

	ChapterStatusPending   = model.ChapterStatusPending
	ChapterStatusRunning   = model.ChapterStatusRunning
	ChapterStatusCompleted = model.ChapterStatusCompleted
	ChapterStatusFailed    = model.ChapterStatusFailed
	ChapterStatusSkipped   = model.ChapterStatusSkipped

	ActorTypeUser  = model.ActorTypeUser
	ActorTypeAgent = model.ActorTypeAgent
)

var (
	ErrNotFound             = service.ErrNotFound
	ErrWorkflowNotInProject = service.ErrWorkflowNotInProject
	ErrInvalidProject       = service.ErrInvalidProject
	ErrInvalidCell          = service.ErrInvalidCell
	ErrRecipeNotFound       = service.ErrRecipeNotFound
	ErrEngineUnavailable    = service.ErrEngineUnavailable
	ErrOutcomePending       = service.ErrOutcomePending
	ErrJobRunStoryMismatch  = service.ErrJobRunStoryMismatch
)

type Service interface {
	ListWorkflows(ctx context.Context, req ListWorkflowsRequest) ([]WorkflowSummary, error)
	GetWorkflow(ctx context.Context, req GetWorkflowRequest) (*WorkflowDetail, error)
	GetWorkflowOutcome(ctx context.Context, req GetWorkflowOutcomeRequest) (*WorkflowOutcome, error)
	GetJobRunStory(ctx context.Context, req GetJobRunStoryRequest) (*JobRunStory, error)
	RestartRecipeJob(ctx context.Context, req RestartRecipeJobRequest) (*RestartRecipeJobResponse, error)
	GetArtifactByOrdinal(ctx context.Context, req GetArtifactByOrdinalRequest) (*ArtifactData, error)
	GetWorkflowArtifact(ctx context.Context, req GetWorkflowArtifactRequest) (*ArtifactData, error)
	StartWorkflow(ctx context.Context, req StartWorkflowRequest) (*WorkflowSummary, error)
}

type ServiceConfig struct {
	Engine             swf.SWFEngine
	Strata             *client.Client
	Cells              workflowapi.CellService
	Projects           workflowapi.ProjectService
	Recipes            RecipeProvider
	RootSourceResolver RecipeSourceResolver
	CELOptionsProvider template.CELOptionsProvider
}

func New(config ServiceConfig) (Service, error) {
	return service.New(service.Config{
		Engine:             config.Engine,
		Strata:             config.Strata,
		Cells:              config.Cells,
		Projects:           config.Projects,
		Recipes:            config.Recipes,
		RootSourceResolver: config.RootSourceResolver,
		CELOptionsProvider: config.CELOptionsProvider,
	})
}

func IsNotFound(err error) bool {
	return errors.Is(err, ErrNotFound)
}
