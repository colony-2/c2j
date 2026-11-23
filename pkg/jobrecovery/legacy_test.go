package jobrecovery_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/colony-2/c2j/pkg/jobdbschema"
	"github.com/colony-2/c2j/pkg/jobrecovery"
	"github.com/colony-2/c2j/pkg/jobref"
	"github.com/colony-2/c2j/pkg/starter"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/remote"
	workflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

type legacyCohort struct {
	Tenant              string
	Descriptor          starter.Descriptor
	Restart             json.RawMessage
	Selected, Restarted jobdb.JobHandle
}
type recordedCall struct {
	Method, Path string
	Body         json.RawMessage
}
type legacyState struct {
	Counts map[string]int
	Calls  []recordedCall
}

// The old SDK, database and server run in a distinct pinned module/process.
// Same-version mocks cannot establish this compatibility contract.
func TestExactJobLegacyCommittedReceipts(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	t.Cleanup(cancel)
	binary := filepath.Join(t.TempDir(), "legacy-server")
	build := exec.CommandContext(ctx, "go", "build", "-mod=readonly", "-o", binary, ".")
	build.Dir = "../../testdata/exactjob-legacy"
	build.Env = append(os.Environ(), "GOWORK=off")
	output, err := build.CombinedOutput()
	require.NoError(t, err, string(output))
	command := exec.CommandContext(ctx, binary, t.TempDir())
	stderr, err := os.Create(filepath.Join(t.TempDir(), "legacy-stderr.log"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = stderr.Close() })
	command.Stderr = stderr
	stdout, err := command.StdoutPipe()
	require.NoError(t, err)
	require.NoError(t, command.Start())
	t.Cleanup(func() { _ = command.Process.Kill(); _ = command.Wait() })
	var fixture struct {
		URL     string
		Cohorts []legacyCohort
		Vectors []struct {
			Descriptor *starter.Descriptor
			Restart    json.RawMessage
			Calls      []recordedCall
			Receipt    jobdb.JobHandle
		}
	}
	err = json.NewDecoder(stdout).Decode(&fixture)
	if err != nil {
		diagnostic, _ := os.ReadFile(stderr.Name())
		require.NoError(t, err, string(diagnostic))
	}
	rt, err := remote.New(fixture.URL, nil)
	require.NoError(t, err)
	sdk, err := workflow.NewEngineBuilder().WithRuntime(rt).BuildEngine()
	require.NoError(t, err)
	recovery, err := jobrecovery.New(fixture.URL, nil)
	require.NoError(t, err)
	engine := jobdbschema.WorkflowEngine{Engine: sdk, Registry: rt, Recovery: recovery}
	state := func() legacyState {
		resp, err := http.Get(fixture.URL + "/_fixture/state")
		require.NoError(t, err)
		defer resp.Body.Close()
		var result legacyState
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&result))
		return result
	}
	seeded := state()
	require.Equal(t, map[string]int{"fixed": 3, "generated": 3}, seeded.Counts)
	for _, cohort := range fixture.Cohorts {
		t.Run(cohort.Tenant, func(t *testing.T) {
			restart := decodeRestart(t, cohort.Restart)
			before, err := json.Marshal([]any{cohort.Descriptor, restart})
			require.NoError(t, err)
			selectedExpected, err := jobref.FromHandle(cohort.Selected)
			require.NoError(t, err)
			restartExpected, err := jobref.FromHandle(cohort.Restarted)
			require.NoError(t, err)
			for attempt := 0; attempt < 2; attempt++ {
				selected, err := cohort.Descriptor.Submit(ctx, engine)
				require.NoError(t, err)
				require.Equal(t, selectedExpected, selected)
				restarted, err := starter.ReplayRestartJob(ctx, engine, restart)
				require.NoError(t, err)
				require.Equal(t, restartExpected, restarted)
			}
			after, err := json.Marshal([]any{cohort.Descriptor, restart})
			require.NoError(t, err)
			require.Equal(t, before, after)
			current := state()
			require.Equal(t, seeded.Counts, current.Counts, "receipt replay must not create another job")
			// Compare exact decoded HTTP bodies with old-SDK seed calls, including all
			// modes, original identities, nested large numbers, artifacts and boundaries.
			for _, call := range current.Calls[len(seeded.Calls):] {
				if call.Method == http.MethodGet {
					require.Equal(t, "/v1/capabilities/primary-goal-selection", call.Path)
					continue
				}
				var original *recordedCall
				for _, seed := range seeded.Calls {
					if seed.Method == call.Method && seed.Path == call.Path {
						copy := seed
						original = &copy
						break
					}
				}
				require.NotNil(t, original, "no alternate endpoint: %s %s", call.Method, call.Path)
				require.Equal(t, exactJSON(t, original.Body), exactJSON(t, call.Body), call.Path)
			}
			// Material changes still conflict, never silently repair or republish.
			changed := cohort.Descriptor
			changed.Input = json.RawMessage(`{"inputs":{"n":9007199254740992}}`)
			_, err = changed.Submit(ctx, engine)
			require.Error(t, err)
			restart.Job.LastStepToKeep++
			_, err = starter.ReplayRestartJob(ctx, engine, restart)
			require.Error(t, err)
			// Revoke original owner authority against already committed receipts.
			for _, denial := range []string{"submit", "selection"} {
				denied, err := jobrecovery.New(fixture.URL, &http.Client{Transport: denyTransport{denial}})
				require.NoError(t, err)
				deniedEngine := engine
				deniedEngine.Recovery = denied
				_, err = cohort.Descriptor.Submit(ctx, deniedEngine)
				require.Error(t, err)
				if denial == "submit" {
					_, err = starter.ReplayRestartJob(ctx, deniedEngine, decodeRestart(t, cohort.Restart))
					require.Error(t, err)
				}
			}
			require.Equal(t, seeded.Counts, state().Counts)
			t.Logf("legacy f885723cc03c: %s selected/restart complete handles, original bodies/routes, repeated receipts, mismatch and authority denials; retained jobs 3/3", cohort.Tenant)
			// Start the next transcript comparison after this cohort's negative controls.
			seeded.Calls = state().Calls
		})
	}
	// Fresh operations stay on the normal SDK path even with recovery configured.
	fresh, err := engine.SubmitJobRef(ctx, jobdb.SubmitJob{TenantId: "fixed", JobID: "fresh", Cell: "app", CellRepo: "example.test/app", JobType: "work", Data: jobdb.NewTaskDataOrPanic(nil)})
	require.NoError(t, err)
	independent, err := starter.ReplayRestartJob(ctx, engine, jobdb.SubmitRestartJobRequest{RequestID: "fresh-restart-id", Job: jobdb.SubmitRestartJob{PriorJobKey: fresh.Key(), JobID: "fresh-restart", Cell: "app", CellRepo: "example.test/app", ShareGoal: false}})
	require.NoError(t, err)
	require.NotEqual(t, fresh.GoalID, independent.GoalID)
	require.Equal(t, map[string]int{"fixed": 5, "generated": 3}, state().Counts)
	t.Log("fresh create and independent restart use normal SDK endpoints with the recovery client configured")
	for i, v := range fixture.Vectors {
		t.Run(fmt.Sprintf("codec-%d", i), func(t *testing.T) {
			var calls []recordedCall
			transport := roundTripFunc(func(q *http.Request) (*http.Response, error) {
				var b []byte
				if q.Body != nil {
					var err error
					b, err = io.ReadAll(q.Body)
					require.NoError(t, err)
				}
				if len(b) == 0 {
					b = nil
				}
				calls = append(calls, recordedCall{q.Method, q.URL.Path, b})
				raw, _ := json.Marshal(v.Receipt)
				if q.Method == http.MethodGet {
					raw = []byte(`{"supported":true}`)
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(raw)), Header: http.Header{"Content-Type": []string{"application/json"}}}, nil
			})
			recovery, err := jobrecovery.New("http://codec.invalid", &http.Client{Transport: transport})
			require.NoError(t, err)
			adapter := engine
			adapter.Recovery = recovery
			if v.Descriptor != nil {
				_, err = v.Descriptor.Submit(ctx, adapter)
			} else {
				_, err = starter.ReplayRestartJob(ctx, adapter, decodeRestart(t, v.Restart))
			}
			require.NoError(t, err)
			require.Len(t, calls, len(v.Calls))
			for k, want := range v.Calls {
				require.Equal(t, want.Method, calls[k].Method)
				require.Equal(t, want.Path, calls[k].Path)
				if want.Method != http.MethodGet {
					require.Equal(t, exactJSON(t, want.Body), exactJSON(t, calls[k].Body))
				}
			}
			t.Log("old-SDK wire parity: schema, scheduling, metadata types, retry/timeouts, dependencies, prerequisites, payload update, source/boundary and request identity")
		})
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type denyTransport struct{ reason string }

func (d denyTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("X-Fixture-Deny", d.reason)
	return http.DefaultTransport.RoundTrip(r)
}
func exactJSON(t *testing.T, raw []byte) any {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	require.NoError(t, decoder.Decode(&value))
	return value
}
func decodeRestart(t *testing.T, raw []byte) jobdb.SubmitRestartJobRequest {
	t.Helper()
	var root map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(raw, &root))
	var job map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(root["Job"], &job))
	data := map[string]*jobdb.SimpleTaskData{}
	for _, name := range []string{"ExtraTaskInput", "ExtraTaskOutput"} {
		if value := job[name]; len(value) > 0 && string(value) != "null" {
			var payload jobdb.SimpleTaskData
			require.NoError(t, json.Unmarshal(value, &payload))
			data[name] = &payload
		}
		delete(job, name)
	}
	root["Job"], _ = json.Marshal(job)
	raw, _ = json.Marshal(root)
	var request jobdb.SubmitRestartJobRequest
	require.NoError(t, json.Unmarshal(raw, &request))
	if data["ExtraTaskInput"] != nil {
		request.Job.ExtraTaskInput = data["ExtraTaskInput"]
	}
	if data["ExtraTaskOutput"] != nil {
		request.Job.ExtraTaskOutput = data["ExtraTaskOutput"]
	}
	return request
}
