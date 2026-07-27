package skillinstall

import "io/fs"

const (
	ScopeAuto    = "auto"
	ScopeProject = "project"
	ScopeUser    = "user"

	ActionInstalled = "installed"
	ActionSkipped   = "skipped"
	ActionFailed    = "failed"

	MaxSkillFileBytes int64 = 1 << 20
)

type Options struct {
	Bundle          fs.FS
	WorkingDir      string
	Scope           string
	Force           bool
	IncludeOpSkills bool
}

type SkillResult struct {
	Name   string
	Action string
	Path   string
	Digest string
	Reason string
	Err    error
}

type Summary struct {
	Results []SkillResult
}

func (s Summary) Installed() []SkillResult {
	return s.byAction(ActionInstalled)
}

func (s Summary) Skipped() []SkillResult {
	return s.byAction(ActionSkipped)
}

func (s Summary) Failed() []SkillResult {
	return s.byAction(ActionFailed)
}

func (s Summary) HasFailures() bool {
	return len(s.Failed()) > 0
}

func (s Summary) byAction(action string) []SkillResult {
	out := []SkillResult{}
	for _, result := range s.Results {
		if result.Action == action {
			out = append(out, result)
		}
	}
	return out
}
