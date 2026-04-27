package ops

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/colony-2/c2j/pkg/contextual"
	"github.com/colony-2/c2j/pkg/git/gitstate"
	recipeops "github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/c2j/pkg/worker/activity"
	"github.com/colony-2/c2j/pkg/workflowctl"
	"github.com/colony-2/swf-go/pkg/swf"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// mockArtifact is a simple test implementation of swf.Artifact
type mockArtifact struct {
	name string
	data []byte
	id   string
	key  *swf.ArtifactKey
}

func (m *mockArtifact) Name() string { return m.name }

func (m *mockArtifact) ID() string {
	if m.id == "" {
		return "mock-artifact-" + m.name
	}
	return m.id
}

func (m *mockArtifact) SaveToFile(ctx context.Context, path string) error {
	return os.WriteFile(path, m.data, 0644)
}

func (m *mockArtifact) Bytes(ctx context.Context) ([]byte, error) {
	return m.data, nil
}

func (m *mockArtifact) SizeBytes() int64 {
	return int64(len(m.data))
}

func (m *mockArtifact) Size() int64 {
	return int64(len(m.data))
}

func (m *mockArtifact) Sha256(ctx context.Context) (string, error) {
	return "", nil
}

func (m *mockArtifact) Cleanup() error {
	return nil
}

func (m *mockArtifact) ContentType() string {
	return "application/octet-stream"
}

func (m *mockArtifact) Open() (io.ReadCloser, error) {
	return io.NopCloser(bytes.NewReader(m.data)), nil
}

func (m *mockArtifact) WriteTo(ctx context.Context, w io.Writer) error {
	_, err := w.Write(m.data)
	return err
}

func (m *mockArtifact) ArtifactKey() (swf.ArtifactKey, error) {
	if m.key == nil {
		return swf.ArtifactKey{}, swf.ErrArtifactKeyUnavailable
	}
	return *m.key, nil
}

func (m *mockArtifact) setArtifactKey(key swf.ArtifactKey) {
	m.key = &key
}

// Test types with proper JSON tags
type TestConfig struct {
	URL     string `json:"url"`
	Timeout int    `json:"timeout"`
}

type TestInput struct {
	Data string `json:"data"`
	Size int    `json:"size"`
}

type TestOutput struct {
	Result  string `json:"result"`
	Success bool   `json:"success"`
}

// Test types missing JSON tags (should fail registration)
type BadConfig struct {
	URL     string // Missing json tag
	Timeout int    `json:"timeout"`
}

type BadInput struct {
	Data string `json:"data"`
	Size int    // Missing json tag
}

// testActivity is a mock RegisterableOp for testing
var testActivity = recipeops.NewActivityMappedOpV2[TestInput, TestOutput](
	recipeops.OpMetadata{
		Type:           "test_registry_activity",
		Description:    "A test activity for unit testing",
		Version:        "1.0.0",
		DefaultTimeout: 30 * time.Second,
	},
	func(inv recipeops.OpDependencies, ctx context.Context, input TestInput) (TestOutput, error) {
		return runTestActivity(inv, ctx, input)
	},
)

func runTestActivity(_ recipeops.OpDependencies, ctx context.Context, input TestInput) (TestOutput, error) {
	return TestOutput{
		Result:  input.Data + " processed",
		Success: true,
	}, nil
}

type jt struct {
	JobKey swf.JobKey
}

func (j *jt) GetJobKey() swf.JobKey {
	return j.JobKey
}

func (j *jt) AwaitJobs(jobIds ...string) error {
	return fmt.Errorf("not supported")
}

var _ recipeops.JobTool = &jt{}

func TestNewNoTaskStepIsDisallowedAndNotRegisteredAsWorker(t *testing.T) {
	orig := recipeops.List()
	recipeops.Clear()
	t.Cleanup(func() {
		recipeops.Clear()
		if len(orig) > 0 {
			recipeops.Register(orig...)
		}
	})

	type stepIn struct {
		Name string `json:"name"`
	}
	type stepOut struct {
		Confirmed bool `json:"confirmed"`
	}

	op, err := recipeops.NewOp().
		WithType("no-task-op").
		AddStep("collect", recipeops.NewNoTaskStep[stepIn, stepOut]()).
		Build()
	require.NoError(t, err)
	recipeops.Register(op.(recipeops.RegisterableOp))

	registry, err := NewActivityRegistry()
	require.NoError(t, err)

	all := registry.GetAll()
	require.Contains(t, all, "no-task-op:collect")
	entry := all["no-task-op:collect"]
	require.True(t, entry.Step.DisallowAsTask, "NoTaskStep should be marked disallowed")

	workers := registry.GetTaskWorkers(recipeops.NewServiceDepsBuilder().Build())
	require.Empty(t, workers, "disallowed steps should not be exposed as task workers")
}

func TestWithGitWorkspaceAppliesContextPatch(t *testing.T) {
	t.Parallel()

	repoDir, baseHash, nextHash := initTwoCommitRepo(t)

	controller := gitstate.NewController(nil)
	deps := recipeops.NewServiceDepsBuilder().Build()

	newBase := nextHash
	patchActivity := recipeops.NewActivityMappedOpV2[struct {
		Context map[string]interface{} `json:"context,omitempty"`
	}, struct {
		GitContextPatch map[string]interface{} `json:"git_context_patch,omitempty"`
	}](
		recipeops.OpMetadata{
			Type:        "test_git_context_patch",
			Description: "emits git context patch",
			Version:     "1.0.0",
		},
		func(inv recipeops.OpDependencies, ctx context.Context, input struct {
			Context map[string]interface{} `json:"context,omitempty"`
		}) (struct {
			GitContextPatch map[string]interface{} `json:"git_context_patch,omitempty"`
		}, error) {
			return struct {
				GitContextPatch map[string]interface{} `json:"git_context_patch,omitempty"`
			}{
				GitContextPatch: map[string]interface{}{"base_hash": newBase},
			}, nil
		},
	)

	step := patchActivity.TaskChain()[0]
	registration := ActivityRegistration{
		Activity:  patchActivity,
		Step:      step,
		StepIndex: 0,
		TaskType:  fmt.Sprintf("%s:%s", patchActivity.GetMetadata().Type, step.Name),
		Metadata:  patchActivity.GetMetadata(),
	}
	wrapped := opExecutor{deps: deps, reg: registration, controller: controller}.do

	jobKey := swf.JobKey{TenantId: "test", JobId: "job-1"}
	output, artifacts, err := wrapped(context.Background(), &jt{jobKey}, ActivityInvocationRequest{
		Input: map[string]interface{}{
			"context": map[string]interface{}{
				"git": map[string]interface{}{
					"base_hash":    baseHash,
					"persist_hash": baseHash,
				},
			},
		},
		GitTaskContext: gitstate.GlobalGitTaskContext{
			BaseRepo:         repoDir,
			BaseRef:          baseHash,
			ResolvedBaseHash: baseHash,
			PersistHash:      "",
			ParentHash:       "",
			CellName:         "cells/beta",
			CellPath:         "cells/beta",
			GitAuthor:        "",
			NodePath:         "",
			InvokeSeq:        0,
			InvokeHash:       "",
		},
	}, nil)
	require.NoError(t, err)
	require.Empty(t, artifacts)

	patch, ok := output.OpOutput["git_context_patch"].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, newBase, patch["base_hash"])
	assert.Equal(t, baseHash, output.GitResult.ParentRef)
	assert.Empty(t, output.GitResult.PersistHash)
}

type fakeJobTool struct {
	jobKey swf.JobKey
}

func TestWithGitWorkspaceProducesDiffAndThinPack(t *testing.T) {
	t.Parallel()

	repoDir, baseHash, _ := initTwoCommitRepo(t)

	controller := gitstate.NewController(nil)
	deps := recipeops.NewServiceDepsBuilder().Build()

	type writeInput struct {
		Message string `json:"message"`
	}
	type writeOutput struct {
		Path string `json:"path"`
	}

	writeActivity := recipeops.NewActivityMappedOpV2[writeInput, writeOutput](
		recipeops.OpMetadata{
			Type:        "test_write_op",
			Description: "writes file in worktree",
			Version:     "1.0.0",
		},
		func(inv recipeops.OpDependencies, ctx context.Context, input writeInput) (writeOutput, error) {
			path := filepath.Join(inv.WorktreePath(), "cells", "beta", "note.txt")
			if err := os.WriteFile(path, []byte(input.Message), 0o644); err != nil {
				return writeOutput{}, err
			}
			return writeOutput{Path: path}, nil
		},
	)

	step := writeActivity.TaskChain()[0]
	registration := ActivityRegistration{
		Activity:  writeActivity,
		Step:      step,
		StepIndex: 0,
		TaskType:  fmt.Sprintf("%s:%s", writeActivity.GetMetadata().Type, step.Name),
		Metadata:  writeActivity.GetMetadata(),
	}
	wrapped := opExecutor{deps: deps, reg: registration, controller: controller}.do

	jobKey := swf.JobKey{TenantId: "test", JobId: "job-2"}
	_, artifacts, err := wrapped(context.Background(), &jt{jobKey}, ActivityInvocationRequest{
		Input: map[string]interface{}{
			"message": "hello",
		},
		GitTaskContext: gitstate.GlobalGitTaskContext{
			BaseRepo:         repoDir,
			BaseRef:          baseHash,
			ResolvedBaseHash: baseHash,
			PersistHash:      "",
			ParentHash:       "",
			CellName:         "cells/beta",
			CellPath:         "cells/beta",
			GitAuthor:        "",
			NodePath:         "",
			InvokeSeq:        0,
			InvokeHash:       "",
		},
	}, nil)
	require.NoError(t, err)
	require.NotEmpty(t, artifacts)

	artifactNames := map[string]bool{}
	for _, artifact := range artifacts {
		artifactNames[artifact.Name()] = true
	}
	assert.True(t, artifactNames[gitstate.ThinPackArtifactName], "expected thin pack artifact")
	assert.True(t, artifactNames["diff_from_parent.diff"], "expected parent diff artifact")
}

type stubJobTool struct {
	JobKey swf.JobKey
}

func (s *stubJobTool) GetJobKey() swf.JobKey {
	return s.JobKey
}
func (s *stubJobTool) AwaitJobs(jobIds ...string) error {
	return nil
}

var _ recipeops.JobTool = &stubJobTool{}

func TestEnableActivitiesInWorkerInjectsDependencies(t *testing.T) {
	t.Parallel()

	db := &gorm.DB{}
	wc := &stubWorkflowControl{}
	deps := recipeops.NewServiceDepsBuilder().
		WithDatabase(db).
		WithWorkflowControl(wc).
		Build()
	registry, err := NewActivityRegistry()
	require.NoError(t, err)

	type depInput struct {
		Message string `json:"message"`
	}
	type depOutput struct {
		Acknowledged bool `json:"acknowledged"`
	}

	const activityType = "deps_injection_check"
	seenDeps := false
	activity := recipeops.NewActivityMappedOpV2[depInput, depOutput](
		recipeops.OpMetadata{Type: activityType, Description: "ensure deps present", Version: "1.0.0"},
		func(inv recipeops.OpDependencies, ctx context.Context, input depInput) (depOutput, error) {
			require.Same(t, db, inv.Database())
			require.Same(t, wc, inv.WorkflowControl())
			seenDeps = true
			return depOutput{Acknowledged: true}, nil
		},
	)
	require.NoError(t, Register(registry, activity))

	worker := newCapturingWorker(t)
	registry.EnableActivitiesInWorker(deps, worker)

	taskType := fmt.Sprintf("%s:%s", activityType, activityType)
	handler, ok := worker.handlers[taskType]
	require.True(t, ok)

	repoPath, baseHash, _ := initTwoCommitRepo(t)
	input := map[string]interface{}{"message": "hi"}
	jobKey := swf.JobKey{TenantId: "test", JobId: "job-3"}
	_, _, err = handler(context.Background(), &stubJobTool{JobKey: jobKey}, ActivityInvocationRequest{
		Input: input,
		GitTaskContext: gitstate.GlobalGitTaskContext{
			BaseRepo:         repoPath,
			BaseRef:          baseHash,
			ResolvedBaseHash: baseHash,
			CellName:         "cells/cell-a",
			CellPath:         "cells/cell-a",
			PersistHash:      "",
			ParentHash:       "",
			GitAuthor:        "",
			NodePath:         "",
			InvokeSeq:        0,
			InvokeHash:       "",
		},
	}, nil)
	require.NoError(t, err)
	require.True(t, seenDeps)
}

type capturingWorker struct {
	t        *testing.T
	handlers map[string]func(context.Context, recipeops.JobTool, ActivityInvocationRequest, []swf.Artifact) (ActivityInvocationOutput, []swf.Artifact, error)
}

func newCapturingWorker(t *testing.T) *capturingWorker {
	return &capturingWorker{t: t, handlers: make(map[string]func(context.Context, recipeops.JobTool, ActivityInvocationRequest, []swf.Artifact) (ActivityInvocationOutput, []swf.Artifact, error))}
}

func (c *capturingWorker) RegisterActivityWithOptions(a interface{}, options activity.RegisterOptions) {
	handler, ok := a.(func(context.Context, recipeops.JobTool, ActivityInvocationRequest, []swf.Artifact) (ActivityInvocationOutput, []swf.Artifact, error))
	if !ok {
		panic(fmt.Errorf("%s expected func(context.Context, swf.JobKey, ActivityInvocationRequest, []swf.Artifact) (ActivityInvocationOutput, []swf.Artifact, error), got %T", options.Name, a))
	}
	c.handlers[options.Name] = handler
}

type stubWorkflowControl struct{}

func (s *stubWorkflowControl) JobResult(ctx context.Context, key swf.JobKey) (swf.JobData, error) {
	return nil, nil
}

func (s *stubWorkflowControl) AwaitJobs(ctx context.Context, jobKeys []swf.JobKey) error {
	return nil
}

func (s *stubWorkflowControl) GetWaitingTask(ctx context.Context, jobKey swf.JobKey) (workflowctl.TaskHandle, error) {
	return nil, nil
}

func (s *stubWorkflowControl) CompleteTask(ctx context.Context, jobKey swf.JobKey, taskOrdinal int64, hash string, data any) error {
	return nil
}

var _ workflowctl.WorkflowControl = &stubWorkflowControl{}

func (s *stubWorkflowControl) StartJob(ctx context.Context, req workflowctl.StartJob) (swf.JobKey, error) {
	_ = ctx
	_ = req
	return swf.JobKey{}, nil
}

func (s *stubWorkflowControl) Cancel(ctx context.Context, jobKey swf.JobKey) error {
	_ = ctx
	_ = jobKey
	return nil
}

func (s *stubWorkflowControl) ListJobs(ctx context.Context, request swf.ListJobsRequest) ([]workflowctl.JobItem, string, error) {
	_ = ctx
	_ = request
	return nil, "", nil
}

func (s *stubWorkflowControl) GetArtifactLazy(ctx context.Context, tenantId string, key swf.ArtifactKey) swf.Artifact {
	_ = ctx
	_ = tenantId
	_ = key
	return nil
}

func initTwoCommitRepo(t *testing.T) (string, string, string) {
	t.Helper()

	root := t.TempDir()
	repoDir := filepath.Join(root, "repo")
	runGitCmd(t, root, "git", "init", repoDir)
	runGitCmd(t, repoDir, "git", "config", "user.name", "Tester")
	runGitCmd(t, repoDir, "git", "config", "user.email", "tester@example.com")
	writeFile(t, repoDir, "README.md", "first\n")
	for _, cell := range []string{"beta", "cell-a", "test-cell"} {
		writeFile(t, repoDir, filepath.Join("cells", cell, "README.md"), cell+"\n")
	}
	runGitCmd(t, repoDir, "git", "add", ".")
	runGitCmd(t, repoDir, "git", "commit", "-m", "first")
	baseHash := strings.TrimSpace(runGitCmd(t, repoDir, "git", "rev-parse", "HEAD"))
	runGitCmd(t, repoDir, "git", "checkout", "-B", "main")
	writeFile(t, repoDir, "README.md", "second\n")
	runGitCmd(t, repoDir, "git", "add", ".")
	runGitCmd(t, repoDir, "git", "commit", "-m", "second")
	nextHash := strings.TrimSpace(runGitCmd(t, repoDir, "git", "rev-parse", "HEAD"))
	return repoDir, baseHash, nextHash
}

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir failed: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write file failed: %v", err)
	}
}

func runGitCmd(t *testing.T, dir string, name string, args ...string) string {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed: %v (%s)", args, err, output)
	}
	return string(output)
}

func TestActivityRegistration(t *testing.T) {
	registry, err := NewActivityRegistry()
	require.NoError(t, err)

	t.Run("successful registration", func(t *testing.T) {
		err := Register(registry, testActivity)
		assert.NoError(t, err)

		// Verify activity was registered
		taskType := "test_registry_activity:test_registry_activity"
		registration, exists := registry.Get(taskType)
		assert.True(t, exists)
		assert.NotNil(t, registration.Activity)
		assert.NotNil(t, registration.InputSchema)
		assert.NotNil(t, registration.OutputSchema)
		assert.Equal(t, "test_registry_activity", registration.Metadata.Type)
		assert.Equal(t, "test_registry_activity", registration.Step.Name)
	})

	t.Run("duplicate registration fails", func(t *testing.T) {
		err := Register(registry, testActivity)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "already registered")
	})

	t.Run("list registered activities", func(t *testing.T) {
		types := registry.List()
		assert.Contains(t, types, "test_registry_activity:test_registry_activity")
	})
}

// BadActivity for testing validation failures
type BadActivity struct{}

func (a *BadActivity) GetMetadata() recipeops.OpMetadata {
	return recipeops.OpMetadata{
		Type:           "bad_activity",
		Description:    "Test activity with bad types",
		Version:        "1.0.0",
		DefaultTimeout: 30 * time.Second,
	}
}

func (a *BadActivity) Execute(ctx context.Context, input BadInput) (TestOutput, error) {
	return TestOutput{}, nil
}

func TestJSONTagValidation(t *testing.T) {
	// Test the schema generator directly for validation
	generator := NewDefaultSchemaGenerator()

	t.Run("missing config json tag fails", func(t *testing.T) {
		err := generator.ValidateStructTags(reflect.TypeOf(BadConfig{}))
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "missing required json tag")
		assert.Contains(t, err.Error(), "URL")
	})

	t.Run("missing input json tag fails", func(t *testing.T) {
		err := generator.ValidateStructTags(reflect.TypeOf(BadInput{}))
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "missing required json tag")
		assert.Contains(t, err.Error(), "Size")
	})

	t.Run("nested struct validation", func(t *testing.T) {
		type NestedBad struct {
			Field string // Missing json tag
		}

		type ConfigWithNested struct {
			Name   string    `json:"name"`
			Nested NestedBad `json:"nested"`
		}

		err := generator.ValidateStructTags(reflect.TypeOf(ConfigWithNested{}))
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "missing required json tag")
		assert.Contains(t, err.Error(), "Field")
	})

	t.Run("ignored fields are allowed", func(t *testing.T) {
		type ConfigWithIgnored struct {
			Public  string `json:"public"`
			Private string `json:"-"` // Explicitly ignored
		}

		err := generator.ValidateStructTags(reflect.TypeOf(ConfigWithIgnored{}))
		assert.NoError(t, err)
	})

	t.Run("all fields with tags pass", func(t *testing.T) {
		err := generator.ValidateStructTags(reflect.TypeOf(TestConfig{}))
		assert.NoError(t, err)

		err = generator.ValidateStructTags(reflect.TypeOf(TestInput{}))
		assert.NoError(t, err)

		err = generator.ValidateStructTags(reflect.TypeOf(TestOutput{}))
		assert.NoError(t, err)
	})
}

func TestSchemaGeneration(t *testing.T) {
	registry, err := NewActivityRegistry()
	require.NoError(t, err)

	err = Register(registry, testActivity)
	require.NoError(t, err)

	registration, exists := registry.Get("test_registry_activity:test_registry_activity")
	require.True(t, exists)

	// Form schema test removed - no longer part of ActivityRegistration

	t.Run("input schema", func(t *testing.T) {
		schema := registration.InputSchema
		assert.NotNil(t, schema)

		schemaJSON, err := json.Marshal(schema)
		require.NoError(t, err)

		var schemaMap map[string]interface{}
		err = json.Unmarshal(schemaJSON, &schemaMap)
		require.NoError(t, err)

		properties, ok := schemaMap["properties"].(map[string]interface{})
		assert.True(t, ok)
		assert.Contains(t, properties, "data")
		assert.Contains(t, properties, "size")
	})
}

// TestActivityProvider tests are commented out to avoid circular import
// These tests should be moved to a separate test package or integration tests
// func TestActivityProvider(t *testing.T) {
// 	registry := NewActivityRegistry()
//
// 	err := Register[TestInput, TestOutput](registry, testActivity)
// 	require.NoError(t, err)
//
// 	registration, _ := registry.Get("test_activity")
// 	provider := worker.NewActivityProvider(testActivity, registration)
//
// 	t.Run("provider metadata", func(t *testing.T) {
// 		assert.Equal(t, "test_activity", provider.GetType())
// 		assert.Equal(t, "A test activity for unit testing", provider.GetDescription())
// 	})
//
// 	t.Run("provider execution", func(t *testing.T) {
// 		ctx := context.Background()
//
// 		config := map[string]interface{}{
// 			"url":     "https://example.com",
// 			"timeout": 30,
// 		}
//
// 		input := map[string]interface{}{
// 			"data": "test data",
// 			"size": 100,
// 		}
//
// 		result, err := provider.Run(ctx, config, input)
// 		assert.NoError(t, err)
//
// 		output, ok := result.(map[string]interface{})
// 		assert.True(t, ok)
// 		assert.Equal(t, "test data processed", output["result"])
// 		assert.Equal(t, true, output["success"])
// 	})
//
// 	t.Run("provider schemas", func(t *testing.T) {
// 		configSchema, inputSchema, outputSchema := provider.GetSchemas()
// 		assert.NotNil(t, configSchema)
// 		assert.NotNil(t, inputSchema)
// 		assert.NotNil(t, outputSchema)
// 	})
// }

// setupGitRepo creates a test git repository
func setupGitRepo(t *testing.T) (string, string, func()) {
	t.Helper()
	dir := t.TempDir()
	repoPath := filepath.Join(dir, "base")
	require.NoError(t, os.MkdirAll(repoPath, 0o755))
	exec.Command("git", "init", repoPath).CombinedOutput()
	exec.Command("git", "-C", repoPath, "config", "user.email", "test@example.com").CombinedOutput()
	exec.Command("git", "-C", repoPath, "config", "user.name", "Test User").CombinedOutput()
	require.NoError(t, os.WriteFile(filepath.Join(repoPath, "README.md"), []byte("initial\n"), 0o644))
	exec.Command("git", "-C", repoPath, "add", ".").CombinedOutput()
	exec.Command("git", "-C", repoPath, "commit", "-m", "init").CombinedOutput()

	output, _ := exec.Command("git", "-C", repoPath, "rev-parse", "HEAD").CombinedOutput()
	head := strings.TrimSpace(string(output))
	return repoPath, head, func() { os.RemoveAll(dir) }
}

func TestWithGitWorkspace_ThinPackFiltering(t *testing.T) {
	t.Parallel()

	thinPackArt := &mockArtifact{name: gitstate.ThinPackArtifactName, data: []byte("thin pack data")}
	userArt1 := &mockArtifact{name: "user_file.txt", data: []byte("user data 1")}
	userArt2 := &mockArtifact{name: "another.txt", data: []byte("user data 2")}

	inputArtifacts := []swf.Artifact{userArt1, thinPackArt, userArt2}
	// Store the thin pack as interface for comparison
	var inputThinPack swf.Artifact
	for _, art := range inputArtifacts {
		if art.Name() == gitstate.ThinPackArtifactName {
			inputThinPack = art
		}
	}

	// Track which artifacts the operation receives and which artifacts we add
	var receivedArtifacts []swf.Artifact
	var addedArtifacts []swf.Artifact

	reg := ActivityRegistration{
		Step: recipeops.TaskStep{
			Invoke: func(deps recipeops.OpDependencies, ctx context.Context, input map[string]interface{}) (map[string]interface{}, error) {
				receivedArtifacts = deps.GetInputArtifacts()
				// Also add the received artifacts as output to test pass-through
				for _, art := range receivedArtifacts {
					deps.AddOutputArtifact(art)
					addedArtifacts = append(addedArtifacts, art)
				}
				return map[string]interface{}{"result": "ok"}, nil
			},
		},
	}

	// Create a real git repo for testing
	baseRepo, baseHash, cleanup := setupGitRepo(t)
	defer cleanup()

	controller := gitstate.NewController(nil)
	wrapped := opExecutor{deps: recipeops.NewServiceDepsBuilder().Build(), reg: reg, controller: controller}.do
	req := ActivityInvocationRequest{
		Input: map[string]interface{}{},
		GitTaskContext: gitstate.GlobalGitTaskContext{
			BaseRepo: baseRepo,
			BaseRef:  baseHash, CellPath: "cells/test",
		},
	}

	jobKey := swf.JobKey{TenantId: "test", JobId: "job-4"}
	_, outputArts, err := wrapped(context.Background(), &jt{jobKey}, req, inputArtifacts)
	require.NoError(t, err)

	// Verify the operation received only user artifacts (thin pack filtered out).
	require.Len(t, receivedArtifacts, 2, "operation should receive only user artifacts")
	for _, art := range receivedArtifacts {
		assert.NotEqual(t, gitstate.ThinPackArtifactName, art.Name(), "thin pack should be filtered from op inputs")
	}

	// Verify output contains the thin pack artifact (passed through)
	var foundThinPack swf.Artifact
	var foundUserArts []swf.Artifact
	for _, art := range outputArts {
		if art.Name() == gitstate.ThinPackArtifactName {
			foundThinPack = art
		} else {
			foundUserArts = append(foundUserArts, art)
		}
	}

	// Critical test: thin pack should be passed through to output
	require.NotNil(t, foundThinPack, "thin pack should be in output")
	// Verify it's the SAME artifact (pointer equality) - this is the key requirement
	require.True(t, foundThinPack == inputThinPack, "should be same artifact reference (pointer equality)")

	// User artifacts should also be in output (added by the operation)
	require.Len(t, addedArtifacts, 2, "operation should pass through user artifacts")
}

func TestWithGitWorkspace_NoThinPackPassThrough(t *testing.T) {
	t.Parallel()

	userArt := &mockArtifact{name: "user_file.txt", data: []byte("user data")}
	inputArtifacts := []swf.Artifact{userArt}

	reg := ActivityRegistration{
		Step: recipeops.TaskStep{
			Invoke: func(deps recipeops.OpDependencies, ctx context.Context, input map[string]interface{}) (map[string]interface{}, error) {
				return map[string]interface{}{"result": "ok"}, nil
			},
		},
	}

	// Create a real git repo for testing
	baseRepo, baseHash, cleanup := setupGitRepo(t)
	defer cleanup()

	controller := gitstate.NewController(nil)
	wrapped := opExecutor{deps: recipeops.NewServiceDepsBuilder().Build(), reg: reg, controller: controller}.do
	req := ActivityInvocationRequest{
		Input: map[string]interface{}{},
		GitTaskContext: gitstate.GlobalGitTaskContext{
			BaseRepo: baseRepo,
			BaseRef:  baseHash, CellPath: "cells/test",
		},
	}

	jobKey := swf.JobKey{TenantId: "test", JobId: "job-5"}
	_, outputArts, err := wrapped(context.Background(), &jt{jobKey}, req, inputArtifacts)
	require.NoError(t, err)

	// Critical test: When there's no input thin pack and Persist returns nil,
	// output should not have thin pack
	foundThinPack := false
	for _, art := range outputArts {
		if art.Name() == gitstate.ThinPackArtifactName {
			foundThinPack = true
		}
	}
	require.False(t, foundThinPack, "thin pack should not be in output when not in input and Persist returns nil")

	// NOTE: Same builder bug as above test - artifacts won't be passed to operation
	// But this doesn't affect the real system because in production, the SWF engine
	// properly provides artifacts to tasks
}

func TestWithGitWorkspace_OperationFailure_PreservesArtifacts(t *testing.T) {
	t.Parallel()

	// Create artifacts that the operation will add before failing
	artifact1 := &mockArtifact{name: "stdout.txt", data: []byte("operation output")}
	artifact2 := &mockArtifact{name: "stderr.txt", data: []byte("operation error")}

	reg := ActivityRegistration{
		Step: recipeops.TaskStep{
			Invoke: func(deps recipeops.OpDependencies, ctx context.Context, input map[string]interface{}) (map[string]interface{}, error) {
				// Add artifacts then fail
				deps.AddOutputArtifact(artifact1)
				deps.AddOutputArtifact(artifact2)
				return nil, fmt.Errorf("operation failed")
			},
		},
	}

	baseRepo, baseHash, cleanup := setupGitRepo(t)
	defer cleanup()

	controller := gitstate.NewController(nil)
	wrapped := opExecutor{deps: recipeops.NewServiceDepsBuilder().Build(), reg: reg, controller: controller}.do
	req := ActivityInvocationRequest{
		Input: map[string]interface{}{},
		GitTaskContext: gitstate.GlobalGitTaskContext{
			BaseRepo: baseRepo,
			BaseRef:  baseHash, CellPath: "cells/test",
		},
	}

	jobKey := swf.JobKey{TenantId: "test", JobId: "job-6"}
	_, outputArts, err := wrapped(context.Background(), &jt{jobKey}, req, nil)

	// Verify operation failed
	require.Error(t, err)
	assert.Contains(t, err.Error(), "operation failed")

	// Critical test: Artifacts should be preserved even though operation failed
	require.Len(t, outputArts, 2, "artifacts should be preserved on failure")

	// Verify both artifacts are present
	foundArtifacts := make(map[string]bool)
	for _, art := range outputArts {
		foundArtifacts[art.Name()] = true
	}
	assert.True(t, foundArtifacts["stdout.txt"], "stdout artifact should be preserved")
	assert.True(t, foundArtifacts["stderr.txt"], "stderr artifact should be preserved")
}

func TestWithGitWorkspace_OperationArtifactsPreservedRegardlessOfPersist(t *testing.T) {
	t.Parallel()

	// This test verifies that operation artifacts are always preserved,
	// even if git persist has issues. The defer ensures artifacts are collected
	// on all code paths.

	artifact1 := &mockArtifact{name: "output.txt", data: []byte("operation output")}

	reg := ActivityRegistration{
		Step: recipeops.TaskStep{
			Invoke: func(deps recipeops.OpDependencies, ctx context.Context, input map[string]interface{}) (map[string]interface{}, error) {
				// Operation succeeds and adds artifacts
				deps.AddOutputArtifact(artifact1)
				return map[string]interface{}{"result": "ok"}, nil
			},
		},
	}

	baseRepo, baseHash, cleanup := setupGitRepo(t)
	defer cleanup()

	controller := gitstate.NewController(nil)
	wrapped := opExecutor{deps: recipeops.NewServiceDepsBuilder().Build(), reg: reg, controller: controller}.do
	req := ActivityInvocationRequest{
		Input: map[string]interface{}{},
		GitTaskContext: gitstate.GlobalGitTaskContext{
			BaseRepo: baseRepo,
			BaseRef:  baseHash, CellPath: "cells/test",
		},
	}

	jobKey := swf.JobKey{TenantId: "test", JobId: "job-7"}
	_, outputArts, err := wrapped(context.Background(), &jt{jobKey}, req, nil)
	require.NoError(t, err)

	// Critical test: Operation artifacts should be preserved
	require.GreaterOrEqual(t, len(outputArts), 1, "operation artifacts should be preserved")

	foundOutput := false
	for _, art := range outputArts {
		if art.Name() == "output.txt" {
			foundOutput = true
			break
		}
	}
	assert.True(t, foundOutput, "operation artifact should be in output")
}

func TestWithGitWorkspace_RestoreFailure_ReturnsNoArtifacts(t *testing.T) {
	t.Parallel()

	operationCalled := false
	reg := ActivityRegistration{
		Step: recipeops.TaskStep{
			Invoke: func(deps recipeops.OpDependencies, ctx context.Context, input map[string]interface{}) (map[string]interface{}, error) {
				operationCalled = true
				return map[string]interface{}{"result": "ok"}, nil
			},
		},
	}

	// Use invalid repo path to force Restore to fail
	invalidRepo := "/nonexistent/repo"
	invalidHash := "0000000000000000000000000000000000000000"

	controller := gitstate.NewController(nil)
	wrapped := opExecutor{deps: recipeops.NewServiceDepsBuilder().Build(), reg: reg, controller: controller}.do
	req := ActivityInvocationRequest{
		Input: map[string]interface{}{},
		GitTaskContext: gitstate.GlobalGitTaskContext{
			BaseRepo: invalidRepo,
			BaseRef:  invalidHash},
	}

	jobKey := swf.JobKey{TenantId: "test", JobId: "job-8"}
	_, outputArts, err := wrapped(context.Background(), &jt{jobKey}, req, nil)

	// Verify restore failed
	require.Error(t, err)

	// Critical test: No artifacts should be returned because operation never ran
	assert.Empty(t, outputArts, "no artifacts should be returned when restore fails")

	// Verify operation was never called
	assert.False(t, operationCalled, "operation should not be called when restore fails")
}

func TestWithGitWorkspace_SuccessPath_StillWorks(t *testing.T) {
	t.Parallel()

	artifact1 := &mockArtifact{name: "output.txt", data: []byte("success")}

	reg := ActivityRegistration{
		Step: recipeops.TaskStep{
			Invoke: func(deps recipeops.OpDependencies, ctx context.Context, input map[string]interface{}) (map[string]interface{}, error) {
				deps.AddOutputArtifact(artifact1)
				return map[string]interface{}{"result": "success"}, nil
			},
		},
	}

	baseRepo, baseHash, cleanup := setupGitRepo(t)
	defer cleanup()

	controller := gitstate.NewController(nil)
	wrapped := opExecutor{deps: recipeops.NewServiceDepsBuilder().Build(), reg: reg, controller: controller}.do
	req := ActivityInvocationRequest{
		Input: map[string]interface{}{},
		GitTaskContext: gitstate.GlobalGitTaskContext{
			BaseRepo: baseRepo,
			BaseRef:  baseHash, CellPath: "cells/test",
		},
	}

	jobKey := swf.JobKey{TenantId: "test", JobId: "job-9"}
	output, outputArts, err := wrapped(context.Background(), &jt{jobKey}, req, nil)

	// Verify success
	require.NoError(t, err)
	assert.Equal(t, "success", output.OpOutput["result"])

	// Verify artifacts are present (operation artifact + potentially git artifact)
	require.NotEmpty(t, outputArts, "artifacts should be present on success")

	foundOutputTxt := false
	for _, art := range outputArts {
		if art.Name() == "output.txt" {
			foundOutputTxt = true
		}
	}
	assert.True(t, foundOutputTxt, "operation artifact should be in output")
}

func TestControllerPersist_DirectCall(t *testing.T) {
	t.Parallel()

	// Simple reproducer: directly call Controller methods to verify thinpack creation
	baseRepo, baseHash, cleanup := setupGitRepo(t)
	defer cleanup()
	worktree := filepath.Join(t.TempDir(), "worktree")

	ctx := &gitstate.GitTaskContext{
		GlobalGitTaskContext: &gitstate.GlobalGitTaskContext{
			BaseRepo:         baseRepo,
			BaseRef:          baseHash,
			ResolvedBaseHash: baseHash,
			PersistHash:      "",
			ParentHash:       "",
			CellName:         "cells/alpha/test", // Full file system path to the cell directory
			CellPath:         "cells/alpha/test",
			NodePath:         "recipe/node", // Recipe node path (not file system path)
			InvokeSeq:        1,
			InvokeHash:       "",
			GitAuthor:        "",
		},
		WorktreePath: worktree,
	}

	controller := gitstate.NewController(nil)

	// Prepare and restore
	require.NoError(t, controller.Restore(context.Background(), ctx, nil))

	// Make a change inside the node path directory
	nodeDir := filepath.Join(worktree, "cells", "alpha", "test")
	require.NoError(t, os.MkdirAll(nodeDir, 0o755))
	newFilePath := filepath.Join(nodeDir, "new_file.txt")
	require.NoError(t, os.WriteFile(newFilePath, []byte("new content\n"), 0o644))

	// Debug: Check git status before persist
	cmd := exec.Command("git", "-C", worktree, "status", "--porcelain")
	statusOutput, _ := cmd.CombinedOutput()
	t.Logf("Git status before Persist: %s", string(statusOutput))

	// Debug: Check if file exists before persist
	_, err := os.Stat(newFilePath)
	t.Logf("File exists before Persist: %v (err: %v)", err == nil, err)

	// Call Persist
	output, artifact, err := controller.Persist(context.Background(), ctx)

	// Debug: Check if file exists after persist
	_, statErr := os.Stat(newFilePath)
	t.Logf("File exists after Persist: %v (err: %v)", statErr == nil, statErr)

	// Debug: Check git status after persist
	cmd = exec.Command("git", "-C", worktree, "status", "--porcelain")
	statusOutput, _ = cmd.CombinedOutput()
	t.Logf("Git status after Persist: %s", string(statusOutput))

	// Verify success
	require.NoError(t, err)
	require.NotNil(t, output, "output should not be nil")

	t.Logf("HasChanges: %v", output.HasChanges)
	t.Logf("CommitHash: %s", output.CommitHash)
	t.Logf("Artifact: %v", artifact)

	// CRITICAL: Verify thinpack artifact was created
	require.True(t, output.HasChanges, "changes should be detected")
	require.NotNil(t, artifact, "thinpack artifact should be created when changes are made")
	require.Equal(t, gitstate.ThinPackArtifactName, artifact.Name())
	require.NotEmpty(t, ctx.PersistHash, "persist hash should be set")
}

func TestWithGitWorkspace_NewThinPackCreatedWhenChanges(t *testing.T) {
	t.Parallel()

	// Test that when an operation makes git changes, a new thinpack artifact is created and returned
	reg := ActivityRegistration{
		Step: recipeops.TaskStep{
			Invoke: func(deps recipeops.OpDependencies, ctx context.Context, input map[string]interface{}) (map[string]interface{}, error) {
				// Make a change to the worktree inside the cell directory
				worktreePath := deps.WorktreePath()
				cellDir := filepath.Join(worktreePath, "cells", "test")
				require.NoError(t, os.MkdirAll(cellDir, 0o755))
				newFilePath := filepath.Join(cellDir, "new_file.txt")
				err := os.WriteFile(newFilePath, []byte("new content\n"), 0o644)
				require.NoError(t, err)

				return map[string]interface{}{"result": "modified"}, nil
			},
		},
	}

	baseRepo, baseHash, cleanup := setupGitRepo(t)
	defer cleanup()

	controller := gitstate.NewController(nil)
	wrapped := opExecutor{deps: recipeops.NewServiceDepsBuilder().Build(), reg: reg, controller: controller}.do
	req := ActivityInvocationRequest{
		Input: map[string]interface{}{},
		GitTaskContext: gitstate.GlobalGitTaskContext{
			BaseRepo: baseRepo,
			BaseRef:  baseHash, CellName: "cells/test",
			CellPath: "cells/test",
		},
	}

	jobKey := swf.JobKey{TenantId: "test", JobId: "job-10"}
	output, outputArts, err := wrapped(context.Background(), &jt{jobKey}, req, nil)

	// Verify success
	require.NoError(t, err)
	assert.Equal(t, "modified", output.OpOutput["result"])

	// Debug: print all artifacts
	t.Logf("Output artifacts count: %d", len(outputArts))
	for i, art := range outputArts {
		t.Logf("Artifact %d: %s", i, art.Name())
	}
	t.Logf("PersistHash from output: %s", output.GitResult.PersistHash)
	t.Logf("ParentHash from output: %s", output.GitResult.ParentHash)

	// CRITICAL: Verify thinpack artifact was created
	var foundThinPack swf.Artifact
	for _, art := range outputArts {
		if art.Name() == gitstate.ThinPackArtifactName {
			foundThinPack = art
			break
		}
	}

	require.NotNil(t, foundThinPack, "thinpack artifact should be created when operation makes changes")

	// Verify the artifact is readable (has actual content)
	reader, err := foundThinPack.Open()
	require.NoError(t, err)
	require.NotNil(t, reader, "thinpack should have readable content")
	reader.Close()

	// Verify persist hash was set (indicates changes were persisted)
	require.NotEmpty(t, output.GitResult.PersistHash, "persist hash should be set when changes are made")
}

func TestWithGitWorkspace_NewThinPackReplacesInputWhenChanges(t *testing.T) {
	t.Parallel()

	// Test that when there's an input thinpack but the operation makes changes,
	// the NEW thinpack is returned (not the input one)
	inputThinPack := &mockArtifact{name: gitstate.ThinPackArtifactName, data: []byte("old thin pack data")}

	reg := ActivityRegistration{
		Step: recipeops.TaskStep{
			Invoke: func(deps recipeops.OpDependencies, ctx context.Context, input map[string]interface{}) (map[string]interface{}, error) {
				// Make a change to the worktree inside the cell directory
				worktreePath := deps.WorktreePath()
				cellDir := filepath.Join(worktreePath, "cells", "test")
				require.NoError(t, os.MkdirAll(cellDir, 0o755))
				modifiedFile := filepath.Join(cellDir, "modified.txt")
				err := os.WriteFile(modifiedFile, []byte("modified content\n"), 0o644)
				require.NoError(t, err)

				return map[string]interface{}{"result": "updated"}, nil
			},
		},
	}

	baseRepo, baseHash, cleanup := setupGitRepo(t)
	defer cleanup()

	controller := gitstate.NewController(nil)
	wrapped := opExecutor{deps: recipeops.NewServiceDepsBuilder().Build(), reg: reg, controller: controller}.do
	req := ActivityInvocationRequest{
		Input: map[string]interface{}{},
		GitTaskContext: gitstate.GlobalGitTaskContext{
			BaseRepo: baseRepo,
			BaseRef:  baseHash, CellName: "cells/test",
			CellPath: "cells/test",
		},
	}

	inputArtifacts := []swf.Artifact{inputThinPack}
	jobKey := swf.JobKey{TenantId: "test", JobId: "job-11"}
	output, outputArts, err := wrapped(context.Background(), &jt{jobKey}, req, inputArtifacts)

	// Verify success
	require.NoError(t, err)
	assert.Equal(t, "updated", output.OpOutput["result"])

	// CRITICAL: Verify a thinpack artifact is in output
	var foundThinPack swf.Artifact
	for _, art := range outputArts {
		if art.Name() == gitstate.ThinPackArtifactName {
			foundThinPack = art
			break
		}
	}

	require.NotNil(t, foundThinPack, "thinpack artifact should be in output")

	// CRITICAL: Verify it's NOT the same artifact as input (pointer inequality)
	// When changes are made, Persist creates a NEW thinpack, not the input one
	require.False(t, foundThinPack == inputThinPack, "should be a NEW thinpack artifact (not the input), because changes were made")

	// Verify the new artifact is readable
	reader, err := foundThinPack.Open()
	require.NoError(t, err)
	require.NotNil(t, reader, "new thinpack should have readable content")
	reader.Close()

	// Verify persist hash was set
	require.NotEmpty(t, output.GitResult.PersistHash, "persist hash should be set when changes are made")
}

func TestWithGitWorkspace_PersistWithDiffs_CreatesThreeArtifacts(t *testing.T) {
	t.Parallel()

	// Test that when an operation makes git changes, PersistWithDiffs creates 3 artifacts:
	// 1. thin pack, 2. diff from parent, 3. diff from base
	reg := ActivityRegistration{
		Step: recipeops.TaskStep{
			Invoke: func(deps recipeops.OpDependencies, ctx context.Context, input map[string]interface{}) (map[string]interface{}, error) {
				// Make a change to the worktree inside the cell directory
				worktreePath := deps.WorktreePath()
				cellDir := filepath.Join(worktreePath, "cells", "test")
				require.NoError(t, os.MkdirAll(cellDir, 0o755))
				newFilePath := filepath.Join(cellDir, "new_file.txt")
				err := os.WriteFile(newFilePath, []byte("new content\n"), 0o644)
				require.NoError(t, err)

				return map[string]interface{}{"result": "modified"}, nil
			},
		},
	}

	baseRepo, baseHash, cleanup := setupGitRepo(t)
	defer cleanup()

	controller := gitstate.NewController(nil)
	wrapped := opExecutor{deps: recipeops.NewServiceDepsBuilder().Build(), reg: reg, controller: controller}.do
	req := ActivityInvocationRequest{
		Input: map[string]interface{}{},
		GitTaskContext: gitstate.GlobalGitTaskContext{
			BaseRepo: baseRepo,
			BaseRef:  baseHash, CellName: "cells/test",
			CellPath: "cells/test",
		},
	}

	jobKey := swf.JobKey{TenantId: "test", JobId: "job-12"}
	output, outputArts, err := wrapped(context.Background(), &jt{jobKey}, req, nil)

	// Verify success
	require.NoError(t, err)
	assert.Equal(t, "modified", output.OpOutput["result"])

	// Debug output
	t.Logf("Output artifacts count: %d", len(outputArts))
	for i, art := range outputArts {
		t.Logf("Artifact %d: %s", i, art.Name())
	}

	// CRITICAL: Since this is the first commit (parent == base), we only get 2 artifacts
	// When parent != base, we'd get 3 artifacts
	require.Len(t, outputArts, 2, "should have 2 artifacts when parent == base: thin pack, diff_from_parent")

	// Verify artifact names
	require.Equal(t, gitstate.ThinPackArtifactName, outputArts[0].Name())
	require.Equal(t, "diff_from_parent.diff", outputArts[1].Name())

	// Verify all artifacts are readable
	for i, art := range outputArts {
		reader, err := art.Open()
		require.NoError(t, err, "artifact %d should be readable", i)
		require.NotNil(t, reader, "artifact %d should have content", i)
		reader.Close()
	}

	// Verify persist hash was set
	require.NotEmpty(t, output.GitResult.PersistHash, "persist hash should be set when changes are made")
}

func TestWithGitWorkspace_PersistWithDiffs_NoChanges_NoArtifacts(t *testing.T) {
	t.Parallel()

	// Test that when an operation makes NO changes, no diff artifacts are created
	reg := ActivityRegistration{
		Step: recipeops.TaskStep{
			Invoke: func(deps recipeops.OpDependencies, ctx context.Context, input map[string]interface{}) (map[string]interface{}, error) {
				// Don't make any changes
				return map[string]interface{}{"result": "no-op"}, nil
			},
		},
	}

	baseRepo, baseHash, cleanup := setupGitRepo(t)
	defer cleanup()

	controller := gitstate.NewController(nil)
	wrapped := opExecutor{deps: recipeops.NewServiceDepsBuilder().Build(), reg: reg, controller: controller}.do
	req := ActivityInvocationRequest{
		Input: map[string]interface{}{},
		GitTaskContext: gitstate.GlobalGitTaskContext{
			BaseRepo: baseRepo,
			BaseRef:  baseHash, CellName: "cells/test",
			CellPath: "cells/test",
		},
	}

	jobKey := swf.JobKey{TenantId: "test", JobId: "job-13"}
	output, outputArts, err := wrapped(context.Background(), &jt{jobKey}, req, nil)

	// Verify success
	require.NoError(t, err)
	assert.Equal(t, "no-op", output.OpOutput["result"])

	// Should have no output artifacts when there are no changes
	require.Empty(t, outputArts, "should have no artifacts when there are no changes")

	// Verify persist hash is empty
	require.Empty(t, output.GitResult.PersistHash, "persist hash should be empty when no changes")
}

func TestWithGitWorkspace_PersistWithDiffs_PassThroughWhenNoChanges(t *testing.T) {
	t.Parallel()

	// Test that when there's an input thinpack but NO changes, the input thinpack is passed through
	// (but no diff artifacts are created)
	inputThinPack := &mockArtifact{name: gitstate.ThinPackArtifactName, data: []byte("existing thin pack data"), id: "input-thinpack-123"}

	reg := ActivityRegistration{
		Step: recipeops.TaskStep{
			Invoke: func(deps recipeops.OpDependencies, ctx context.Context, input map[string]interface{}) (map[string]interface{}, error) {
				// Don't make any changes
				return map[string]interface{}{"result": "no-change"}, nil
			},
		},
	}

	baseRepo, baseHash, cleanup := setupGitRepo(t)
	defer cleanup()

	controller := gitstate.NewController(nil)
	wrapped := opExecutor{deps: recipeops.NewServiceDepsBuilder().Build(), reg: reg, controller: controller}.do
	req := ActivityInvocationRequest{
		Input: map[string]interface{}{},
		GitTaskContext: gitstate.GlobalGitTaskContext{
			BaseRepo: baseRepo,
			BaseRef:  baseHash, CellName: "cells/test",
			CellPath: "cells/test",
		},
	}

	inputArtifacts := []swf.Artifact{inputThinPack}
	jobKey := swf.JobKey{TenantId: "test", JobId: "job-14"}
	output, outputArts, err := wrapped(context.Background(), &jt{jobKey}, req, inputArtifacts)

	// Verify success
	require.NoError(t, err)
	assert.Equal(t, "no-change", output.OpOutput["result"])

	// Should have exactly 1 artifact (the passed-through input thinpack)
	require.Len(t, outputArts, 1, "should have 1 artifact: the passed-through input thinpack")

	// Verify it's the SAME artifact as input (pass-through)
	require.Equal(t, gitstate.ThinPackArtifactName, outputArts[0].Name())
	require.True(t, outputArts[0] == inputThinPack, "should be the same artifact (passed through)")

	// Verify persist hash is empty
	require.Empty(t, output.GitResult.PersistHash, "persist hash should be empty when no changes")
	require.Equal(t, baseHash, output.GitResult.ParentRef, "ref-mode no-change should stay ref-backed")
}

func TestWithGitWorkspace_PersistWithDiffs_PreservesHashModeWhenNoChanges(t *testing.T) {
	t.Parallel()

	reg := ActivityRegistration{
		Step: recipeops.TaskStep{
			Invoke: func(deps recipeops.OpDependencies, ctx context.Context, input map[string]interface{}) (map[string]interface{}, error) {
				return map[string]interface{}{"result": "still-clean"}, nil
			},
		},
	}

	baseRepo, parentHash, persistHash := initTwoCommitRepo(t)

	controller := gitstate.NewController(nil)
	wrapped := opExecutor{deps: recipeops.NewServiceDepsBuilder().Build(), reg: reg, controller: controller}.do
	req := ActivityInvocationRequest{
		Input: map[string]interface{}{},
		GitTaskContext: gitstate.GlobalGitTaskContext{
			BaseRepo:    baseRepo,
			BaseRef:     persistHash,
			PersistHash: persistHash,
			ParentHash:  parentHash,
			CellName:    "cells/test-cell",
			CellPath:    "cells/test-cell",
		},
	}

	jobKey := swf.JobKey{TenantId: "test", JobId: "job-14a"}
	output, outputArts, err := wrapped(context.Background(), &jt{jobKey}, req, nil)

	require.NoError(t, err)
	assert.Equal(t, "still-clean", output.OpOutput["result"])
	require.Empty(t, outputArts, "no-change hash-mode should not emit new artifacts")
	require.Equal(t, persistHash, output.GitResult.PersistHash, "hash-mode no-change should preserve the current hash")
	require.Equal(t, parentHash, output.GitResult.ParentHash, "hash-mode no-change should preserve the original parent hash")
	require.Empty(t, output.GitResult.ParentRef, "hash-mode result should stay in hash mode")
}

func TestWithGitWorkspace_ConstTreatsMutationsAsNoChange(t *testing.T) {
	t.Parallel()

	inputThinPack := &mockArtifact{name: gitstate.ThinPackArtifactName, data: []byte("existing thin pack data"), id: "input-thinpack-const"}

	reg := ActivityRegistration{
		Step: recipeops.TaskStep{
			Invoke: func(deps recipeops.OpDependencies, ctx context.Context, input map[string]interface{}) (map[string]interface{}, error) {
				worktreePath := deps.WorktreePath()
				cellDir := filepath.Join(worktreePath, "cells", "test-cell")
				require.NoError(t, os.MkdirAll(cellDir, 0o755))
				constFile := filepath.Join(cellDir, "const_mutation.txt")
				require.NoError(t, os.WriteFile(constFile, []byte("discard me\n"), 0o644))
				return map[string]interface{}{"result": "mutated-but-const"}, nil
			},
		},
	}

	baseRepo, parentHash, persistHash := initTwoCommitRepo(t)

	controller := gitstate.NewController(nil)
	wrapped := opExecutor{deps: recipeops.NewServiceDepsBuilder().Build(), reg: reg, controller: controller}.do
	req := ActivityInvocationRequest{
		Input: map[string]interface{}{},
		Const: true,
		GitTaskContext: gitstate.GlobalGitTaskContext{
			BaseRepo:    baseRepo,
			BaseRef:     persistHash,
			PersistHash: persistHash,
			ParentHash:  parentHash,
			CellName:    "cells/test-cell",
			CellPath:    "cells/test-cell",
		},
	}

	jobKey := swf.JobKey{TenantId: "test", JobId: "job-14b"}
	output, outputArts, err := wrapped(context.Background(), &jt{jobKey}, req, []swf.Artifact{inputThinPack})

	require.NoError(t, err)
	assert.Equal(t, "mutated-but-const", output.OpOutput["result"])
	require.Equal(t, persistHash, output.GitResult.PersistHash, "const should preserve incoming hash-mode state")
	require.Equal(t, parentHash, output.GitResult.ParentHash, "const should preserve incoming parent hash")
	require.Empty(t, output.GitResult.ParentRef, "const hash-mode should stay hash-backed")
	require.Len(t, outputArts, 1, "const should only pass through the prior thin pack")
	require.True(t, outputArts[0] == inputThinPack, "const should pass through the SAME thin pack artifact")
}

func TestWithGitWorkspace_PersistWithDiffs_DiffContent(t *testing.T) {
	t.Parallel()

	// Test that the diff artifacts contain expected content
	reg := ActivityRegistration{
		Step: recipeops.TaskStep{
			Invoke: func(deps recipeops.OpDependencies, ctx context.Context, input map[string]interface{}) (map[string]interface{}, error) {
				// Make a specific change so we can verify the diff content
				worktreePath := deps.WorktreePath()
				cellDir := filepath.Join(worktreePath, "cells", "test")
				require.NoError(t, os.MkdirAll(cellDir, 0o755))

				testFilePath := filepath.Join(cellDir, "test_diff.txt")
				err := os.WriteFile(testFilePath, []byte("Line 1\nLine 2\nLine 3\n"), 0o644)
				require.NoError(t, err)

				return map[string]interface{}{"result": "added file"}, nil
			},
		},
	}

	baseRepo, baseHash, cleanup := setupGitRepo(t)
	defer cleanup()

	controller := gitstate.NewController(nil)
	wrapped := opExecutor{deps: recipeops.NewServiceDepsBuilder().Build(), reg: reg, controller: controller}.do
	req := ActivityInvocationRequest{
		Input: map[string]interface{}{},
		GitTaskContext: gitstate.GlobalGitTaskContext{
			BaseRepo: baseRepo,
			BaseRef:  baseHash, CellName: "cells/test",
			CellPath: "cells/test",
		},
	}

	jobKey := swf.JobKey{TenantId: "test", JobId: "job-15"}
	output, outputArts, err := wrapped(context.Background(), &jt{jobKey}, req, nil)

	// Verify success
	require.NoError(t, err)
	// First commit: parent == base, so only 2 artifacts
	require.Len(t, outputArts, 2)

	// Read the diff from parent artifact and verify content
	diffFromParentReader, err := outputArts[1].Open()
	require.NoError(t, err)
	diffFromParentBytes, err := io.ReadAll(diffFromParentReader)
	require.NoError(t, err)
	diffFromParentReader.Close()

	diffFromParentStr := string(diffFromParentBytes)
	t.Logf("Diff from parent:\n%s", diffFromParentStr)

	// Verify the diff contains expected patterns
	assert.Contains(t, diffFromParentStr, "test_diff.txt", "diff should mention the file")
	assert.Contains(t, diffFromParentStr, "diff --git", "diff should be in git diff format")
	assert.Contains(t, diffFromParentStr, "Line 1", "diff should contain file content")

	// Note: No diff from base since parent == base in this first commit scenario

	// Verify persist hash was set
	require.NotEmpty(t, output.GitResult.PersistHash)
}

func TestReplaceSentinelValue_HandlesInputMap(t *testing.T) {
	testPath := "/test/worktree/path"
	testInbox := "/test/inbox/path"
	testOutbox := "/test/outbox/path"
	replacements := map[string]string{
		contextual.WorktreePathSentinel:   testPath,
		contextual.ArtifactInboxSentinel:  testInbox,
		contextual.ArtifactOutboxSentinel: testOutbox,
	}

	t.Run("map[string]interface{} with sentinel", func(t *testing.T) {
		input := map[string]interface{}{
			"path":   contextual.WorktreePathSentinel,
			"other":  "value",
			"nested": map[string]interface{}{"inner": contextual.WorktreePathSentinel},
		}
		result := replaceSentinels(input, replacements)
		assert.Equal(t, testPath, result["path"])
		assert.Equal(t, "value", result["other"])
		nested := result["nested"].(map[string]interface{})
		assert.Equal(t, testPath, nested["inner"])
	})

	t.Run("recipe.InputMap with sentinel", func(t *testing.T) {
		input := map[string]interface{}{
			"path":   contextual.WorktreePathSentinel,
			"other":  "value",
			"nested": map[string]interface{}{"inner": contextual.WorktreePathSentinel},
		}
		result := replaceSentinelValue(input, replacements)
		resultMap := result.(map[string]interface{})
		assert.Equal(t, testPath, resultMap["path"])
		assert.Equal(t, "value", resultMap["other"])
		nested := resultMap["nested"].(map[string]interface{})
		assert.Equal(t, testPath, nested["inner"])
	})

	t.Run("array with sentinels", func(t *testing.T) {
		input := []interface{}{
			contextual.WorktreePathSentinel,
			"normal",
			map[string]interface{}{"key": contextual.WorktreePathSentinel},
			map[string]interface{}{"key": contextual.WorktreePathSentinel},
		}
		result := replaceSentinelValue(input, replacements)
		resultArr := result.([]interface{})
		assert.Equal(t, testPath, resultArr[0])
		assert.Equal(t, "normal", resultArr[1])
		assert.Equal(t, testPath, resultArr[2].(map[string]interface{})["key"])
		assert.Equal(t, testPath, resultArr[3].(map[string]interface{})["key"])
	})

	t.Run("sentinel embedded in string is replaced", func(t *testing.T) {
		input := map[string]interface{}{
			"run": contextual.ArtifactInboxSentinel + "/input.txt",
		}
		result := replaceSentinels(input, replacements)
		assert.Equal(t, testInbox+"/input.txt", result["run"])
	})

	t.Run("multiple sentinels in one string are replaced", func(t *testing.T) {
		input := map[string]interface{}{
			"run": "cp " + contextual.ArtifactInboxSentinel + "/a.txt " + contextual.ArtifactOutboxSentinel + "/b.txt",
		}
		result := replaceSentinels(input, replacements)
		assert.Equal(t, "cp "+testInbox+"/a.txt "+testOutbox+"/b.txt", result["run"])
	})

	t.Run("embedded sentinel replacement works in nested arrays and maps", func(t *testing.T) {
		input := map[string]interface{}{
			"items": []interface{}{
				map[string]interface{}{
					"path": contextual.ArtifactInboxSentinel + "/nested.txt",
				},
			},
		}
		result := replaceSentinels(input, replacements)
		items := result["items"].([]interface{})
		first := items[0].(map[string]interface{})
		assert.Equal(t, testInbox+"/nested.txt", first["path"])
	})

	t.Run("non-sentinel values unchanged", func(t *testing.T) {
		input := map[string]interface{}{
			"string": "hello",
			"number": 42,
			"bool":   true,
			"nil":    nil,
		}
		result := replaceSentinels(input, replacements)
		assert.Equal(t, "hello", result["string"])
		assert.Equal(t, 42, result["number"])
		assert.Equal(t, true, result["bool"])
		assert.Nil(t, result["nil"])
	})
}
