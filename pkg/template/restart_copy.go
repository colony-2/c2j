package template

import (
	"slices"

	"github.com/colony-2/c2j/pkg/artifacts"

	"github.com/colony-2/c2j/pkg/jobcontext"
	"github.com/colony-2/c2j/pkg/recipe"
)

func cloneRestartJobs(v jobcontext.StartedJobsContext) jobcontext.StartedJobsContext {
	v.JobIDs = slices.Clone(v.JobIDs)
	v.GoalIDs = slices.Clone(v.GoalIDs)
	v.Items = slices.Clone(v.Items)
	return v
}

func cloneRestartFailure(v *recipe.RuntimeFailure) *recipe.RuntimeFailure {
	out := v.Clone()
	// RuntimeFailure.Clone handles typed metadata; use the recipe payload copier
	// for attrs too, including typed maps and lists.
	for src, dst := v, out; src != nil; src, dst = src.Cause, dst.Cause {
		dst.Attrs = cloneTemplateVars(src.Attrs)
		dst.Artifacts = cloneRestartArtifacts(src.Artifacts)
	}
	return out
}

func cloneRestartSteps(in map[string]StepOutput) map[string]StepOutput {
	if in == nil {
		return nil
	}
	out := make(map[string]StepOutput, len(in))
	for k, v := range in {
		v.Outputs = cloneTemplateVars(v.Outputs)
		v.Artifacts = cloneRestartArtifacts(v.Artifacts)
		v.Jobs = cloneRestartJobs(v.Jobs)
		v.Runs = slices.Clone(v.Runs)
		for i := range v.Runs {
			r := &v.Runs[i]
			r.Outputs = cloneTemplateVars(r.Outputs)
			r.Artifacts = cloneRestartArtifacts(r.Artifacts)
			r.Jobs = cloneRestartJobs(r.Jobs)
		}
		out[k] = v
	}
	return out
}

func cloneRestartData(v templateData) templateData {
	v.ContainerInputs = cloneTemplateVars(v.ContainerInputs)
	v.Vars = cloneTemplateVars(v.Vars)
	v.Outputs = cloneTemplateVars(v.Outputs)
	v.Locals = cloneTemplateVars(v.Locals)
	v.Sequence = cloneRestartSteps(v.Sequence)
	v.States = cloneRestartSteps(v.States)
	v.Transition.Payload = cloneTemplateVars(v.Transition.Payload)
	v.Transition.Failure = cloneRestartFailure(v.Transition.Failure)
	v.Failure = cloneRestartFailure(v.Failure)
	if v.Context.Workspace != nil {
		w := *v.Context.Workspace
		v.Context.Workspace = &w
	}
	if v.Context.CellResolution != nil {
		c := *v.Context.CellResolution
		v.Context.CellResolution = &c
	}
	v.Context.Artifacts = cloneRestartArtifacts(v.Context.Artifacts)
	v.Context.InlineStack = slices.Clone(v.Context.InlineStack)
	return v
}

func cloneRestartArtifact(v artifacts.Ref) artifacts.Ref {
	if v.Stored != nil {
		p := *v.Stored
		v.Stored = &p
	}
	if v.External != nil {
		p := *v.External
		v.External = &p
	}
	v.Value = slices.Clone(v.Value)
	return v
}
func cloneRestartArtifacts(in map[string]artifacts.Ref) map[string]artifacts.Ref {
	if in == nil {
		return nil
	}
	out := make(map[string]artifacts.Ref, len(in))
	for k, v := range in {
		out[k] = cloneRestartArtifact(v)
	}
	return out
}
