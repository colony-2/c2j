package story

import (
	"encoding/json"
	"errors"
	"strings"

	"github.com/colony-2/c2j/pkg/story/internal/model"
	coretasks "github.com/colony-2/c2j/pkg/task"
	"github.com/colony-2/swf-go/pkg/swf"
)

func splitInvocationNodePath(nodePath string) []string {
	nodePath = strings.TrimSpace(nodePath)
	if nodePath == "" {
		return make([]string, 0)
	}
	parts := strings.Split(nodePath, "/")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		out = append(out, p)
	}
	return out
}

func setStoryNodePath(n *model.JobRunStoryNode, invocationNodePath string, extra ...string) {
	if n == nil {
		return
	}
	path := splitInvocationNodePath(invocationNodePath)
	for _, seg := range extra {
		seg = strings.TrimSpace(seg)
		if seg == "" {
			continue
		}
		path = append(path, seg)
	}
	n.Path = path
}

func statusFromErr(err error, fallback model.JobRunStoryNodeStatus) model.JobRunStoryNodeStatus {
	if err == nil {
		return fallback
	}
	if isReplayCacheMissErr(err) {
		return model.JobRunStoryNodeStatusRunning
	}
	return model.JobRunStoryNodeStatusFailed
}

func isReplayCacheMissErr(err error) bool {
	if err == nil {
		return false
	}
	var miss swf.ReplayCacheMissError
	if errors.As(err, &miss) {
		return true
	}
	return strings.Contains(strings.ToLower(err.Error()), "replay cache miss:")
}

func deriveContainerStatus(children []*model.JobRunStoryNode) model.JobRunStoryNodeStatus {
	if len(children) == 0 {
		return model.JobRunStoryNodeStatusUnknown
	}
	last := children[len(children)-1]
	if last != nil && last.Status == model.JobRunStoryNodeStatusRunning {
		return model.JobRunStoryNodeStatusRunning
	}
	for _, ch := range children {
		if ch != nil && ch.Status == model.JobRunStoryNodeStatusFailed {
			return model.JobRunStoryNodeStatusFailed
		}
	}
	return model.JobRunStoryNodeStatusSucceeded
}

func stepIDFromTaskType(opID, taskType string) string {
	// Expected: "<opId>:<stepId>"
	taskType = strings.TrimSpace(taskType)
	if taskType == "" {
		return ""
	}
	parts := strings.SplitN(taskType, ":", 2)
	if len(parts) != 2 {
		return taskType
	}
	if strings.TrimSpace(parts[0]) != strings.TrimSpace(opID) {
		return parts[1]
	}
	return parts[1]
}

func unwrapTaskEnvelopePayload(raw []byte) (any, bool) {
	var env coretasks.OutputEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, false
	}
	if env.Version != coretasks.OutputEnvelopeVersion || strings.TrimSpace(string(env.Kind)) == "" {
		return nil, false
	}
	if len(env.Payload) == 0 {
		return nil, true
	}
	var payload any
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		return nil, false
	}
	return payload, true
}

func outputKindFromRaw(raw []byte) (coretasks.OutputKind, bool) {
	var env coretasks.OutputEnvelope
	if json.Unmarshal(raw, &env) != nil {
		return "", false
	}
	if env.Version != coretasks.OutputEnvelopeVersion {
		return "", false
	}
	return env.Kind, true
}

func findFirstChildStep(children []*model.JobRunStoryNode) *model.JobRunStoryNode {
	for _, ch := range children {
		if ch != nil && ch.Kind == model.JobRunStoryNodeKindOpStep {
			return ch
		}
	}
	return nil
}

func findLastChildStep(children []*model.JobRunStoryNode) *model.JobRunStoryNode {
	for i := len(children) - 1; i >= 0; i-- {
		ch := children[i]
		if ch != nil && ch.Kind == model.JobRunStoryNodeKindOpStep {
			return ch
		}
	}
	return nil
}
