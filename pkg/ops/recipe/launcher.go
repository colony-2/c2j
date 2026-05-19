package recipe

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"strings"

	recipeartifacts "github.com/colony-2/c2j/pkg/artifacts"
	"github.com/colony-2/c2j/pkg/contextual"
	"github.com/colony-2/c2j/pkg/workflowctl"
	"github.com/colony-2/swf-go/pkg/swf"
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

func recipeToStart(tenantId string, recipe SingleRecipe, recipeSourceRepo string, recipeSourceRef string, gitRef string, jobID string) (workflowctl.StartJob, error) {
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
		GitRef: gitRef,
	}, nil
}

// func(deps OpDependencies, ctx context.Context, in In)
// Execute runs the activity with provided configuration and inputs
func startJobs(ctx context.Context, parentJobKey swf.JobKey, invocation contextual.Invocation, ctl workflowctl.WorkflowControl, recipeSourceRepo string, recipeSourceRef string, recipes []SingleRecipe, gitRef string) ([]swf.JobKey, error) {
	if len(recipes) == 0 {
		return nil, fmt.Errorf("no jobs to start")
	}

	jobs := make([]workflowctl.StartJob, len(recipes))
	for i, recipe := range recipes {
		job, err := recipeToStart(parentJobKey.TenantId, recipe, recipeSourceRepo, recipeSourceRef, gitRef, deterministicChildJobID(parentJobKey.JobId, invocation, i))
		if err != nil {
			return nil, err
		}
		jobs[i] = job
	}

	if len(jobs) == 1 {
		key, err := ctl.StartJob(ctx, jobs[0])
		if err != nil {
			return nil, err
		}
		return []swf.JobKey{key}, nil
	}

	keys := make([]swf.JobKey, len(jobs))
	for i, job := range jobs {
		key, err := ctl.StartJob(ctx, job)
		if err != nil {
			return nil, err
		}
		keys[i] = key
	}
	return keys, nil
}
