package input

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	coreops "github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/c2j/pkg/recipe"
	"github.com/colony-2/c2j/pkg/starter"
	"github.com/colony-2/c2j/pkg/worker/compiler"
	"github.com/colony-2/c2j/pkg/worker/ops"
	"github.com/colony-2/c2j/pkg/worker/workflow"
	"github.com/colony-2/c2j/pkg/workflowctl"
	"github.com/gorilla/mux"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestHTTPHandlersWithMuxRouter tests the HTTP handlers with gorilla/mux router
// to ensure URL parameters are extracted correctly
func TestHTTPHandlersWithMuxRouter(t *testing.T) {
	op := GetOp()
	mgmtService := op.GetManagementService().(*inputManagementService)
	coreops.Register(op)

	recipeYaml := `
---
id: test-recipe
op: input
inputs:
  form:
    question: "What is your name?"
`

	testRecipe, err := recipe.LoadRecipeFromString([]byte(recipeYaml))
	require.NoError(t, err)

	registry, err := ops.NewActivityRegistry()
	require.NoError(t, err)
	g := gen{max: 1}
	eng := newToyEngine(t, "test-project", g.Generate)

	wf := workflow.SWFWorkflowControl{
		Engine: eng,
	}
	deps := coreops.NewServiceDepsBuilder().
		WithWorkflowControl(&wf).
		WithSSEManager(NewSimpleSSEManager()).
		Build()
	require.NoError(t, mgmtService.Initialize(deps))

	workSet, err := compiler.NewRecipeWorker(deps, registry, nil)
	require.NoError(t, eng.RegisterWorkers(workSet))
	jobCtx, gitCtx := compiler.GenerateTestContext()

	job := workflowctl.StartJob{
		TenantId:   "test-project",
		RecipeName: testRecipe.GetMetadata().ID,
		Inputs:     map[string]interface{}{},
		JobContext: jobCtx,
		GitRef:     gitCtx.ParentRef,
	}

	_, err = starter.StartRecipeJob(context.Background(), job, eng, *testRecipe)
	require.NoError(t, err)

	// Wait for job to reach Ready status
	var inputs []PendingInput
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		inputs, err = mgmtService.collectPendingInputs(context.Background(), "test-project")
		assert.NoError(c, err)
		assert.Len(c, inputs, 1)
	}, 20*time.Second, 100*time.Millisecond, "Expected 1 pending input after waiting")
	require.Len(t, inputs, 1)

	// Setup router (simulating how the API server does it)
	router := mux.NewRouter()
	for _, route := range mgmtService.GetRoutes() {
		// Simulate the setup in opssetup.SetupOps which strips /api prefix
		path := route.Path
		if len(path) > 4 && path[:4] == "/api" {
			path = path[4:]
		}
		router.HandleFunc(path, route.Handler).Methods(route.Method)
	}

	// Test ListPending endpoint
	t.Run("ListPending", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/projects/test-project/user-inputs/pending", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		require.Equal(t, http.StatusOK, w.Code, "Expected 200 OK, got: %s", w.Body.String())

		var pending []PendingInput
		err := json.NewDecoder(w.Body).Decode(&pending)
		require.NoError(t, err)
		require.Equal(t, 1, len(pending))
	})

	// Get the job ID from pending inputs
	jobID := inputs[0].Id

	// Test GetDetails endpoint
	t.Run("GetDetails", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/projects/test-project/user-inputs/"+jobID, nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		require.Equal(t, http.StatusOK, w.Code, "Expected 200 OK, got: %s", w.Body.String())

		var details map[string]interface{}
		err := json.NewDecoder(w.Body).Decode(&details)
		require.NoError(t, err)
		require.NotNil(t, details)
	})

	// Test SubmitResponse endpoint
	t.Run("SubmitResponse", func(t *testing.T) {
		hash := "test-hash"
		response := FormResponse{
			Fields: map[string]interface{}{
				"response": "John Doe",
			},
			Hash: &hash,
		}
		body, _ := json.Marshal(response)
		req := httptest.NewRequest("POST", "/projects/test-project/user-inputs/"+jobID+"/respond", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		require.Equal(t, http.StatusOK, w.Code, "Expected 200 OK, got: %s", w.Body.String())
	})

	// Test SSEStream endpoint
	t.Run("SSEStream", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/projects/test-project/user-inputs/stream", nil)
		w := httptest.NewRecorder()

		// Create a context with timeout since SSE is long-lived
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()
		req = req.WithContext(ctx)

		router.ServeHTTP(w, req)

		// SSE should set proper headers
		require.Equal(t, "text/event-stream", w.Header().Get("Content-Type"))
	})

}

// TestMissingProjectIdParameter verifies that missing projectId returns 400
func TestMissingProjectIdParameter(t *testing.T) {
	op := GetOp()
	mgmtService := op.GetManagementService().(*inputManagementService)

	g := gen{max: 1}
	eng := newToyEngine(t, "", g.Generate)
	wf := workflow.SWFWorkflowControl{Engine: eng}
	deps := coreops.NewServiceDepsBuilder().
		WithWorkflowControl(&wf).
		WithSSEManager(NewSimpleSSEManager()).
		Build()
	require.NoError(t, mgmtService.Initialize(deps))

	// Setup router WITHOUT the {projectId} parameter
	router := mux.NewRouter()
	router.HandleFunc("/projects/user-inputs/pending", mgmtService.ListPending).Methods("GET")

	req := httptest.NewRequest("GET", "/projects/user-inputs/pending", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code)
	require.Contains(t, w.Body.String(), "projectId is required")
}
