package template

import (
	"encoding/json"
	"time"
)

// InputSnapshot preserves the template context for manifest defaults discovered
// only inside a live task. It contains unresolved references, never fetched values.
func (rc *ResolutionContext) InputSnapshot() (json.RawMessage, error) {
	data := rc.TemplateData
	// Compiler-local identifiers and timestamps are not durable input identity.
	data.Scope = ScopeMetadata{}
	strip := func(src map[string]StepOutput) map[string]StepOutput {
		dst := make(map[string]StepOutput, len(src))
		for k, step := range src {
			step.Runs = append([]RunOutput(nil), step.Runs...)
			for i := range step.Runs {
				step.Runs[i].RunID = ""
				step.Runs[i].Timestamp = time.Time{}
			}
			dst[k] = step
		}
		return dst
	}
	data.Sequence = strip(data.Sequence)
	data.States = strip(data.States)
	return json.Marshal(data)
}

func ResolveDeferredInputs(snapshot json.RawMessage, input map[string]any) (map[string]any, error) {
	rc := &ResolutionContext{Options: DefaultResolutionOptions()}
	if err := json.Unmarshal(snapshot, &rc.TemplateData); err != nil {
		return nil, err
	}
	rc.TemplateData.Scope = ScopeMetadata{ExecutionID: generateExecutionID(), Timestamp: time.Now()}
	if _, err := rc.initializeCEL(); err != nil {
		return nil, err
	}
	return rc.ResolveInputs(input)
}
