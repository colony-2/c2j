package template

import (
	"github.com/colony-2/c2j/pkg/jobcontext"
	"github.com/google/cel-go/common/types"
	"github.com/google/cel-go/common/types/traits"
)

func (rc *ResolutionContext) celSequenceValue() interface{} {
	if !rc.Options.ClampSliceIndex {
		return rc.TemplateData.Sequence
	}
	return clampStepOutputs(rc.TemplateData.Sequence, rc.CELEnv.CELTypeAdapter())
}

func (rc *ResolutionContext) celStatesValue() interface{} {
	if !rc.Options.ClampSliceIndex {
		return rc.TemplateData.States
	}
	return clampStepOutputs(rc.TemplateData.States, rc.CELEnv.CELTypeAdapter())
}

func clampStepOutputs(stepOutputs map[string]StepOutput, adapter types.Adapter) map[string]interface{} {
	out := make(map[string]interface{}, len(stepOutputs))
	for key, step := range stepOutputs {
		artifactRefs := step.Artifacts
		outputs := step.Outputs
		if outputs == nil {
			outputs = map[string]interface{}{}
		}
		// In validation mode we want missing keys to yield null instead of failing.
		outputsVal := newPermissiveMap(outputs, adapter)
		artifactsVal := newPermissiveArtifactMap(artifactRefs, adapter)
		out[key] = map[string]interface{}{
			"outputs":   outputsVal,
			"artifacts": artifactsVal,
			"jobs":      jobsContextValue(step.Jobs),
			"runs":      clampRuns(step.Runs, adapter),
		}
	}
	return out
}

func clampRuns(runs []RunOutput, adapter types.Adapter) traits.Lister {
	if len(runs) == 0 {
		return types.NewDynamicList(adapter, []interface{}{})
	}
	runMaps := make([]interface{}, 0, len(runs))
	for _, run := range runs {
		artifactRefs := run.Artifacts
		outputs := run.Outputs
		if outputs == nil {
			outputs = map[string]interface{}{}
		}
		runMaps = append(runMaps, map[string]interface{}{
			"outputs":   newPermissiveMap(outputs, adapter),
			"artifacts": newPermissiveArtifactMap(artifactRefs, adapter),
			"jobs":      jobsContextValue(run.Jobs),
			"run_id":    run.RunID,
			"timestamp": run.Timestamp,
		})
	}
	list := newClampedList(types.NewDynamicList(adapter, runMaps))
	if lister, ok := list.(traits.Lister); ok {
		return lister
	}
	return types.NewDynamicList(adapter, runMaps)
}

func jobsContextValue(jobs jobcontext.StartedJobsContext) map[string]interface{} {
	jobIDs := make([]interface{}, 0, len(jobs.JobIDs))
	for _, jobID := range jobs.JobIDs {
		jobIDs = append(jobIDs, jobID)
	}

	items := make([]interface{}, 0, len(jobs.Items))
	for _, item := range jobs.Items {
		items = append(items, map[string]interface{}{
			"tenant_id":              item.TenantID,
			"job_id":                 item.JobID,
			"recipe":                 item.RecipeName,
			"status":                 item.Status,
			"parent_invocation_hash": item.ParentInvocationHash,
		})
	}

	return map[string]interface{}{
		"job_ids": jobIDs,
		"items":   items,
	}
}
