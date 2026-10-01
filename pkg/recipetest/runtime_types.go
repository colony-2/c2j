package recipetest

import (
	"fmt"
	"path/filepath"
	"strings"
)

// RuntimeCase opts a case into real JobDB and worker execution. Legacy cases
// (including integration_case) retain their existing executor when omitted.
type RuntimeCase struct {
	CommandSandbox string                 `json:"command_sandbox,omitempty"`
	ExpectError    string                 `json:"expect_error,omitempty"`
	Cell           string                 `json:"cell,omitempty"`
	Cells          map[string]CellFixture `json:"cells,omitempty"`
	Responses      []InputFixture         `json:"responses,omitempty"`
}

type CellFixture struct {
	Files       map[string]string `json:"files,omitempty"`
	FileSources map[string]string `json:"file_sources,omitempty"`
}

type InputFixture struct {
	NodePath    string            `json:"node_path"`
	Cell        string            `json:"cell,omitempty"`
	Fields      map[string]any    `json:"fields,omitempty"`
	Response    any               `json:"response,omitempty"`
	Attachments map[string]string `json:"attachments,omitempty"`
}

type FixtureEffects struct {
	Worktree      map[string]string        `json:"worktree,omitempty"`
	ArtifactFiles map[string]string        `json:"artifact_files,omitempty"`
	Objects       map[string]ObjectFixture `json:"objects,omitempty"`
	Children      []ChildFixture           `json:"children,omitempty"`
}

type ObjectFixture struct {
	Type     string            `json:"type"`
	Metadata map[string]any    `json:"metadata,omitempty"`
	Files    map[string]string `json:"files,omitempty"`
}

type ChildFixture struct {
	Recipe string         `json:"recipe"`
	Cell   string         `json:"cell"`
	Inputs map[string]any `json:"inputs,omitempty"`
}

type RepositoryReport struct {
	Head         string            `json:"head"`
	NewCommits   int               `json:"new_commits"`
	Clean        bool              `json:"clean"`
	ChangedFiles []string          `json:"changed_files"`
	Files        map[string]string `json:"files"`
}

type RuntimeReport struct {
	Repositories map[string]RepositoryReport `json:"repositories,omitempty"`
	Calls        []OpCall                    `json:"calls"`
	Reviews      []ReviewCall                `json:"reviews,omitempty"`
}

type OpCall struct {
	Artifacts map[string]string `json:"artifacts,omitempty"`
	JobID     string            `json:"job_id"`
	Cell      string            `json:"cell"`
	NodePath  string            `json:"node_path"`
	Op        string            `json:"op"`
	Inputs    map[string]any    `json:"inputs"`
}

type ReviewCall struct {
	NodePath string `json:"node_path"`
	Form     any    `json:"form"`
}

func fixturePath(root, name string) (string, error) {
	if name == "" || filepath.IsAbs(name) || filepath.Clean(name) == ".." || strings.HasPrefix(filepath.Clean(name), ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("fixture output path must remain within its directory: %q", name)
	}
	return filepath.Join(root, name), nil
}

func validateRuntimeCase(opts HarnessOptions, c Case) []Issue {
	var issues []Issue
	add := func(err error) {
		if err != nil {
			issues = append(issues, Issue{Code: "invalid_runtime_fixture", Message: err.Error()})
		}
	}
	if c.Runtime == nil {
		for _, m := range c.Mocks.Ops {
			if m.Behavior.Effects != nil || m.Match.Cell != "" || m.Match.Selector != "" || m.Repeat {
				add(fmt.Errorf("fixture effects, selector/cell matchers and repeat require runtime: {}"))
			}
		}
		return issues
	}
	if c.ExpectError != "" && c.Runtime.ExpectError != "" {
		add(fmt.Errorf("set expect_error at the case or runtime level, not both"))
	}
	if c.Runtime.CommandSandbox != "" && c.Runtime.CommandSandbox != "none" {
		add(fmt.Errorf("command_sandbox supports only none (or omit to keep the recipe sandbox)"))
	}
	if c.Runtime.Cell != "" {
		if _, ok := c.Runtime.Cells[c.Runtime.Cell]; !ok {
			add(fmt.Errorf("unknown primary fixture cell %q", c.Runtime.Cell))
		}
	}
	for cell, fixture := range c.Runtime.Cells {
		if strings.ContainsAny(cell, "/\\") || cell == "." || cell == ".." || cell == "" {
			add(fmt.Errorf("invalid fixture cell %q", cell))
		}
		for name := range fixture.Files {
			_, err := fixturePath("", name)
			add(err)
		}
		for name := range fixture.FileSources {
			_, err := fixturePath("", name)
			add(err)
		}
	}
	for _, m := range c.Mocks.Ops {
		if m.Behavior.Mode == "record_passthrough" || m.Behavior.Mode == "replay" {
			add(fmt.Errorf("runtime cases support return, fail, and passthrough mocks"))
		}
		if e := m.Behavior.Effects; e != nil {
			for name := range e.Worktree {
				_, err := fixturePath("", name)
				add(err)
			}
			for _, child := range e.Children {
				if _, ok := c.Runtime.Cells[child.Cell]; !ok {
					add(fmt.Errorf("child fixture references unknown cell %q", child.Cell))
				}
			}
		}
	}
	for _, response := range c.Runtime.Responses {
		if response.NodePath == "" {
			add(fmt.Errorf("input fixture requires node_path"))
		}
	}
	return issues
}

// Expected errors never accept a timeout or a validation error.
func applyExpectedError(result *CaseRunResult, expected string) {
	if expected == "" {
		return
	}
	if result.Status == "failed" && strings.Contains(result.FailureReason, expected) {
		result.Status = "passed"
		result.FailureCategory = ""
		result.FailureReason = ""
	} else {
		result.Status = "failed"
		result.FailureReason = fmt.Sprintf("expected error containing %q; got %s", expected, result.FailureReason)
	}
}
