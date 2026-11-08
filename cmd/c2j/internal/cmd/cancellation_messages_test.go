package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/colony-2/c2j/internal/celltest"
	"github.com/colony-2/c2j/pkg/childbroker"
	"github.com/colony-2/c2j/pkg/jobcontext"
	"github.com/colony-2/c2j/pkg/jobinspect"
	"github.com/colony-2/c2j/pkg/selfinfo"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/remote"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/sqlite"
	"github.com/stretchr/testify/require"
)

func TestJobShowPersistedCancellationMessage(t *testing.T) {
	ctx := celltest.Context(context.Background())
	db, err := sqlite.NewFromConfig(ctx, sqlite.Config{DBPath: filepath.Join(t.TempDir(), "jobs.db")})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close(ctx)) })
	server := httptest.NewServer(celltest.Handler(remote.NewServer(db)))
	t.Cleanup(server.Close)
	rt, err := remote.New(server.URL, server.Client())
	require.NoError(t, err)
	celltest.Create(t, rt, "tenant", "test", "example.test/test")
	for _, reason := range []string{"", "  Cancelled: use the existing files.\n" + strings.Repeat("请保留 ☃\t", 600) + "\n "} {
		h, err := rt.SubmitJob(ctx, jobdb.SubmitJobRequest{Job: jobdb.SubmitJob{TenantId: "tenant", JobType: "recipe", Cell: "test", CellRepo: "example.test/test", Data: jobdb.NewTaskDataOrPanic(nil)}})
		require.NoError(t, err)
		require.NoError(t, rt.CancelJob(ctx, jobdb.CancelJobRequest{JobKey: h.JobKey, Reason: reason}))
		for _, route := range []string{"standalone", "task-helper"} {
			t.Run(route+"/"+h.JobKey.JobId, func(t *testing.T) {
				current := jobcontext.Current{TenantID: "tenant", JobID: h.JobKey.JobId, GoalID: h.GoalID}
				for key := range jobcontext.EnvForCurrent(current) {
					t.Setenv(key, "")
				}
				for _, key := range []string{jobcontext.ChildJobEndpointEnv, jobcontext.ChildJobTokenEnv, jobcontext.ChildJobSessionIDEnv} {
					t.Setenv(key, "")
				}
				t.Setenv("C2J_JOBDB", server.URL+"/tenant")
				if route == "task-helper" {
					api := &jobinspect.Service{Runtime: rt, Tenant: "tenant"}
					broker, err := childbroker.Start(jobinspect.WithAPI(ctx, api), childbroker.Options{Current: current, Self: func() selfinfo.Info { return selfinfo.Unknown("") }})
					require.NoError(t, err)
					defer broker.Close()
					for k, v := range jobcontext.EnvForCurrent(current) {
						t.Setenv(k, v)
					}
					for k, v := range broker.Env() {
						t.Setenv(k, v)
					}
					t.Setenv("C2J_JOBDB", "invalid://ambient")
				}
				run := func(args ...string) string {
					var out bytes.Buffer
					command := newJobCmd()
					command.SetOut(&out)
					command.SetErr(io.Discard)
					command.SetArgs(args)
					require.NoError(t, command.Execute())
					return out.String()
				}
				var response jobinspect.Response
				require.NoError(t, json.Unmarshal([]byte(run("show", h.JobKey.JobId, "--json")), &response))
				require.Equal(t, "CANCELLED", response.Job.Status)
				require.Equal(t, "cancelled", response.Job.CompletionStatus)
				require.Equal(t, reason, response.Job.CompletionDetail)
				text := run("show", h.JobKey.JobId)
				if reason == "" {
					require.NotContains(t, text, "Completion detail:")
				} else {
					require.Contains(t, text, "Completion detail:\n"+reason+"\n")
				}
			})
		}
	}
}
