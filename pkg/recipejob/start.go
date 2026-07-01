package recipejob

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	recipeartifacts "github.com/colony-2/c2j/pkg/artifacts"
	"github.com/colony-2/c2j/pkg/contextual"
	"github.com/colony-2/c2j/pkg/jobcontext"
	"github.com/colony-2/c2j/pkg/recipe"
	"github.com/colony-2/c2j/pkg/starter"
	"github.com/colony-2/c2j/pkg/worker/compiler"
	"github.com/colony-2/c2j/pkg/workflowctl"
	"github.com/colony-2/jobdb/pkg/jobdb"
)

type BuildStartJobRequest struct {
	TenantID     string
	JobID        string
	Target       ResolvedTarget
	Recipe       string
	Inputs       map[string]interface{}
	Artifacts    []jobdb.Artifact
	ArtifactRefs []recipeartifacts.Ref
	Parent       *jobcontext.Parent
	SubmittedAt  *time.Time
}

type Submitter interface {
	SubmitJob(ctx context.Context, job jobdb.SubmitJob) (jobdb.JobKey, error)
}

func BuildStartJob(req BuildStartJobRequest) (workflowctl.StartJob, error) {
	tenantID := strings.TrimSpace(req.TenantID)
	if tenantID == "" {
		return workflowctl.StartJob{}, fmt.Errorf("tenant ID is required")
	}

	repositorySource := strings.TrimSpace(req.Target.RepositorySource)
	if repositorySource == "" {
		return workflowctl.StartJob{}, fmt.Errorf("target repository source is required")
	}
	normalizedRepositorySource, err := compiler.NormalizeGitRepositorySource(repositorySource)
	if err != nil {
		return workflowctl.StartJob{}, err
	}
	repositorySource = normalizedRepositorySource

	defaultRef := strings.TrimSpace(req.Target.DefaultRef)
	if defaultRef == "" {
		defaultRef = compiler.DefaultRecipeRef
	}

	cellName := strings.TrimSpace(req.Target.CellName)
	if cellName == "" {
		cellName = compiler.RepositoryNameFromSource(repositorySource)
	}

	recipeName := strings.TrimSpace(req.Recipe)
	if recipeName == "" {
		recipeName = compiler.DefaultRecipeName
	}
	if err := compiler.ValidateRecipeSelector(recipeName); err != nil {
		return workflowctl.StartJob{}, err
	}

	submittedAt := time.Now().UTC()
	if req.SubmittedAt != nil {
		submittedAt = req.SubmittedAt.UTC()
	}

	return workflowctl.StartJob{
		TenantId:     tenantID,
		JobID:        strings.TrimSpace(req.JobID),
		RecipeName:   recipeName,
		Inputs:       req.Inputs,
		Artifacts:    append([]jobdb.Artifact(nil), req.Artifacts...),
		ArtifactRefs: append([]recipeartifacts.Ref(nil), req.ArtifactRefs...),
		Parent:       cloneParent(req.Parent),
		JobContext: contextual.JobContext{
			Workflow: contextual.WorkflowContext{
				CellName:  cellName,
				ProjectId: tenantID,
			},
			GitBase: contextual.GitBaseContext{
				BaseRepo: repositorySource,
				BaseRef:  defaultRef,
			},
			RecipeSource: contextual.RecipeSourceContext{
				Repo: repositorySource,
				Ref:  defaultRef,
			},
		},
		GitRef:      defaultRef,
		SubmittedAt: &submittedAt,
		InputHash:   InputHash(req.Inputs),
	}, nil
}

func cloneParent(parent *jobcontext.Parent) *jobcontext.Parent {
	if parent == nil {
		return nil
	}
	out := *parent
	return &out
}

func SubmitRecipeJob(ctx context.Context, req BuildStartJobRequest, submitter Submitter, recipes ...recipe.Recipe) (jobdb.JobKey, error) {
	start, err := BuildStartJob(req)
	if err != nil {
		return jobdb.JobKey{}, err
	}
	return starter.StartRecipeJob(ctx, start, submitter, recipes...)
}

func InputHash(inputs map[string]interface{}) string {
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
