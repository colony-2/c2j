package template

import "encoding/json"

// RestartContextSnapshot returns detached JSON-shaped values, without resolving
// expressions or dereferencing artifacts, objects, configuration, or secrets.
// Job has the shape accepted by ContextPatch.Job; Visible uses template names.
func (rc *ResolutionContext) RestartContextSnapshot() (job, visible map[string]any, err error) {
	exec := rc.TaskExecutionContext()
	values := map[string]any{
		"inputs":     rc.TemplateData.ContainerInputs,
		"sequence":   rc.TemplateData.Sequence,
		"states":     rc.TemplateData.States,
		"vars":       rc.TemplateData.Vars,
		"outputs":    rc.TemplateData.Outputs,
		"transition": rc.TemplateData.Transition.AsMap(),
		"failure":    rc.TemplateData.Failure,
		"scope":      rc.TemplateData.Scope,
		"context":    exec,
		"item":       rc.localValue("item"),
		"index":      rc.localValue("index"),
	}
	convert := func(v any) (map[string]any, error) {
		data, err := json.Marshal(v)
		if err != nil {
			return nil, err
		}
		var out map[string]any
		err = json.Unmarshal(data, &out)
		return out, err
	}
	job, err = convert(exec.JobContext())
	if err != nil {
		return nil, nil, err
	}
	visible, err = convert(values)
	return
}
