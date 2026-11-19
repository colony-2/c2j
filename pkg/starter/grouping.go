package starter

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/colony-2/c2j/pkg/grouping"
	"github.com/colony-2/c2j/pkg/jobref"
	"github.com/colony-2/c2j/pkg/recipe"
	"github.com/colony-2/c2j/pkg/workflowctl"
	"github.com/colony-2/jobdb/pkg/jobdb"
)

type GroupedSubmitter interface {
	StartOrAppendJobRef(context.Context, jobdb.StartOrAppendJob) (jobref.Ref, error)
}

func StartOrAppendRecipeJob(ctx context.Context, start workflowctl.StartJob, spec grouping.Spec, engine any, recipes ...recipe.Recipe) (jobref.Ref, error) {
	originalStart := start
	p, err := spec.Prepare()
	if err != nil {
		return jobref.Ref{}, err
	}
	submitter, ok := engine.(GroupedSubmitter)
	if !ok {
		return jobref.Ref{}, fmt.Errorf("lease-authorized grouped submission is unavailable")
	}
	// Backend chooses identities and sequencing. Shared receivers have no
	// exclusive parent, including when this caller creates the generation.
	start.JobID, start.GoalID, start.Parent, start.Dependencies = "", "", nil, nil
	job, err := PrepareRecipeJob(ctx, start, StartRecipeJobOptions{}, recipes...)
	if err != nil {
		return jobref.Ref{}, err
	}
	raw, err := json.Marshal(p.Contribution)
	if err != nil {
		return jobref.Ref{}, err
	}
	finish, err := beginTestSubmission(ctx, originalStart, "append")
	if err != nil {
		return jobref.Ref{}, err
	}
	ref, err := submitter.StartOrAppendJobRef(ctx, jobdb.StartOrAppendJob{
		Job: job, AppendKey: p.Key, MaxInputs: p.MaxItems, Append: raw, AppendArtifacts: p.Native,
	})
	return ref, finish(ref, err)
}
