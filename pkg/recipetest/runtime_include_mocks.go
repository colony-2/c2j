package recipetest

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/colony-2/c2j/internal/includetest"
	"github.com/colony-2/c2j/internal/jsonexact"
	"github.com/colony-2/c2j/pkg/recipe"
	"github.com/colony-2/c2j/pkg/secrets"
	"github.com/colony-2/jobdb/pkg/jobdb"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
)

// IncludeMockResponse is consumed on one body attempt at an authored call site.
type IncludeMockResponse struct {
	Mode      string            `json:"mode"`
	Outputs   map[string]any    `json:"outputs,omitempty"`
	Artifacts map[string]string `json:"artifacts,omitempty"`
	Error     *TestError        `json:"error,omitempty"`
	Optional  bool              `json:"optional,omitempty"`
}

type IncludeCall struct {
	NodePath     string         `json:"node_path"`
	Cell         string         `json:"cell"`
	Inputs       map[string]any `json:"inputs"`
	ArtifactRefs map[string]any `json:"artifact_refs,omitempty"`
	Response     int            `json:"response"`
}

func validateIncludeMocks(c Case, rec *recipe.Recipe) []Issue {
	if len(c.Mocks.Includes) == 0 {
		return nil
	}
	field := "case.mocks.includes"
	issue := func(field, message string) Issue {
		return Issue{Code: "invalid_include_mock", Field: field, Message: message}
	}
	if c.Runtime == nil {
		return []Issue{issue(field, "include mocks require a native runtime case")}
	}
	known := includeMockSites(rec)
	var issues []Issue
	for _, path := range slices.Sorted(maps.Keys(c.Mocks.Includes)) {
		site := fmt.Sprintf("%s[%q]", field, path)
		if _, exists := known[path]; rec != nil && !exists {
			issues = append(issues, issue(site, "unknown include call site in the parent recipe"))
		}
		responses := c.Mocks.Includes[path]
		if len(responses) == 0 {
			issues = append(issues, issue(site, "at least one include response is required"))
		}
		for i, response := range responses {
			entry := fmt.Sprintf("%s[%d]", site, i)
			switch response.Mode {
			case "return":
				if response.Error != nil {
					issues = append(issues, issue(entry, "return responses cannot declare an error"))
				}
			case "fail":
				if response.Error == nil || strings.TrimSpace(response.Error.Message) == "" {
					issues = append(issues, issue(entry, "fail responses require an error message"))
				}
				if len(response.Outputs) > 0 || len(response.Artifacts) > 0 {
					issues = append(issues, issue(entry, "fail responses cannot declare outputs or artifacts"))
				}
			default:
				issues = append(issues, issue(entry, "include response mode must be return or fail"))
			}
			for _, name := range slices.Sorted(maps.Keys(response.Artifacts)) {
				if strings.TrimSpace(name) == "" {
					issues = append(issues, issue(entry, "artifact name cannot be blank"))
				}
			}
		}
	}
	return issues
}

func includeMockSites(rec *recipe.Recipe) map[string]struct{} {
	sites := map[string]struct{}{}
	if rec == nil {
		return sites
	}
	var visit func(recipe.Node)
	visit = func(node recipe.Node) {
		if meta := node.GetMetadata().Internal; meta != nil {
			if meta.Inline != nil {
				sites[meta.Inline.CallsitePath] = struct{}{}
				return
			}
			if meta.Late != nil {
				sites[meta.Late.CallsitePath] = struct{}{}
				return
			}
		}
		switch n := node.NodeImpl.(type) {
		case *recipe.NodeSequence:
			for _, child := range n.Sequence {
				visit(child)
			}
		case *recipe.NodeState:
			if n.States != nil {
				for _, child := range n.States.States {
					visit(child.Node)
				}
			}
		}
	}
	switch root := rec.RecipeImpl.(type) {
	case *recipe.RecipeSequence:
		for _, node := range root.Sequence {
			visit(node)
		}
	case *recipe.RecipeState:
		if root.States != nil {
			for _, node := range root.States.States {
				visit(node.Node)
			}
		}
	}
	return sites
}

type includeMockWorker struct{ fixture *runtimeFixture }

func (includeMockWorker) Name() string { return includetest.TaskType }

func (w includeMockWorker) Run(tc jobworkflow.TaskContext, data jobdb.TaskData) (jobdb.TaskData, error) {
	f := w.fixture
	raw, err := data.GetData()
	if err != nil {
		return nil, err
	}
	var invocation includetest.Invocation
	if err := jsonexact.Unmarshal(raw, &invocation); err != nil {
		return nil, err
	}
	if !f.matchesIncludeMock(tc.JobKey, invocation.NodePath) || tc.InvocationID == "" {
		return nil, fmt.Errorf("include mock task has no matching case invocation")
	}

	index, err := f.selectIncludeResponse(tc, invocation)
	if err != nil {
		return nil, err
	}
	responses := f.c.Mocks.Includes[invocation.NodePath]
	if index >= len(responses) {
		return nil, f.rejectIncludeMock(invocation.NodePath, fmt.Errorf("unexpected call %d after %d configured responses", index+1, len(responses)))
	}

	response := responses[index]
	if response.Mode == "fail" {
		return jobdb.NewTaskData(includetest.Result{Failure: &jobdb.AppErrorPayload{
			Message: response.Error.Message,
			Attrs:   map[string]any{"code": response.Error.Code},
		}})
	}
	var artifacts []jobdb.Artifact
	for _, name := range slices.Sorted(maps.Keys(response.Artifacts)) {
		artifacts = append(artifacts, jobdb.NewArtifactFromBytes(name, []byte(response.Artifacts[name])))
	}
	return jobdb.NewTaskData(includetest.Result{Outputs: response.Outputs}, artifacts...)
}

func (f *runtimeFixture) matchesIncludeMock(job jobdb.JobKey, path string) bool {
	_, configured := f.c.Mocks.Includes[path]
	return job.JobId == f.rootJobID && configured
}

func (f *runtimeFixture) selectIncludeResponse(tc jobworkflow.TaskContext, invocation includetest.Invocation) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if index, seen := f.includeInvocations[tc.InvocationID]; seen {
		return index, nil
	}

	index := f.includeUsed[invocation.NodePath]
	var safeCall IncludeCall
	// The compiler supplies bound values before secret materialization. Keep
	// the report detached and mask any fixture secrets in literal values.
	redactor := secrets.NewRedactor()
	defer redactor.Close()
	for _, secret := range f.c.Runtime.Secrets {
		if err := redactor.Register(secret); err != nil {
			return 0, err
		}
	}
	session := redactor.NewSession()
	defer session.Close()
	reportCtx := secrets.WithSession(tc.Context(), session)
	if err := snapshotRuntimeValue(reportCtx, struct {
		includetest.Invocation
		Response int `json:"response"`
	}{Invocation: invocation, Response: index + 1}, &safeCall); err != nil {
		return 0, err
	}
	f.includeInvocations[tc.InvocationID] = index
	f.includeUsed[invocation.NodePath]++
	f.report.IncludeCalls = append(f.report.IncludeCalls, safeCall)
	return index, nil
}

func (f *runtimeFixture) rejectIncludeMock(path string, err error) error {
	failure := fmt.Errorf("include mock %q: %w", path, err)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fixtureFailure == "" {
		f.fixtureFailure = failure.Error()
	}
	return failure
}

func (f *runtimeFixture) finishIncludeMocks(result *CaseRunResult) {
	for _, path := range slices.Sorted(maps.Keys(f.c.Mocks.Includes)) {
		responses := f.c.Mocks.Includes[path]
		for i := min(f.includeUsed[path], len(responses)); i < len(responses); i++ {
			if !responses[i].Optional {
				markFailure(result, "include_mock_unused", fmt.Sprintf("include mock %q response %d was not used", path, i+1))
			}
		}
	}
}
