package childbroker

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/colony-2/c2j/internal/celltest"
	"github.com/colony-2/c2j/pkg/git/gitstate"
	"github.com/colony-2/c2j/pkg/jobcontext"
	"github.com/colony-2/c2j/pkg/jobref"
	"github.com/colony-2/c2j/pkg/starter"
	"github.com/colony-2/c2j/pkg/workflowctl"
	"github.com/colony-2/c2j/pkg/workspacesubmit"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
)

type workspaceReceiptSubmitter func(context.Context, jobdb.SubmitJob) (jobref.Ref, error)

func (f workspaceReceiptSubmitter) SubmitJobRef(ctx context.Context, job jobdb.SubmitJob) (jobref.Ref, error) {
	return f(ctx, job)
}

// Workspace selection delegates the target to the worker, but the native
// receipt must still match that resolved target before recording the child.
func TestExactJobWorkspaceNativeReceipt(t *testing.T) {
	for _, prepared := range []bool{false, true} {
		for _, fault := range []string{"none", "tenant", "job", "cell", "cell-id", "repository"} {
			t.Run(strings.Join([]string{map[bool]string{false: "direct", true: "prepared"}[prepared], fault}, "/"), func(t *testing.T) {
				ctx := celltest.Context(context.Background())
				capture := &captureSubmitter{}
				submit := workspaceReceiptSubmitter(func(ctx context.Context, job jobdb.SubmitJob) (jobref.Ref, error) {
					require.Equal(t, "workspace", job.Cell)
					require.Equal(t, "example.test/workspace", job.CellRepo)
					ref, err := capture.SubmitJobRef(ctx, job)
					switch fault {
					case "tenant":
						ref.TenantID = "other"
					case "job":
						ref.JobID = "other"
					case "cell":
						ref.Cell = "other"
					case "cell-id":
						ref.CellID = ""
					case "repository":
						ref.CellRepo = "example.test/other"
					}
					return ref, err
				})
				broker, err := Start(ctx, Options{
					Current: jobcontext.Current{TenantID: "tenant", JobID: "parent", InvocationHash: "invocation"},
					Workspace: &workspacesubmit.Source{Git: gitstate.GlobalGitTaskContext{
						BaseRepo: "example.test/workspace", BaseRef: "main", ResolvedBaseHash: strings.Repeat("a", 40), CellName: "workspace",
					}},
					Submitter: submit,
				})
				require.NoError(t, err)
				defer broker.Close()
				req := SubmitRequest{Start: workflowctl.StartJob{TenantId: "tenant", JobID: "child", Cell: "placeholder", CellRepo: "example.test/placeholder", RecipeName: "child"}, WorkspaceMode: workspacesubmit.Start}
				var response SubmitResponse
				if prepared {
					descriptor, e := Prepare(ctx, brokerConfig(t, broker), req)
					require.NoError(t, e)
					require.Zero(t, capture.calls, "preparation must not publish")
					response, err = SubmitPrepared(ctx, brokerConfig(t, broker), descriptor)
				} else {
					response, err = SubmitLocal(WithLocalSubmitter(ctx, broker), req)
				}
				require.Equal(t, 1, capture.calls)
				if fault != "none" {
					require.Error(t, err)
					require.Empty(t, broker.StartedJobs().Items, "invalid receipts must not become child evidence")
					return
				}
				require.NoError(t, err)
				require.Equal(t, "workspace", response.Cell)
				require.Equal(t, "example.test/workspace", response.CellRepo)
				require.Equal(t, "cell-workspace", response.CellID)
				require.Equal(t, []string{"child"}, broker.StartedJobs().JobIDs)
			})
		}
	}
}

// A prepared client's redundant Start projection may omit ownership; the
// response must agree with the descriptor, including after a workspace resolve.
func TestExactJobPreparedBrokerReceiptAssertions(t *testing.T) {
	ctx := celltest.Context(context.Background())
	job, err := starter.PrepareRecipeJob(ctx, workflowctl.StartJob{TenantId: "tenant", JobID: "child", Cell: "workspace", CellRepo: "example.test/workspace", RecipeName: "child"}, starter.StartRecipeJobOptions{})
	require.NoError(t, err)
	descriptor, err := starter.NewDescriptor(ctx, job)
	require.NoError(t, err)
	for _, fault := range []string{"none", "cell", "repository"} {
		t.Run(fault, func(t *testing.T) {
			response := SubmitResponse{TenantID: "tenant", JobID: "child", Cell: "workspace", CellID: "cell-workspace", CellRepo: "example.test/workspace"}
			if fault == "cell" {
				response.Cell = "other"
			} else if fault == "repository" {
				response.CellRepo = "example.test/other"
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				require.Equal(t, submitPath, r.URL.Path)
				require.NoError(t, json.NewEncoder(w).Encode(response))
			}))
			defer server.Close()
			_, err := SubmitPrepared(ctx, jobcontext.ChildJobBroker{Endpoint: server.URL + submitPath}, descriptor)
			if fault == "none" {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, "mismatch")
			}
		})
	}
}
