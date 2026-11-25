package childbroker

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/colony-2/c2j/pkg/jobcontext"
	"github.com/colony-2/c2j/pkg/jobref"
	"github.com/colony-2/c2j/pkg/workflowctl"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
)

func TestSubmitterDependencyBrokerValidation(t *testing.T) {
	f := newBrokerOnlyFixture(t, "")
	var submits atomic.Int32
	f.broker.submitter = brokerSubmitFunc(func(context.Context, jobdb.SubmitJob) (jobref.Ref, error) {
		submits.Add(1)
		return jobref.Ref{}, fmt.Errorf("unexpected backend submission")
	})
	for _, tc := range []struct {
		name, body, auth string
		status           int
		routeSuffix      string
	}{
		{"missing", `{}`, "", 400, submitterDependencyPath},
		{"null", `{"submitter_dependency":null}`, "", 400, submitterDependencyPath},
		{"string", `{"submitter_dependency":"false"}`, "", 400, submitterDependencyPath},
		{"numeric", `{"submitter_dependency":0}`, "", 400, submitterDependencyPath},
		{"wrong-token", `{"submitter_dependency":false}`, "token", 401, submitterDependencyPath},
		{"wrong-session", `{"submitter_dependency":true}`, "session", 401, submitterDependencyPath},
		{"selected", `{"submitter_dependency":false,"start":{"goal_id":"goal"}}`, "", 400, submitterDependencyPath},
		{"descriptor", `{"submitter_dependency":false,"descriptor":{}}`, "", 400, submitterDependencyPath},
		{"legacy-route", `{"submitter_dependency":false}`, "", 400, ""},
		{"prepare-route", `{"submitter_dependency":true}`, "", 400, "/prepare"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, submitPath+tc.routeSuffix, strings.NewReader(tc.body))
			r.Header.Set("Authorization", "Bearer "+f.broker.token)
			r.Header.Set(sessionHeader, f.broker.sessionID)
			switch tc.auth {
			case "token":
				r.Header.Set("Authorization", "Bearer wrong")
			case "session":
				r.Header.Set(sessionHeader, "wrong")
			}
			w := httptest.NewRecorder()
			f.broker.server.Handler.ServeHTTP(w, r)
			require.Equal(t, tc.status, w.Code, w.Body.String())
			require.Zero(t, submits.Load())
			f.inventory(0)
		})
	}
}

func TestSubmitterDependencyRouteUsesURLPath(t *testing.T) {
	requestedPath := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestedPath <- r.URL.Path
		http.NotFound(w, r)
	}))
	defer server.Close()
	value := false
	_, err := Submit(context.Background(), jobcontext.ChildJobBroker{Endpoint: server.URL + submitPath + "?route=local#fragment"}, SubmitRequest{SubmitterDependency: &value})
	require.ErrorContains(t, err, "does not support --submitter-dependency")
	select {
	case path := <-requestedPath:
		require.Equal(t, submitPath+submitterDependencyPath, path)
	default:
		t.Fatal("broker request was not observed")
	}
}

func TestSubmitterDependencyCLI(t *testing.T) {
	binary := os.Getenv("C2J_TEST_SUBMIT_BINARY")
	if binary == "" {
		binary = filepath.Join(t.TempDir(), "c2j")
		c := exec.Command("go", "build", "-mod=readonly", "-o", binary, "./cmd/c2j")
		c.Dir = testRepoRoot(t)
		out, err := c.CombinedOutput()
		require.NoError(t, err, string(out))
	}
	t.Run("explicit-empty", func(t *testing.T) {
		f := newBrokerOnlyFixture(t, binary)
		out, err := f.run(append(f.args(), "--submitter-dependency="), nil)
		require.Error(t, err)
		require.Contains(t, string(out), "--submitter-dependency requires true or false")
		require.Zero(t, f.publications.Load())
		f.inventory(0)
	})
	anchor := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
	for _, marker := range []string{"true", "false", ""} {
		name := marker
		if name == "" {
			name = "omitted"
		}
		t.Run("durable-pending/"+name, func(t *testing.T) {
			f := newBrokerOnlyFixture(t, binary)
			args := append(f.args(), "--wait-job", f.source.JobID, "--wait-until", anchor.Format(time.RFC3339))
			if marker != "" {
				args = append(args, "--submitter-dependency="+marker)
			}
			ctx, cancel := context.WithTimeout(f.ctx, 15*time.Second)
			defer cancel()
			sourceCtx := f.ctx
			f.ctx = ctx
			out, err := f.run(args, nil)
			f.ctx = sourceCtx
			require.NoError(t, err, string(out))
			ref := f.checkSubmission(out, "")
			key := jobdb.JobKey{TenantId: ref.TenantID, JobId: ref.JobID}
			rows, err := f.rt.ListJobs(f.ctx, jobdb.ListJobsRequest{TenantIds: []string{ref.TenantID}, JobKeys: []jobdb.JobKey{key}})
			require.NoError(t, err)
			require.Len(t, rows.Jobs, 1)
			require.Equal(t, jobdb.JobStatusPendingJobs, rows.Jobs[0].Status)
			require.True(t, anchor.Equal(rows.Jobs[0].AvailableAt))
			var metadata map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(rows.Jobs[0].Metadata, &metadata))
			require.Equal(t, marker, string(metadata["submitter_dependency"]))
			require.NotContains(t, string(out), "submitter_dependency")
			chapter, err := f.rt.GetChapter(f.ctx, jobdb.ChapterRef{JobKey: key, Ordinal: 0})
			require.NoError(t, err)
			raw := chapter.Body.(jobdb.JobStartChapter).Input.Data
			require.NotContains(t, string(raw), "submitter_dependency")
			var start workflowctl.StartJob
			require.NoError(t, json.Unmarshal(raw, &start))
			wait, err := f.rt.GetDependencyWait(f.ctx, jobdb.DependencyWaitKey{JobKey: key, WaitID: "submission"})
			require.NoError(t, err)
			require.Equal(t, jobdb.DependencyPending, wait.State)
			require.Len(t, wait.Dependencies, 1)
			gotWaits := make(map[jobdb.DependencyTarget]jobdb.JobPrereqCondition)
			for _, dependency := range wait.Dependencies {
				gotWaits[dependency.Target] = dependency.Condition
			}
			require.Equal(t, map[jobdb.DependencyTarget]jobdb.JobPrereqCondition{
				{JobID: f.source.JobID}: jobdb.JobPrereqComplete,
			}, gotWaits)
		})
	}
	for _, marker := range []string{"true", "false", ""} {
		for _, status := range []int{404, 307, 308} {
			if marker == "" && status != 404 {
				continue
			}
			name := marker
			if name == "" {
				name = "omitted"
			}
			t.Run(fmt.Sprintf("no-downgrade/%s/%d", name, status), func(t *testing.T) {
				f := newBrokerOnlyFixture(t, binary)
				var explicit, legacy, redirected, direct atomic.Int32
				redirectTrap := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					redirected.Add(1)
					http.Error(w, "redirected submission", http.StatusInternalServerError)
				}))
				defer redirectTrap.Close()
				directTrap := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					direct.Add(1)
					http.Error(w, "direct runtime forbidden", http.StatusInternalServerError)
				}))
				defer directTrap.Close()
				proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Method == http.MethodPost {
						switch r.URL.Path {
						case submitPath + submitterDependencyPath:
							explicit.Add(1)
							if status == 404 {
								http.NotFound(w, r)
							} else {
								http.Redirect(w, r, redirectTrap.URL, status)
							}
							return
						case submitPath:
							legacy.Add(1)
						}
					}
					f.broker.server.Handler.ServeHTTP(w, r)
				}))
				defer proxy.Close()
				args := append(f.args(), "--jobdb", directTrap.URL+"/"+brokerOnlyTenant)
				if marker != "" {
					args = append(args, "--submitter-dependency="+marker)
				}
				out, err := f.run(args, map[string]string{jobcontext.ChildJobEndpointEnv: proxy.URL + submitPath})
				if marker == "" {
					require.NoError(t, err, string(out))
					require.EqualValues(t, 1, legacy.Load())
					require.Zero(t, explicit.Load())
					f.checkSubmission(out, "")
				} else {
					require.Error(t, err, string(out))
					require.Contains(t, string(out), "submitter-dependency")
					require.EqualValues(t, 1, explicit.Load())
					require.Zero(t, legacy.Load())
					f.inventory(0)
				}
				require.Zero(t, direct.Load())
				require.Zero(t, redirected.Load())
			})
		}
	}
}
