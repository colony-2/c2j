package recipe

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"strings"

	recipeartifacts "github.com/colony-2/c2j/pkg/artifacts"
	"github.com/colony-2/c2j/pkg/contextual"
	"github.com/colony-2/c2j/pkg/jobcontext"
	"github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/c2j/pkg/starter"
	"github.com/colony-2/c2j/pkg/workflowctl"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/segmentio/ksuid"
)

const childJobIDNamespace = "colony2.recipe-child/v1"

func deterministicChildJobID(parentJobID string, invocation contextual.Invocation, recipeIndex int) string {
	hasher := sha256.New()
	hasher.Write([]byte(childJobIDNamespace))
	hasher.Write([]byte{0})
	hasher.Write([]byte(parentJobID))
	hasher.Write([]byte{0})
	hasher.Write([]byte(invocation.NodePath))
	hasher.Write([]byte{0})

	var intBuf [8]byte
	binary.BigEndian.PutUint64(intBuf[:], uint64(invocation.InvokeSeq))
	hasher.Write(intBuf[:])
	binary.BigEndian.PutUint64(intBuf[:], uint64(recipeIndex))
	hasher.Write(intBuf[:])

	sum := hasher.Sum(nil)
	var jobID ksuid.KSUID
	copy(jobID[:], sum[:len(jobID)])
	return jobID.String()
}

func recipeToStart(tenantId string, recipe SingleRecipe, recipeSourceRepo string, recipeSourceRef string, gitRef string, jobID string, parent jobcontext.Parent) (workflowctl.StartJob, error) {
	recipeName := strings.TrimSpace(recipe.Name)
	if recipeName == "" {
		return workflowctl.StartJob{}, fmt.Errorf("recipe name is required")
	}

	lookupRepo := strings.TrimSpace(recipeSourceRepo)
	if lookupRepo == "" {
		lookupRepo = strings.TrimSpace(recipe.Git.BaseRepo)
	}
	lookupRef := strings.TrimSpace(recipeSourceRef)
	if lookupRef == "" {
		lookupRef = strings.TrimSpace(recipe.Git.BaseRef)
	}

	var parentPtr *jobcontext.Parent
	if parent.HasJob() {
		cloned := parent
		parentPtr = &cloned
	}

	return workflowctl.StartJob{
		TenantId:     tenantId,
		JobID:        jobID,
		RecipeName:   recipeName,
		Inputs:       recipe.Inputs,
		ArtifactRefs: append([]recipeartifacts.Ref(nil), recipe.Artifacts...),
		JobContext: contextual.JobContext{
			Workflow: contextual.WorkflowContext{
				CellName: recipe.CellName,
			},
			GitBase: contextual.GitBaseContext{
				BaseRepo:         recipe.Git.BaseRepo,
				BaseRef:          recipe.Git.BaseRef,
				ResolvedBaseHash: recipe.Git.BaseHash,
				GitAuthor:        recipe.Git.Author,
			},
			RecipeSource: contextual.RecipeSourceContext{
				Repo: lookupRepo,
				Ref:  lookupRef,
			},
		},
		Parent: parentPtr,
		GitRef: gitRef,
	}, nil
}

// func(deps OpDependencies, ctx context.Context, in In)
// Execute runs the activity with provided configuration and inputs
type childJobSubmitter interface {
	SubmitJob(context.Context, jobdb.SubmitJob) (jobdb.JobKey, error)
}

func startJobs(ctx context.Context, deps ops.OpDependencies, parentJobKey jobdb.JobKey, invocation contextual.Invocation, ctl workflowctl.WorkflowControl, recipeSourceRepo string, recipeSourceRef string, recipes []SingleRecipe, gitRef string, parent jobcontext.Parent) ([]jobdb.JobKey, error) {
	if len(recipes) == 0 {
		return nil, fmt.Errorf("no jobs to start")
	}

	jobs := make([]workflowctl.StartJob, len(recipes))
	for i, recipe := range recipes {
		job, err := recipeToStart(parentJobKey.TenantId, recipe, recipeSourceRepo, recipeSourceRef, gitRef, deterministicChildJobID(parentJobKey.JobId, invocation, i), parent)
		if err != nil {
			return nil, err
		}
		jobs[i] = job
	}

	if len(jobs) == 1 {
		key, err := startRecipeChildJob(ctx, deps, ctl, jobs[0])
		if err != nil {
			return nil, err
		}
		return []jobdb.JobKey{key}, nil
	}

	keys := make([]jobdb.JobKey, len(jobs))
	for i, job := range jobs {
		key, err := startRecipeChildJob(ctx, deps, ctl, job)
		if err != nil {
			return nil, err
		}
		keys[i] = key
	}
	return keys, nil
}

func startRecipeChildJob(ctx context.Context, deps ops.OpDependencies, ctl workflowctl.WorkflowControl, start workflowctl.StartJob) (jobdb.JobKey, error) {
	if deps != nil {
		if jobTool := deps.JobTool(); jobTool != nil {
			if submitter, ok := jobTool.(childJobSubmitter); ok {
				return starter.StartRecipeJob(ctx, start, submitter)
			}
		}
	}
	return ctl.StartJob(ctx, start)
}
