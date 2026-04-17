package template

import (
	"encoding/json"
	"fmt"

	"github.com/colony-2/c2j/pkg/contextual"
	coretask "github.com/colony-2/c2j/pkg/task"
)

// ApplyContextPatch mutates template-visible context for this ResolutionContext.
//
// Job patches are applied to this context and all ancestors (global visibility).
// Scoped patches are applied to the currently-visible scope containers only
// (e.g. a sequence's "sequence" map, or a state machine's "states" map).
func (rc *ResolutionContext) ApplyContextPatch(p coretask.ContextPatch) error {
	if rc == nil {
		return fmt.Errorf("ApplyContextPatch: nil ResolutionContext")
	}

	// 1) Job-level patch: apply to this context and ancestors so outer scopes observe changes.
	if len(p.Job) > 0 {
		job, err := applyJobMergePatch(rc.TaskExecutionContext().JobContext(), p.Job)
		if err != nil {
			return err
		}
		for cur := rc; cur != nil; cur = cur.Parent {
			applyJobContextToTaskExecutionContext(&cur.TemplateData.Context, job)
		}
	}

	// 2) Scoped patches: apply to the locally visible containers.
	for _, sp := range p.Scopes {
		if sp.ID == "" {
			return fmt.Errorf("ApplyContextPatch: scope patch id is required")
		}
		switch sp.Container {
		case "sequence":
			if err := applyStepOutputsPatch(rc.TemplateData.Sequence, sp.ID, sp.Outputs); err != nil {
				return err
			}
		case "states":
			if err := applyStepOutputsPatch(rc.TemplateData.States, sp.ID, sp.Outputs); err != nil {
				return err
			}
		default:
			return fmt.Errorf("ApplyContextPatch: unsupported container %q", sp.Container)
		}
	}

	return nil
}

func applyStepOutputsPatch(container map[string]StepOutput, id string, patch map[string]any) error {
	if len(patch) == 0 {
		return nil
	}
	step := container[id]
	if step.Outputs == nil {
		step.Outputs = map[string]any{}
	}

	merged, err := applyMapMergePatch(step.Outputs, patch)
	if err != nil {
		return err
	}
	step.Outputs = merged
	container[id] = step
	return nil
}

func applyJobMergePatch(job contextual.JobContext, patch map[string]any) (contextual.JobContext, error) {
	// Convert current job context to a JSON-shaped map.
	baseBytes, err := json.Marshal(job)
	if err != nil {
		return contextual.JobContext{}, err
	}
	var base map[string]any
	if err := json.Unmarshal(baseBytes, &base); err != nil {
		return contextual.JobContext{}, err
	}

	merged, err := applyMapMergePatch(base, patch)
	if err != nil {
		return contextual.JobContext{}, err
	}

	mergedBytes, err := json.Marshal(merged)
	if err != nil {
		return contextual.JobContext{}, err
	}
	var out contextual.JobContext
	if err := json.Unmarshal(mergedBytes, &out); err != nil {
		return contextual.JobContext{}, err
	}
	return out, nil
}

func applyJobContextToTaskExecutionContext(exec *contextual.TaskExecutionContext, job contextual.JobContext) {
	if exec == nil {
		return
	}

	// Update embedded job fields.
	exec.Actor = job.Actor
	exec.Ticket = job.Ticket
	exec.Environment = job.Environment
	exec.Workflow = job.Workflow

	// Update immutable git base values, but preserve commit-level fields (persist/parent) already tracked.
	exec.GitTask.BaseRepo = job.GitBase.BaseRepo
	exec.GitTask.BaseRef = job.GitBase.BaseRef
	exec.GitTask.ResolvedBaseHash = job.GitBase.ResolvedBaseHash
	exec.GitTask.GitAuthor = job.GitBase.GitAuthor
}

func applyMapMergePatch(dst map[string]any, patch map[string]any) (map[string]any, error) {
	if dst == nil {
		dst = map[string]any{}
	}
	for k, v := range patch {
		if v == nil {
			delete(dst, k)
			continue
		}
		pmap, ok := v.(map[string]any)
		if ok {
			existing, ok := dst[k].(map[string]any)
			if !ok || existing == nil {
				dst[k] = cloneAnyMap(pmap)
				continue
			}
			merged, err := applyMapMergePatch(existing, pmap)
			if err != nil {
				return nil, err
			}
			dst[k] = merged
			continue
		}
		dst[k] = v
	}
	return dst, nil
}

func cloneAnyMap(in map[string]any) map[string]any {
	if in == nil {
		return nil
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		if nested, ok := v.(map[string]any); ok {
			out[k] = cloneAnyMap(nested)
			continue
		}
		out[k] = v
	}
	return out
}
