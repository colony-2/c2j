package compiler

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/colony-2/c2j/internal/jsonexact"
	"github.com/colony-2/c2j/pkg/grouping"
	"github.com/colony-2/c2j/pkg/jobsemantics"
	"github.com/colony-2/c2j/pkg/workflowctl"
	"github.com/colony-2/jobdb/pkg/jobdb"
)

// namedStartArtifact changes only the display name, never storage identity.
type namedStartArtifact struct {
	jobdb.Artifact
	name string
}

func (a namedStartArtifact) Name() string { return a.name }

func decodeRecipeInput(data []byte, artifacts []jobdb.Artifact) (workflowctl.StartJob, []jobdb.Artifact, error) {
	var start workflowctl.StartJob
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return start, nil, err
	}
	_, hasStart := fields["start"]
	_, hasAppends := fields["appends"]
	if hasStart || hasAppends {
		if !hasStart || !hasAppends || len(fields) != 2 {
			return start, nil, fmt.Errorf("malformed grouped recipe input: requires start and appends")
		}
		var appends []map[string]any
		if err := jsonexact.Unmarshal(fields["appends"], &appends); err != nil || len(appends) == 0 {
			return start, nil, fmt.Errorf("malformed grouped recipe appends")
		}
		if string(fields["start"]) == "null" {
			return start, nil, fmt.Errorf("malformed grouped recipe start")
		}
		if err := jsonexact.Unmarshal(fields["start"], &start); err != nil {
			return start, nil, fmt.Errorf("grouped recipe start: %w", err)
		}
		group := &grouping.Context{Appends: make([]grouping.Contribution, len(appends))}
		for i, value := range appends {
			c, _, err := grouping.DecodeContribution(value, false)
			if err != nil {
				return start, nil, fmt.Errorf("grouped recipe append %d: %w", i, err)
			}
			if c.Content != nil {
				c.Content = jsonexact.Native(c.Content).(map[string]any)
			}
			group.Appends[i] = c
		}
		start.JobContext.Grouping = group
		creator := make([]jobdb.Artifact, 0, len(artifacts))
		for _, artifact := range artifacts {
			if strings.HasPrefix(artifact.Name(), "start/") {
				creator = append(creator, namedStartArtifact{Artifact: artifact, name: strings.TrimPrefix(artifact.Name(), "start/")})
			} else if !strings.HasPrefix(artifact.Name(), "appends/") {
				return start, nil, fmt.Errorf("grouped recipe artifact %q has no transport namespace", artifact.Name())
			}
		}
		artifacts = creator
	} else {
		if err := jsonexact.Unmarshal(data, &start); err != nil {
			return start, nil, err
		}
	}
	var snapshot map[string]json.RawMessage
	rawStart := data
	if hasStart {
		rawStart = fields["start"]
	}
	if err := json.Unmarshal(rawStart, &snapshot); err != nil {
		return start, nil, err
	}
	if raw, ok := snapshot["job_semantics_version"]; ok && string(raw) != "1" && string(raw) != "2" {
		return start, nil, fmt.Errorf("job_semantics_version must be integer 1 or 2")
	}
	if raw, ok := snapshot["status_history_version"]; ok && string(raw) != "1" {
		return start, nil, fmt.Errorf("status_history_version must be integer 1")
	}
	if err := jobsemantics.Validate(start.JobSemanticsVersion); err != nil {
		return start, nil, err
	}
	if start.JobContext.JobSemanticsVersion != 0 && start.JobContext.JobSemanticsVersion != start.JobSemanticsVersion {
		return start, nil, fmt.Errorf("contradictory job_semantics_version in stored context")
	}
	start.JobContext.JobSemanticsVersion = start.JobSemanticsVersion
	marker, marked := snapshot["hook_policy_version"]
	if marked && string(marker) != "1" {
		return start, nil, fmt.Errorf("hook_policy_version must be integer 1")
	}
	var storedContext struct {
		Permissions map[string]json.RawMessage `json:"inherited_permissions"`
	}
	if raw, present := snapshot["context"]; present {
		if err := json.Unmarshal(raw, &storedContext); err != nil {
			return start, nil, err
		}
	}
	if raw, present := storedContext.Permissions["child_hook_lineage"]; present {
		state := start.JobContext.InheritedPermissions
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(raw, &fields); err != nil || len(fields) != 1 || fields["repository"] == nil || state.ChildHookLineage == nil {
			return start, nil, fmt.Errorf("malformed child_hook_lineage")
		}
		if !marked || len(state.ChildPolicy) > 0 || state.Version != 1 || state.Grant == nil || state.Limit == nil || state.Inputs == nil || !state.Inputs.Foreign {
			return start, nil, fmt.Errorf("child_hook_lineage requires execution hook policy and an inherited ceiling, without child_policy")
		}
		if err := state.ChildHookLineage.Validate(); err != nil {
			return start, nil, err
		}
	} else if state := start.JobContext.InheritedPermissions; marked && state != nil && len(state.ChildPolicy) > 0 {
		return start, nil, fmt.Errorf("child_policy is incompatible with execution hook policy")
	} else if state != nil && state.ChildHookLineage != nil {
		return start, nil, fmt.Errorf("malformed child_hook_lineage")
	}
	if !marked && start.HookPolicyVersion != 0 {
		return start, nil, fmt.Errorf("malformed hook_policy_version")
	}
	if start.Inputs != nil {
		start.Inputs = jsonexact.Native(start.Inputs).(map[string]any)
	}
	return start, artifacts, nil
}
