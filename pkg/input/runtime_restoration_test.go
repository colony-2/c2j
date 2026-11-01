package input

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	recipeartifacts "github.com/colony-2/c2j/pkg/artifacts"
	"github.com/colony-2/c2j/pkg/contextual"
	coretask "github.com/colony-2/c2j/pkg/task"
	workerops "github.com/colony-2/c2j/pkg/worker/ops"
	"github.com/colony-2/c2j/pkg/workflowctl"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/gorilla/mux"
	"github.com/stretchr/testify/require"
)

// These tests use the production encoder, not a hand-built offload envelope.
func restorationTask(t *testing.T, form InputForm) (*clientTask, *Runtime) {
	t.Helper()
	value, err := jsonValue(form)
	require.NoError(t, err)
	req := workerops.ActivityInvocationRequest{Input: value.(map[string]any), Artifacts: map[string]recipeartifacts.Ref{"application": recipeartifacts.NewExternalRef("application", "https://example.invalid/document", false)}}
	req.GitTaskContext.Workspace = &contextual.WorkspaceContext{ScopeID: "workspace"}
	req.GitTaskContext.ParentHash = "parent"
	data, err := workerops.NewInvocationTaskData(req)
	require.NoError(t, err)
	task := &clientTask{key: jobdb.JobKey{TenantId: "tenant", JobId: "input"}, ordinal: 7, data: data}
	ctl := &clientControl{waiting: func(jobdb.JobKey) (workflowctl.TaskHandle, error) { return task, nil }, list: func(jobdb.ListJobsRequest) ([]workflowctl.JobItem, string, error) {
		return []workflowctl.JobItem{taskListing(task)}, "next", nil
	}, artifact: func(string, jobdb.ArtifactKey) jobdb.Artifact {
		t.Fatal("application bytes loaded during form read")
		return nil
	}}
	runtime, err := NewRuntime(ctl, nil)
	require.NoError(t, err)
	return task, runtime
}

func restorationForm(kind string, size int) InputForm {
	form := InputForm{Question: strings.Repeat("<", size), RequestID: "frozen", RequestedAt: "2026-11-01T01:02:03Z"}
	if kind != "ordinary" {
		form.Question = ""
		form.Request = map[string]any{"text": strings.Repeat("<", size)}
		form.ResponseSchema = structuredConfig().ResponseSchema
	}
	if kind == "review" {
		form.Kind = "review"
		form.ResponseSchema = nil
		form.Request = nil
		form.Title = strings.Repeat("<", size)
		form.Fields = reviewConfig().Fields
		form.Documents = map[string]recipeartifacts.Ref{"document": recipeartifacts.NewStoredRef(jobdb.ArtifactKey{JobId: "producer", TaskOrdinal: 1, Name: "document.md", SizeBytes: 4})}
	}
	return form
}

func TestRuntimeRestoresPendingInput(t *testing.T) {
	for _, kind := range []string{"ordinary", "structured", "review"} {
		for _, size := range []int{1024, 65536} {
			t.Run(fmt.Sprintf("%s/%d", kind, size), func(t *testing.T) {
				form := restorationForm(kind, size)
				task, runtime := restorationTask(t, form)
				raw, err := task.data.GetData()
				require.NoError(t, err)
				var envelope workerops.ActivityInvocationRequest
				require.NoError(t, json.Unmarshal(raw, &envelope))
				require.Equal(t, size == 65536, envelope.InputArtifact != "")
				before, err := task.data.GetArtifacts()
				require.NoError(t, err)
				for i := 0; i < 2; i++ {
					got, err := runtime.GetForm(context.Background(), "tenant", "input")
					require.NoError(t, err)
					require.Equal(t, form, got)
					details, err := runtime.GetDetails(context.Background(), "tenant", "input")
					require.NoError(t, err)
					require.Equal(t, toOpenAPIInputFormConfig(form), details.Form)
					page, err := runtime.ListPendingInputsPage(context.Background(), "tenant", PendingInputOptions{})
					require.NoError(t, err)
					require.Equal(t, "next", page.NextPageToken)
					require.Equal(t, []PendingInputOccurrence{{JobID: "input", TaskOrdinal: 7, RequestID: form.RequestID, RequestedAt: form.RequestedAt}}, page.Inputs)
					_, output, artifacts, err := runtime.getOutput(context.Background(), "tenant", "input")
					require.NoError(t, err)
					require.Equal(t, "workspace", output.WorkspaceScopeID)
					require.Equal(t, "parent", output.GitResult.ParentHash)
					require.Contains(t, output.ArtifactRefs, "application")
					require.Empty(t, artifacts)
				}
				after, err := task.data.GetData()
				require.NoError(t, err)
				require.Equal(t, raw, after)
				afterArtifacts, err := task.data.GetArtifacts()
				require.NoError(t, err)
				require.Equal(t, before, afterArtifacts)
				service := &inputManagementService{ctl: runtime.ctl}
				_, output, _, err := service.getOutput(context.Background(), "tenant", "input")
				require.NoError(t, err)
				require.NotEmpty(t, output.OpOutput)
				service.runtime = runtime
				response := httptest.NewRecorder()
				service.GetDetails(response, mux.SetURLVars(httptest.NewRequest(http.MethodGet, "/", nil), map[string]string{"projectId": "tenant", "jobId": "input"}))
				require.Equal(t, http.StatusOK, response.Code)
				expected, err := json.Marshal(toOpenAPIInputFormConfig(form))
				require.NoError(t, err)
				var body struct {
					Form json.RawMessage `json:"form"`
				}
				require.NoError(t, json.Unmarshal(response.Body.Bytes(), &body))
				require.JSONEq(t, string(expected), string(body.Form))
				finishes := 0
				task.finish = func(jobdb.TaskData) error { finishes++; return nil }
				response = httptest.NewRecorder()
				service.SubmitResponse(response, mux.SetURLVars(httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"fields":{}}`)), map[string]string{"projectId": "tenant", "jobId": "input"}))
				if kind == "ordinary" {
					require.Equal(t, http.StatusOK, response.Code)
					require.Equal(t, 1, finishes)
				} else {
					require.NotEqual(t, http.StatusOK, response.Code)
					require.Zero(t, finishes)
				}
			})
		}
	}
}

type restorationArtifact struct {
	jobdb.Artifact
	read func(context.Context) ([]byte, error)
}

func (a restorationArtifact) Bytes(ctx context.Context) ([]byte, error) { return a.read(ctx) }

func TestPendingInputRestorationFailure(t *testing.T) {
	failure := errors.New("lazy artifact read failed")
	for _, mode := range []string{"missing", "unreadable", "malformed", "canceled", "deadline"} {
		t.Run(mode, func(t *testing.T) {
			task, runtime := restorationTask(t, restorationForm("structured", 65536))
			raw, err := task.data.GetData()
			require.NoError(t, err)
			attachments, err := task.data.GetArtifacts()
			require.NoError(t, err)
			require.Len(t, attachments, 1)
			cause := failure
			switch mode {
			case "missing":
				attachments = nil
			case "malformed":
				attachments = []jobdb.Artifact{jobdb.NewArtifactFromBytes(attachments[0].Name(), []byte("{"))}
			default:
				if mode == "canceled" {
					cause = context.Canceled
				}
				if mode == "deadline" {
					cause = context.DeadlineExceeded
				}
				attachments = []jobdb.Artifact{restorationArtifact{Artifact: attachments[0], read: func(ctx context.Context) ([]byte, error) {
					if ctx.Err() != nil {
						return nil, ctx.Err()
					}
					return nil, cause
				}}}
			}
			task.data = &jobdb.SimpleTaskData{Data: raw, Artifacts: attachments}
			runtime.sse = NewSimpleSSEManager()
			events := runtime.sse.Subscribe("test")
			defer runtime.sse.Unsubscribe("test")
			finishes := 0
			task.finish = func(jobdb.TaskData) error { finishes++; return nil }
			ctx := context.Background()
			if mode == "canceled" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			if mode == "deadline" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithDeadline(ctx, time.Now().Add(-time.Second))
				defer cancel()
			}
			for _, read := range []func() error{
				func() error { _, err := runtime.GetForm(ctx, "tenant", "input"); return err },
				func() error { _, err := runtime.GetDetails(ctx, "tenant", "input"); return err },
				func() error {
					_, err := runtime.ListPendingInputsPage(ctx, "tenant", PendingInputOptions{})
					return err
				},
				func() error {
					_, err := runtime.SubmitStructuredResponse(ctx, "tenant", "input", StructuredSubmission{}, Actor{})
					return err
				},
				func() error { return runtime.SubmitResponse(ctx, "tenant", "input", FormResponse{}) },
				func() error {
					_, _, _, err := (&inputManagementService{ctl: runtime.ctl}).getOutput(ctx, "tenant", "input")
					return err
				},
			} {
				err := read()
				require.Error(t, err)
				if mode == "canceled" || mode == "deadline" {
					require.ErrorIs(t, err, cause)
				} else {
					require.ErrorIs(t, err, ErrStorage)
					var runtimeErr *RuntimeError
					require.ErrorAs(t, err, &runtimeErr)
					require.Equal(t, "restore pending input", runtimeErr.Operation)
					if mode == "unreadable" {
						require.ErrorIs(t, err, failure)
					}
					if mode == "malformed" {
						var syntax *json.SyntaxError
						require.ErrorAs(t, err, &syntax)
					}
				}
			}
			require.Zero(t, finishes)
			select {
			case event := <-events:
				t.Fatalf("unexpected completion: %v", event)
			default:
			}
		})
	}
	for _, service := range []*inputManagementService{{}, {runtime: &Runtime{}}} {
		_, _, _, err := service.getOutput(context.Background(), "tenant", "input")
		require.Error(t, err)
	}
}

func TestRestoredStructuredSubmissionBoundaries(t *testing.T) {
	task, runtime := restorationTask(t, restorationForm("structured", 65536))
	raw, err := task.data.GetData()
	require.NoError(t, err)
	artifacts, err := task.data.GetArtifacts()
	require.NoError(t, err)
	task.data = &jobdb.SimpleTaskData{Data: raw, Artifacts: append(artifacts, jobdb.NewArtifactFromBytes("unrelated.txt", []byte("retain")))}
	runtime.sse = NewSimpleSSEManager()
	events := runtime.sse.Subscribe("test")
	defer runtime.sse.Unsubscribe("test")
	finishes := 0
	task.finish = func(data jobdb.TaskData) error {
		finishes++
		raw, err := data.GetData()
		require.NoError(t, err)
		var envelope coretask.OutputEnvelope
		require.NoError(t, json.Unmarshal(raw, &envelope))
		var out workerops.ActivityInvocationOutput
		require.NoError(t, envelope.DecodePayload(&out))
		require.Equal(t, "workspace", out.WorkspaceScopeID)
		require.Equal(t, "parent", out.GitResult.ParentHash)
		require.Contains(t, out.ArtifactRefs, "application")
		require.NotContains(t, out.ArtifactRefs, "c2j-invocation-input.json")
		var accepted Output
		require.NoError(t, json.Unmarshal(mustJSON(t, out.OpOutput), &accepted))
		require.Equal(t, "frozen", accepted.Receipt.RequestID)
		require.Equal(t, "submission", accepted.Receipt.SubmissionID)
		require.Equal(t, Actor{ID: "trusted", Kind: "human"}, accepted.Receipt.Actor)
		artifacts, err := data.GetArtifacts()
		require.NoError(t, err)
		require.Len(t, artifacts, 1)
		require.Equal(t, "unrelated.txt", artifacts[0].Name())
		bytes, err := artifacts[0].Bytes(context.Background())
		require.NoError(t, err)
		require.Equal(t, []byte("retain"), bytes)
		return nil
	}
	valid := StructuredSubmission{RequestID: "frozen", SubmissionID: "submission", Response: map[string]any{"decision": "approve"}}
	actor := Actor{ID: "trusted", Kind: "human"}
	for _, tc := range []struct {
		sub   StructuredSubmission
		actor Actor
	}{
		{StructuredSubmission{RequestID: "wrong", SubmissionID: "submission", Response: valid.Response}, actor},
		{StructuredSubmission{RequestID: "frozen", SubmissionID: "submission", Response: map[string]any{"decision": "wrong"}}, actor},
		{valid, Actor{Kind: "human"}}, {valid, Actor{ID: "trusted"}},
	} {
		_, err := runtime.SubmitStructuredResponse(context.Background(), "tenant", "input", tc.sub, tc.actor)
		require.Error(t, err)
		require.Zero(t, finishes)
	}
	require.Error(t, runtime.SubmitResponse(context.Background(), "tenant", "input", FormResponse{}))
	require.Zero(t, finishes)
	_, err = runtime.SubmitStructuredResponse(context.Background(), "tenant", "input", valid, actor)
	require.NoError(t, err)
	require.Equal(t, 1, finishes)
	select {
	case event := <-events:
		require.Equal(t, "input_completed", event.Type)
	default:
		t.Fatal("missing completion event")
	}
}
func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	raw, err := json.Marshal(v)
	require.NoError(t, err)
	return raw
}

type restorationWrongTypeTask struct{ *clientTask }

func (*restorationWrongTypeTask) TaskType() string { return "other:task" }

func TestRestorationRetainsWaitingTaskValidation(t *testing.T) {
	task, runtime := restorationTask(t, restorationForm("structured", 65536))
	finishes := 0
	task.finish = func(jobdb.TaskData) error { finishes++; return nil }
	for _, key := range []jobdb.JobKey{{TenantId: "other", JobId: "input"}, {TenantId: "tenant", JobId: "other"}} {
		_, err := runtime.GetForm(context.Background(), key.TenantId, key.JobId)
		require.ErrorIs(t, err, ErrInputNotPending)
	}
	ctl := runtime.ctl.(*clientControl)
	for _, wrong := range []workflowctl.TaskHandle{nil, &restorationWrongTypeTask{task}} {
		ctl.waiting = func(jobdb.JobKey) (workflowctl.TaskHandle, error) { return wrong, nil }
		_, err := runtime.SubmitStructuredResponse(context.Background(), "tenant", "input", StructuredSubmission{}, Actor{})
		require.ErrorIs(t, err, ErrInputNotPending)
	}
	require.Zero(t, finishes)
}

func TestRestoredInputCompletionConflicts(t *testing.T) {
	for _, mode := range []string{"completed", "replaced", "still_waiting", "storage"} {
		t.Run(mode, func(t *testing.T) {
			task, runtime := restorationTask(t, restorationForm("structured", 65536))
			storage := errors.New("finish storage failure")
			ctl := runtime.ctl.(*clientControl)
			finishes := 0
			task.finish = func(jobdb.TaskData) error {
				finishes++
				switch mode {
				case "completed":
					ctl.waiting = func(jobdb.JobKey) (workflowctl.TaskHandle, error) { return nil, jobdb.ErrJobNotFound }
				case "replaced":
					replacement := *task
					replacement.ordinal++
					ctl.waiting = func(jobdb.JobKey) (workflowctl.TaskHandle, error) { return &replacement, nil }
				case "storage":
					return storage
				}
				return jobdb.ErrConflict
			}
			_, err := runtime.SubmitStructuredResponse(context.Background(), "tenant", "input", StructuredSubmission{RequestID: "frozen", SubmissionID: "submission", Response: map[string]any{"decision": "approve"}}, Actor{ID: "trusted", Kind: "human"})
			require.Equal(t, 1, finishes)
			expected := ErrStorage
			if mode == "completed" {
				expected = ErrInputNotPending
			}
			if mode == "replaced" {
				expected = ErrStaleRequest
			}
			require.ErrorIs(t, err, expected)
			if mode == "storage" {
				require.ErrorIs(t, err, storage)
			} else {
				require.ErrorIs(t, err, jobdb.ErrConflict)
			}
		})
	}
}
