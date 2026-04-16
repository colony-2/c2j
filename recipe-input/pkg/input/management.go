package input

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"time"

	"github.com/colony-2/c2j/recipe-core/pkg/ops"
	coretask "github.com/colony-2/c2j/recipe-core/pkg/task"
	"github.com/colony-2/c2j/recipe-core/pkg/workflowctl"
	openapi "github.com/colony-2/c2j/recipe-input/pkg/openapi"
	ops2 "github.com/colony-2/c2j/recipe-worker/pkg/ops"
	"github.com/colony-2/swf-go/pkg/swf"
	"github.com/gorilla/mux"
)

// inputManagementService implements ManagementService for input activities
type inputManagementService struct {
	workflowType string
	sse          ops.SSEManager
	ctl          workflowctl.WorkflowControl
	runtime      *Runtime
}

// newInputManagementService creates a new input management service
func newInputManagementService() *inputManagementService {
	return &inputManagementService{
		workflowType: "InputCollectionWorkflow",
	}
}

// Initialize sets up the service with dependencies
func (s *inputManagementService) Initialize(deps ops.ServiceDependencies2) error {
	runtime, err := NewRuntimeFromDeps(deps)
	if err != nil {
		return err
	}
	s.runtime = runtime
	s.ctl = runtime.ctl
	s.sse = runtime.sse
	if s.sse == nil {
		return fmt.Errorf("sse manager must be provided")
	}
	return nil
}

func (s *inputManagementService) Close() {
	// No-op: service depends on external WorkflowControl lifetime
}

// GetRoutes returns the HTTP routes provided by this service
func (s *inputManagementService) GetRoutes() []ops.Route {
	return []ops.Route{
		{Method: "GET", Path: "/api/projects/{projectId}/user-inputs/pending", Handler: s.ListPending},
		{Method: "GET", Path: "/api/projects/{projectId}/user-inputs/stream", Handler: s.SSEStream},
		{Method: "GET", Path: "/api/projects/{projectId}/user-inputs/{jobId}", Handler: s.GetDetails},
		{Method: "POST", Path: "/api/projects/{projectId}/user-inputs/{jobId}/respond", Handler: s.SubmitResponse},
		{Method: "POST", Path: "/api/projects/{projectId}/user-inputs/{jobId}/cancel", Handler: s.Cancel},
	}
}

// ListPending returns all pending input requests
func (s *inputManagementService) ListPending(w http.ResponseWriter, r *http.Request) {
	slog.Info("list_pending: request received", "method", r.Method, "url", r.URL.Path)

	vars := mux.Vars(r)
	projectID := vars["projectId"]
	if projectID == "" {
		slog.Warn("list_pending: missing projectId parameter", "url", r.URL.Path, "vars", vars)
		http.Error(w, "projectId is required", http.StatusBadRequest)
		return
	}

	slog.Info("list_pending: collecting pending inputs", "project_id", projectID)
	pending, err := s.collectPendingInputs(r.Context(), projectID)
	if err != nil {
		slog.Error("list_pending: failed to collect pending inputs",
			"project_id", projectID,
			"error", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	slog.Info("list_pending: success", "project_id", projectID, "count", len(pending))
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(pending); err != nil {
		slog.Error("list_pending: failed to encode response", "project_id", projectID, "error", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (s *inputManagementService) GetDetails(w http.ResponseWriter, r *http.Request) {
	slog.Info("get_details: request received", "method", r.Method, "url", r.URL.Path)

	vars := mux.Vars(r)
	projectID := vars["projectId"]
	if projectID == "" {
		slog.Warn("get_details: missing projectId parameter", "url", r.URL.Path, "vars", vars)
		http.Error(w, "projectId is required", http.StatusBadRequest)
		return
	}

	jobId := vars["jobId"]
	if jobId == "" {
		slog.Warn("get_details: missing jobId parameter", "url", r.URL.Path, "project_id", projectID, "vars", vars)
		http.Error(w, "jobId is required", http.StatusBadRequest)
		return
	}

	slog.Info("get_details: fetching job details", "project_id", projectID, "job_id", jobId)
	res := s.getDetails(r.Context(), projectID, jobId)
	if res.sendError(w) {
		slog.Error("get_details: failed to get details", "project_id", projectID, "job_id", jobId, "error", res.err)
		return
	}
	d := res.value
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(d); err != nil {
		slog.Error("get_details: failed to encode response", "project_id", projectID, "job_id", jobId, "error", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}

}

func (s *inputManagementService) getOutput(ctx context.Context, projectID string, jobId string) (workflowctl.TaskHandle, ops2.ActivityInvocationOutput, []swf.Artifact, error) {
	if s.ctl == nil {
		return nil, ops2.ActivityInvocationOutput{}, nil, errors.New("workflow control unavailable")
	}

	task, err := s.ctl.GetWaitingTask(ctx, swf.JobKey{
		TenantId: projectID,
		JobId:    jobId,
	})

	if err != nil {
		return nil, ops2.ActivityInvocationOutput{}, nil, fmt.Errorf("failed to find job: %w", err)
	}

	td, err := task.Data()
	if err != nil {
		return nil, ops2.ActivityInvocationOutput{}, nil, fmt.Errorf("failed to get task data: %w", err)
	}

	data, err := td.GetData()
	if err != nil {
		return nil, ops2.ActivityInvocationOutput{}, nil, fmt.Errorf("failed to get task data: %w", err)
	}

	artifacts, err := td.GetArtifacts()
	if err != nil {
		return nil, ops2.ActivityInvocationOutput{}, nil, fmt.Errorf("failed to get task artifacts: %w", err)
	}

	// Note: for capability-based waiting tasks, swf-go exposes the cached output chapter
	// data (the last completed step), not the next step's invocation request.
	var env coretask.OutputEnvelope
	if err := json.Unmarshal(data, &env); err != nil {
		return nil, ops2.ActivityInvocationOutput{}, nil, fmt.Errorf("failed to unmarshal task output envelope: %w", err)
	}
	if env.Version != coretask.OutputEnvelopeVersion {
		return nil, ops2.ActivityInvocationOutput{}, nil, fmt.Errorf("unsupported task output envelope version %d", env.Version)
	}
	if env.Kind != coretask.OutputKindActivityInvocationOutput {
		return nil, ops2.ActivityInvocationOutput{}, nil, fmt.Errorf("unexpected task output kind %q", env.Kind)
	}

	var out ops2.ActivityInvocationOutput
	if err := env.DecodePayload(&out); err != nil {
		return nil, ops2.ActivityInvocationOutput{}, nil, fmt.Errorf("failed to decode activity output payload: %w", err)
	}

	return task, out, artifacts, nil
}

// GetDetails returns details about a specific input request
func (s *inputManagementService) getDetails(ctx context.Context, projectID string, jobId string) result[*openapi.UserInputDetails] {
	if s.runtime == nil {
		return result[*openapi.UserInputDetails]{err: "workflow control unavailable"}
	}
	details, err := s.runtime.GetDetails(ctx, projectID, jobId)
	if err != nil {
		if errors.Is(err, ErrInputNotPending) {
			return result[*openapi.UserInputDetails]{err: "not found", status: http.StatusNotFound}
		}
		return result[*openapi.UserInputDetails]{err: err.Error()}
	}
	return result[*openapi.UserInputDetails]{value: details}
}

func (s *inputManagementService) findJob(ctx context.Context, projectID string, jobId string) result[*workflowctl.JobItem] {
	slog.Info("find_job: searching for job", "project_id", projectID, "job_id", jobId)

	if s.ctl == nil {
		return result[*workflowctl.JobItem]{err: "workflow control unavailable"}
	}

	// Query with TenantId filter and specific JobKey, only for jobs waiting for input
	jobs, _, err := s.ctl.ListJobs(ctx, swf.ListJobsRequest{
		Stores:    []swf.JobStore{swf.JobStoreActive},
		TenantIds: []string{projectID},
		JobKeys:   []swf.JobKey{{TenantId: projectID, JobId: jobId}},
		Statuses:  []swf.JobStatus{swf.JobStatusReady},
		JobTasks: []swf.JobTaskFilter{{
			JobType:  "recipe",
			TaskType: "input:collect_user_input",
		}},
	})
	if err != nil {
		slog.Error("find_job: query failed",
			"project_id", projectID,
			"job_id", jobId,
			"error", err)
		return result[*workflowctl.JobItem]{
			err: fmt.Errorf("failed to query workflow: %w", err).Error(),
		}
	}

	if len(jobs) == 0 {
		slog.Warn("find_job: job not found",
			"project_id", projectID,
			"job_id", jobId)
		return result[*workflowctl.JobItem]{
			err:    "not found",
			status: http.StatusNotFound,
		}
	}

	// Validate project ownership
	job := jobs[0]
	if job.JobKey.TenantId != projectID {
		slog.Warn("find_job: project mismatch",
			"project_id", projectID,
			"job_tenant_id", job.JobKey.TenantId,
			"job_id", jobId)
		return result[*workflowctl.JobItem]{
			err:    "workflow not found in project",
			status: http.StatusNotFound,
		}
	}

	slog.Info("find_job: found job", "project_id", projectID, "job_id", jobId)
	return result[*workflowctl.JobItem]{
		value: &job,
	}
}

type result[T any] struct {
	value  T
	err    string
	status int // HTTP Status Code to be used by the handler
}

func (res result[T]) hasError() bool {
	return res.err != ""
}

func (res result[T]) sendError(w http.ResponseWriter) bool {
	if res.err != "" {
		if res.status == 0 {
			res.status = http.StatusInternalServerError
		}
		http.Error(w, res.err, res.status)
		return true
	}
	return false
}

func (s *inputManagementService) SubmitResponse(w http.ResponseWriter, r *http.Request) {
	slog.Info("submit_response: request received", "method", r.Method, "url", r.URL.Path)

	vars := mux.Vars(r)
	projectID := vars["projectId"]
	if projectID == "" {
		slog.Warn("submit_response: missing projectId parameter", "url", r.URL.Path, "vars", vars)
		http.Error(w, "projectId is required", http.StatusBadRequest)
		return
	}

	jobId := vars["jobId"]
	if jobId == "" {
		slog.Warn("submit_response: missing jobId parameter", "url", r.URL.Path, "project_id", projectID, "vars", vars)
		http.Error(w, "jobId is required", http.StatusBadRequest)
		return
	}

	slog.Info("submit_response: decoding request body", "project_id", projectID, "job_id", jobId)
	output := FormResponse{}
	if err := json.NewDecoder(r.Body).Decode(&output); err != nil {
		slog.Warn("submit_response: invalid request body", "project_id", projectID, "job_id", jobId, "error", err)
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	if output.Fields == nil {
		slog.Warn("submit_response: missing fields in request", "project_id", projectID, "job_id", jobId)
		http.Error(w, "fields are required", http.StatusBadRequest)
		return
	}

	userID := ""
	if output.UserId != nil {
		userID = *output.UserId
	}
	if userID == "" {
		userID = r.Header.Get("X-User-ID")
		slog.Info("submit_response: using user ID from header", "project_id", projectID, "job_id", jobId, "user_id", userID)
	} else {
		slog.Info("submit_response: using user ID from body", "project_id", projectID, "job_id", jobId, "user_id", userID)
	}
	if userID != "" {
		output.UserId = &userID
	}

	slog.Info("submit_response: submitting response", "project_id", projectID, "job_id", jobId, "field_count", len(output.Fields))
	res := s.submitResponse(r.Context(), projectID, jobId, output)
	if res.sendError(w) {
		slog.Error("submit_response: failed to submit response", "project_id", projectID, "job_id", jobId, "error", res.err)
		return
	}
	// Return success response
	slog.Info("submit_response: success", "project_id", projectID, "job_id", jobId)
	w.Header().Set("Content-Type", "application/json")
	ok := res.value
	_ = json.NewEncoder(w).Encode(struct {
		Ok *bool `json:"ok,omitempty"`
	}{Ok: &ok})
}

// SubmitResponse handles user form submission
func (s *inputManagementService) submitResponse(ctx context.Context, projectID string, jobId string, output FormResponse) result[bool] {
	slog.Debug("submitResponse: starting", "project_id", projectID, "job_id", jobId)
	if s.runtime == nil {
		return result[bool]{err: "workflow control unavailable"}
	}
	if err := s.runtime.SubmitResponse(ctx, projectID, jobId, output); err != nil {
		return result[bool]{err: err.Error()}
	}
	slog.Info("submitResponse: successfully completed", "project_id", projectID, "job_id", jobId)
	return result[bool]{value: true}
}

// Cancel handles cancellation of a pending input request
func (s *inputManagementService) Cancel(w http.ResponseWriter, r *http.Request) {
	slog.Info("cancel: request received", "method", r.Method, "url", r.URL.Path)

	if s.ctl == nil {
		slog.Error("cancel: workflow control unavailable")
		http.Error(w, "workflow control unavailable", http.StatusInternalServerError)
		return
	}

	vars := mux.Vars(r)
	projectID := vars["projectId"]
	if projectID == "" {
		slog.Warn("cancel: missing projectId parameter", "url", r.URL.Path, "vars", vars)
		http.Error(w, "projectId is required", http.StatusBadRequest)
		return
	}

	jobId := vars["jobId"]
	if jobId == "" {
		slog.Warn("cancel: missing jobId parameter", "url", r.URL.Path, "project_id", projectID, "vars", vars)
		http.Error(w, "jobId is required", http.StatusBadRequest)
		return
	}

	// Parse cancellation request (OpenAPI-generated type)
	var cancelRequest openapi.CancelRequest

	slog.Info("cancel: decoding cancellation request", "project_id", projectID, "job_id", jobId)
	if err := json.NewDecoder(r.Body).Decode(&cancelRequest); err != nil {
		slog.Warn("cancel: invalid request body", "project_id", projectID, "job_id", jobId, "error", err)
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	reason := ""
	if cancelRequest.Reason != nil {
		reason = *cancelRequest.Reason
	}

	slog.Info("cancel: attempting to cancel job", "project_id", projectID, "job_id", jobId, "reason", reason)
	if s.runtime == nil {
		http.Error(w, "workflow control unavailable", http.StatusInternalServerError)
		return
	}
	err := s.runtime.Cancel(r.Context(), projectID, jobId, reason)
	if err != nil {
		slog.Error("cancel: cancel failed",
			"project_id", projectID,
			"job_id", jobId,
			"error", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	slog.Info("cancel: successfully cancelled job", "project_id", projectID, "job_id", jobId)
	// Return success response
	w.Header().Set("Content-Type", "application/json")
	ok := true
	_ = json.NewEncoder(w).Encode(struct {
		Ok *bool `json:"ok,omitempty"`
	}{Ok: &ok})
}

// SSEStream handles Server-Sent Events streaming for real-time updates
func (s *inputManagementService) SSEStream(w http.ResponseWriter, r *http.Request) {
	slog.Info("sse_stream: request received",
		"method", r.Method,
		"url", r.URL.Path,
		"remote_addr", r.RemoteAddr,
		"user_agent", r.Header.Get("User-Agent"))

	vars := mux.Vars(r)
	projectID := vars["projectId"]
	if projectID == "" {
		slog.Warn("sse_stream: missing projectId parameter",
			"url", r.URL.Path,
			"remote_addr", r.RemoteAddr,
			"vars", vars)
		http.Error(w, "projectId is required", http.StatusBadRequest)
		return
	}

	slog.Info("sse_stream: validated projectId", "project_id", projectID)

	// Set headers for SSE
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	slog.Info("sse_stream: set SSE headers", "project_id", projectID)

	// Create a client ID
	clientID := r.Header.Get("X-Client-ID")
	if clientID == "" {
		clientID = generateClientID()
		slog.Info("sse_stream: generated client ID", "project_id", projectID, "client_id", clientID)
	} else {
		slog.Info("sse_stream: using provided client ID", "project_id", projectID, "client_id", clientID)
	}

	// Subscribe to events if SSE manager is available
	if s.sse == nil {
		slog.Error("sse_stream: SSE manager not configured", "project_id", projectID, "client_id", clientID)
		// No SSE manager, just send a heartbeat and close
		fmt.Fprintf(w, "event: error\ndata: {\"error\": \"SSE not configured\"}\n\n")
		return
	}

	slog.Info("sse_stream: subscribing to events", "project_id", projectID, "client_id", clientID)
	events := s.sse.Subscribe(clientID)
	defer func() {
		slog.Info("sse_stream: unsubscribing client", "project_id", projectID, "client_id", clientID)
		s.sse.Unsubscribe(clientID)
	}()

	// Send initial connection event
	slog.Info("sse_stream: sending connection event", "project_id", projectID, "client_id", clientID)
	fmt.Fprintf(w, "event: connected\ndata: {\"client_id\": \"%s\"}\n\n", clientID)
	flush(w)

	sentPending := make(map[string]struct{})

	// Emit snapshot of current pending inputs so subscribers have immediate context.
	slog.Info("sse_stream: collecting pending inputs snapshot", "project_id", projectID, "client_id", clientID)
	if pending, err := s.collectPendingInputs(r.Context(), projectID); err != nil {
		slog.Error("sse_stream: failed to collect pending inputs snapshot",
			"project_id", projectID,
			"client_id", clientID,
			"error", err)
		fmt.Fprintf(w, "event: error\ndata: {\"error\": \"failed to load pending inputs\"}\n\n")
		flush(w)
	} else {
		slog.Info("sse_stream: sending pending inputs snapshot",
			"project_id", projectID,
			"client_id", clientID,
			"count", len(pending))
		for _, item := range pending {
			payload, err := json.Marshal(item)
			if err != nil {
				slog.Error("sse_stream: failed to marshal pending input",
					"project_id", projectID,
					"client_id", clientID,
					"job_id", item.Id,
					"error", err)
				continue
			}
			fmt.Fprintf(w, "event: input_pending\ndata: %s\n\n", payload)
			sentPending[item.Id] = struct{}{}
		}
		flush(w)
		slog.Info("sse_stream: finished sending pending inputs", "project_id", projectID, "client_id", clientID)
	}

	// Create a ticker for heartbeat
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	pendingInterval := 1 * time.Second
	pendingTimer := time.NewTimer(pendingInterval)
	defer pendingTimer.Stop()
	lastNewPendingAt := time.Now()

	slog.Info("sse_stream: entering event loop", "project_id", projectID, "client_id", clientID)

	// Stream events
	for {
		select {
		case <-r.Context().Done():
			// Client disconnected
			slog.Info("sse_stream: client disconnected",
				"project_id", projectID,
				"client_id", clientID,
				"context_error", r.Context().Err())
			return

		case event := <-events:
			// Send event to client
			slog.Info("sse_stream: sending event to client",
				"project_id", projectID,
				"client_id", clientID,
				"event_type", event.Type)
			data, _ := json.Marshal(event.Data)
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event.Type, string(data))
			flush(w)

		case <-pendingTimer.C:
			newPending := false
			pending, err := s.collectPendingInputs(r.Context(), projectID)
			if err != nil {
				slog.Warn("sse_stream: pending input poll failed",
					"project_id", projectID,
					"client_id", clientID,
					"error", err)
			} else {
				for _, item := range pending {
					if _, ok := sentPending[item.Id]; ok {
						continue
					}
					payload, err := json.Marshal(item)
					if err != nil {
						slog.Error("sse_stream: failed to marshal polled pending input",
							"project_id", projectID,
							"client_id", clientID,
							"job_id", item.Id,
							"error", err)
						continue
					}
					fmt.Fprintf(w, "event: input_pending\ndata: %s\n\n", payload)
					sentPending[item.Id] = struct{}{}
					newPending = true
					flush(w)
				}
			}

			if newPending {
				lastNewPendingAt = time.Now()
				pendingInterval = 1 * time.Second
			} else if time.Since(lastNewPendingAt) >= 30*time.Minute {
				pendingInterval = 1 * time.Minute
			}
			pendingTimer.Reset(pendingInterval)

		case <-ticker.C:
			// Send heartbeat
			fmt.Fprintf(w, "event: heartbeat\ndata: {\"timestamp\": \"%s\"}\n\n", time.Now().Format(time.RFC3339))
			flush(w)
		}
	}
}

const pendingStatusQuery = "InputStatus = \"pending\""

func (s *inputManagementService) collectPendingInputs(ctx context.Context, projectID string) ([]PendingInput, error) {
	if s.runtime == nil {
		return nil, fmt.Errorf("workflow control unavailable")
	}
	return s.runtime.ListPendingInputs(ctx, projectID)
}

func toOpenAPIInputFormConfig(form InputForm) openapi.InputFormConfig {
	out := openapi.InputFormConfig{}

	if form.Question != "" {
		out.Question = &form.Question
	}
	if string(form.Type) != "" {
		t := openapi.FieldType(form.Type)
		out.Type = &t
	}
	if len(form.Options) > 0 {
		opts := make([]openapi.Option, 0, len(form.Options))
		for _, opt := range form.Options {
			var label *string
			if opt.Label != "" {
				v := opt.Label
				label = &v
			}
			opts = append(opts, openapi.Option{
				Value: opt.Value,
				Label: label,
			})
		}
		out.Options = &opts
	}
	if form.Scale != nil {
		scale := openapi.LinearScale{
			Min: form.Scale.Min,
			Max: form.Scale.Max,
		}
		if form.Scale.MinLabel != "" {
			v := form.Scale.MinLabel
			scale.MinLabel = &v
		}
		if form.Scale.MaxLabel != "" {
			v := form.Scale.MaxLabel
			scale.MaxLabel = &v
		}
		out.Scale = &scale
	}

	if form.Title != "" {
		out.Title = &form.Title
	}
	if len(form.Fields) > 0 {
		fields := make([]openapi.FormField, 0, len(form.Fields))
		for _, f := range form.Fields {
			fields = append(fields, toOpenAPIFormField(f))
		}
		out.Fields = &fields
	}

	if ctx, ok := toOpenAPIFormContext(form.Context); ok {
		out.Context = &ctx
	}

	if form.Timeout > 0 {
		secs := int(form.Timeout / time.Second)
		out.Timeout = &secs
	}

	return out
}

func toOpenAPIFormField(field FormField) openapi.FormField {
	out := openapi.FormField{
		Id:       field.ID,
		Question: field.Question,
		Type:     openapi.FieldType(field.Type),
	}

	if field.Placeholder != "" {
		out.Placeholder = &field.Placeholder
	}
	if field.Required {
		v := true
		out.Required = &v
	}

	if len(field.Options) > 0 {
		opts := make([]openapi.Option, 0, len(field.Options))
		for _, opt := range field.Options {
			var label *string
			if opt.Label != "" {
				v := opt.Label
				label = &v
			}
			opts = append(opts, openapi.Option{
				Value: opt.Value,
				Label: label,
			})
		}
		out.Options = &opts
	}
	if field.Scale != nil {
		scale := openapi.LinearScale{
			Min: field.Scale.Min,
			Max: field.Scale.Max,
		}
		if field.Scale.MinLabel != "" {
			v := field.Scale.MinLabel
			scale.MinLabel = &v
		}
		if field.Scale.MaxLabel != "" {
			v := field.Scale.MaxLabel
			scale.MaxLabel = &v
		}
		out.Scale = &scale
	}

	if fv, ok := toOpenAPIFieldValidation(field.Validation); ok {
		out.Validation = &fv
	}

	return out
}

func toOpenAPIFieldValidation(v FieldValidation) (openapi.FieldValidation, bool) {
	out := openapi.FieldValidation{}
	used := false

	if v.MinLength != 0 {
		out.MinLength = &v.MinLength
		used = true
	}
	if v.MaxLength != 0 {
		out.MaxLength = &v.MaxLength
		used = true
	}
	if v.Pattern != "" {
		out.Pattern = &v.Pattern
		used = true
	}
	if v.Min != 0 {
		out.Min = &v.Min
		used = true
	}
	if v.Max != 0 {
		out.Max = &v.Max
		used = true
	}

	return out, used
}

func toOpenAPIFormContext(ctx FormContext) (openapi.FormContext, bool) {
	out := openapi.FormContext{}
	used := false

	if len(ctx.Artifacts) > 0 {
		arts := make([]openapi.Artifact, 0, len(ctx.Artifacts))
		for _, a := range ctx.Artifacts {
			arts = append(arts, openapi.Artifact{Path: a.Path})
		}
		out.Artifacts = &arts
		used = true
	}
	if ctx.ArtifactsFromOutput != "" {
		v := ctx.ArtifactsFromOutput
		out.ArtifactsFromOutput = &v
		used = true
	}
	if len(ctx.ArtifactsGlob) > 0 {
		globs := make([]openapi.GlobPattern, 0, len(ctx.ArtifactsGlob))
		for _, g := range ctx.ArtifactsGlob {
			globs = append(globs, openapi.GlobPattern{Pattern: g.Pattern})
		}
		out.ArtifactsGlob = &globs
		used = true
	}

	return out, used
}

func stringValue(val any) string {
	switch v := val.(type) {
	case string:
		return v
	case fmt.Stringer:
		return v.String()
	case []byte:
		return string(v)
	case float32, float64, int, int32, int64, uint, uint32, uint64, bool:
		return fmt.Sprintf("%v", v)
	case time.Time:
		return v.UTC().Format(time.RFC3339)
	}
	return fmt.Sprintf("%v", val)
}

func timeAttr(attrs map[string]any, key string) string {
	val, ok := attrs[key]
	if !ok || val == nil {
		return ""
	}
	switch v := val.(type) {
	case time.Time:
		return v.UTC().Format(time.RFC3339)
	case string:
		if t, ok := parseRFC3339(v); ok {
			return t.UTC().Format(time.RFC3339)
		}
		return v
	default:
		return stringValue(val)
	}
}

func parseRFC3339(value string) (time.Time, bool) {
	if value == "" {
		return time.Time{}, false
	}
	if t, err := time.Parse(time.RFC3339, value); err == nil {
		return t, true
	}
	if t, err := time.Parse(time.RFC3339Nano, value); err == nil {
		return t, true
	}
	return time.Time{}, false
}

func flush(w http.ResponseWriter) {
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

// generateClientID generates a unique client ID
func generateClientID() string {
	return fmt.Sprintf("client-%d", time.Now().UnixNano())
}

// sortedMapKeys returns sorted keys of the map for stable logging
func sortedMapKeys(m map[string]interface{}) []string {
	if m == nil {
		return nil
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
