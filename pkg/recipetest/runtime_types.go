package recipetest

import (
	"bytes"
	"encoding/json"
	"fmt"
	inputop "github.com/colony-2/c2j/pkg/input"
	"path/filepath"
	"strings"
)

// RuntimeCase opts a case into real JobDB and worker execution. Legacy cases
// (including integration_case) retain their existing executor when omitted.
type RuntimeCase struct {
	// Backend defaults to the compatible in-memory runtime. sqlite creates a
	// disposable on-disk database owned exclusively by this case.
	Controls *RuntimeControls   `json:"controls,omitempty"`
	Backend  string             `json:"backend,omitempty"`
	Observe  *ObservationConfig `json:"observe,omitempty"`
	// Synthetic, case-local tenant values; never use production credentials.
	Secrets      map[string]string      `json:"secrets,omitempty"`
	Config       map[string]any         `json:"config,omitempty"`
	ObserveFiles []string               `json:"observe_files,omitempty"`
	ExpectError  string                 `json:"expect_error,omitempty"`
	Cell         string                 `json:"cell,omitempty"`
	Cells        map[string]CellFixture `json:"cells,omitempty"`
	Responses    []InputFixture         `json:"responses,omitempty"`
}

type CellFixture struct {
	Files       map[string]string `json:"files,omitempty"`
	FileSources map[string]string `json:"file_sources,omitempty"`
}

type AfterCall struct {
	NodePath   string `json:"node_path"`
	Cell       string `json:"cell,omitempty"`
	Occurrence int    `json:"occurrence"`
}

type OrderingEvent struct {
	Position      int    `json:"position"`
	Kind          string `json:"kind"`
	JobID         string `json:"job_id"`
	ParentJobID   string `json:"parent_job_id,omitempty"`
	NodePath      string `json:"node_path,omitempty"`
	Cell          string `json:"cell,omitempty"`
	ResponseIndex *int   `json:"response_index,omitempty"`
	Status        string `json:"status,omitempty"`
}

type InputFixture struct {
	AfterCall *AfterCall `json:"after_call,omitempty"`
	// After gates this native submission on a prepared control event. The pending
	// form is captured on one controller pass before submission on a later pass.
	After       string            `json:"after,omitempty"`
	NodePath    string            `json:"node_path"`
	Cell        string            `json:"cell,omitempty"`
	Fields      map[string]any    `json:"fields,omitempty"`
	Response    any               `json:"response,omitempty"`
	Attachments map[string]string `json:"attachments,omitempty"`
}

type FixtureEffects struct {
	WorktreeExpr  map[string]string        `json:"worktree_expr,omitempty"`
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
	Bind   string         `json:"bind,omitempty"`
	Await  string         `json:"await,omitempty"`
	Recipe string         `json:"recipe"`
	Cell   string         `json:"cell"`
	Inputs map[string]any `json:"inputs,omitempty"`
}

type RepositoryReport struct {
	Repo         string            `json:"repo"`
	Head         string            `json:"head"`
	NewCommits   int               `json:"new_commits"`
	Clean        bool              `json:"clean"`
	ChangedFiles []string          `json:"changed_files"`
	Files        map[string]string `json:"files"`
}

type RuntimeReport struct {
	Events                []OrderingEvent             `json:"events,omitempty"`
	Controls              *ControlReport              `json:"controls,omitempty"`
	ObservationsVersion   int                         `json:"observations_version,omitempty"`
	Observations          []map[string]any            `json:"observations,omitempty"`
	ObservationCollection *ObservationCollection      `json:"observation_collection,omitempty"`
	ObservationArtifacts  []ObservationArtifact       `json:"observation_artifacts,omitempty"`
	Repositories          map[string]RepositoryReport `json:"repositories,omitempty"`
	Calls                 []OpCall                    `json:"calls"`
	Reviews               []ReviewCall                `json:"reviews,omitempty"`
}

type OpCall struct {
	Worktree  map[string]string `json:"worktree"`
	Outputs   map[string]any    `json:"outputs,omitempty"`
	Artifacts map[string]string `json:"artifacts,omitempty"`
	JobID     string            `json:"job_id"`
	Cell      string            `json:"cell"`
	NodePath  string            `json:"node_path"`
	Op        string            `json:"op"`
	Inputs    map[string]any    `json:"inputs"`
}

type ReviewCall struct {
	JobID     string           `json:"job_id,omitempty"`
	RequestID string           `json:"request_id,omitempty"`
	Receipt   *inputop.Receipt `json:"receipt,omitempty"`
	NodePath  string           `json:"node_path"`
	Form      any              `json:"form"`
}

func fixturePath(root, name string) (string, error) {
	if name == "" || filepath.IsAbs(name) || filepath.Clean(name) == ".." || strings.HasPrefix(filepath.Clean(name), ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("fixture output path must remain within its directory: %q", name)
	}
	return filepath.Join(root, name), nil
}

func validateRuntimeCase(opts HarnessOptions, c Case) []Issue {
	issues := validateResponses(c)
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
	if c.Runtime.Backend != "" && c.Runtime.Backend != "memory" && c.Runtime.Backend != "sqlite" {
		add(fmt.Errorf("unknown runtime backend %q; use memory or sqlite", c.Runtime.Backend))
	}
	add(validateControls(c.Runtime))
	if c.Runtime.Controls != nil && c.ExpectError != "" {
		for _, a := range c.Runtime.Controls.Actions {
			if a.Do == "cancel_job" {
				add(fmt.Errorf("cancel_job controls require no case expect_error"))
			}
		}
	}
	add(validateObservations(c.Runtime.Observe))
	for _, name := range c.Runtime.ObserveFiles {
		_, err := fixturePath("", name)
		add(err)
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
				if child.Await != "" && child.Await != "terminal" {
					add(fmt.Errorf("child await must be terminal when supplied"))
				}
				if _, ok := c.Runtime.Cells[child.Cell]; !ok {
					add(fmt.Errorf("child fixture references unknown cell %q", child.Cell))
				}
			}
		}
	}
	for _, response := range c.Runtime.Responses {
		if gate := response.AfterCall; gate != nil {
			if strings.TrimSpace(gate.NodePath) == "" || gate.Occurrence <= 0 {
				add(fmt.Errorf("after_call requires nonempty node_path and positive occurrence"))
			}
		}
		if response.After != "" {
			id, event, ok := strings.Cut(response.After, ".")
			exists := false
			if c.Runtime.Controls != nil {
				for _, b := range c.Runtime.Controls.Barriers {
					if b.ID == id {
						exists = true
					}
				}
			}
			if !ok || !exists || (event != "entered" && event != "exited") {
				add(fmt.Errorf("unknown input response gate %q", response.After))
			}
		}
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

// Reject misspelled selector keys rather than silently relaxing an ordering gate.
func (g *AfterCall) UnmarshalJSON(data []byte) error {
	type plain AfterCall
	var value plain
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		return fmt.Errorf("after_call: %w", err)
	}
	*g = AfterCall(value)
	return nil
}

func (c *ChildFixture) UnmarshalJSON(data []byte) error {
	type plain ChildFixture
	var value plain
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	if raw, supplied := fields["bind"]; supplied {
		var name string
		if err := json.Unmarshal(raw, &name); err != nil || strings.TrimSpace(name) == "" {
			return fmt.Errorf("child bind must be a nonempty name")
		}
	}
	if raw, supplied := fields["await"]; supplied {
		var mode string
		if err := json.Unmarshal(raw, &mode); err != nil || mode != "terminal" {
			return fmt.Errorf("child await must be terminal when supplied")
		}
	}
	*c = ChildFixture(value)
	return nil
}

// A supplied null gate is not a selector; omission is the asynchronous default.
func (r *InputFixture) UnmarshalJSON(data []byte) error {
	type plain InputFixture
	var value plain
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	if raw, supplied := fields["after_call"]; supplied && bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return fmt.Errorf("after_call requires a selector")
	}
	*r = InputFixture(value)
	return nil
}
