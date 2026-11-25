package compiler

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/colony-2/c2j/internal/celltest"
	extops "github.com/colony-2/c2j/pkg/ops/extensions"
	"github.com/colony-2/c2j/pkg/worker/commandop"
	"github.com/colony-2/jobdb/pkg/jobdb"
	wf "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

var errEagainLostAck = errors.New("injected lost acknowledgment for extension EAGAIN")

type eagainFaultRuntime struct {
	jobdb.ConsumerRuntime
	beforeRoute func(jobdb.RenewableExecutionLease) error
	afterRoute  func() error
	afterWait   func(jobdb.ResourceBackoffReceipt) error
}

type eagainFaultLease struct {
	jobdb.RenewableExecutionLease
	fault *eagainFaultRuntime
}

func (rt *eagainFaultRuntime) GetJobLease(ctx context.Context, req jobdb.GetJobLeaseRequest) (jobdb.ExecutionLease, error) {
	lease, err := rt.ConsumerRuntime.GetJobLease(ctx, req)
	if err != nil || lease == nil {
		return lease, err
	}
	return &eagainFaultLease{lease.(jobdb.RenewableExecutionLease), rt}, nil
}

func (lease *eagainFaultLease) Renew(ctx context.Context) (jobdb.RenewableExecutionLease, error) {
	next, err := lease.RenewableExecutionLease.Renew(ctx)
	if err != nil {
		return nil, err
	}
	return &eagainFaultLease{next, lease.fault}, nil
}

func (lease *eagainFaultLease) LeaseSchemaHash() string {
	return lease.RenewableExecutionLease.(interface{ LeaseSchemaHash() string }).LeaseSchemaHash()
}

func (lease *eagainFaultLease) Reschedule(ctx context.Context, req jobdb.RescheduleExecutionRequest) error {
	if lease.fault.beforeRoute != nil {
		if err := lease.fault.beforeRoute(lease.RenewableExecutionLease); err != nil {
			return err
		}
	}
	if err := lease.RenewableExecutionLease.Reschedule(ctx, req); err != nil {
		return err
	}
	if lease.fault.afterRoute != nil {
		return lease.fault.afterRoute()
	}
	return nil
}

func (lease *eagainFaultLease) RescheduleResourceBackoff(ctx context.Context, req jobdb.ResourceBackoffRequest) (jobdb.ResourceBackoffReceipt, error) {
	receipt, err := lease.RenewableExecutionLease.(jobdb.ResourceBackoffLease).RescheduleResourceBackoff(ctx, req)
	if err == nil && lease.fault.afterWait != nil {
		if err := lease.fault.afterWait(receipt); err != nil {
			return jobdb.ResourceBackoffReceipt{}, err
		}
	}
	return receipt, err
}

func TestExtensionEAGAINRecoverySQLite(t *testing.T) {
	withRegisteredOps(t, extops.GetExecutionOp(), commandop.GetOp())
	for _, scenario := range []string{"before-route", "lost-route-ack", "lost-backoff-ack", "expired-at-route", "expired-at-wake"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := celltest.Context(context.Background())
			rt := newTimeoutReproRuntime(t, "sqlite").ConsumerRuntime
			repo := filepath.Join(t.TempDir(), "repo")
			workspaceRepo(t, repo, "source")
			launches := filepath.Join(t.TempDir(), "launches")
			successor := filepath.Join(t.TempDir(), "successor")
			commitHookFile(t, repo, "ops/repeat/op.yaml", fmt.Sprintf(`name: repeat
shell: sh
run: |
  set -eu
  cat >/dev/null
  if [ -z "${C2J_EXTENSION_EAGAIN_RESUME+x}" ]; then
    echo producer >> %q
    printf '%%s' '{"eagain":{"version":1,"mode":"persist","next_inputs":{}}}'
    exit 77
  fi
  echo continuation >> %q
  printf '%%s' '{"output":{"result":"resumed"}}'
input_schema: {type: object}
output_schema: {type: object, required: [result]}
`, launches, launches))
			timeout := ""
			const lifetime = 5 * time.Second
			if scenario == "expired-at-route" {
				timeout = fmt.Sprintf("timeout: %s\n", lifetime)
			} else if scenario == "expired-at-wake" {
				timeout = "timeout: 1500ms\n"
			}
			key := submitGitLifecycle(t, rt, repo, fmt.Sprintf(`id: git-lifecycle
%ssequence:
 - id: repeat
   op: ./ops/repeat
 - id: later
   op: command_execution
   inputs: {run: %q}
outputs: {result: '${{ sequence.repeat.outputs.result }}'}
`, timeout, fmt.Sprintf("printf done > %q", successor)))
			assertLaunches := func(want string) {
				t.Helper()
				contents, err := os.ReadFile(launches)
				require.NoError(t, err)
				require.Equal(t, want, string(contents))
			}
			assertNoSuccessor := func() {
				t.Helper()
				_, err := os.Stat(successor)
				require.ErrorIs(t, err, os.ErrNotExist)
			}
			assertProducerCommitted := func() time.Time {
				t.Helper()
				chapters, err := rt.ListChapters(ctx, jobdb.ListChaptersRequest{JobKey: key})
				require.NoError(t, err)
				results := 0
				var checkpointDeadline time.Time
				for _, chapter := range chapters {
					body, ok := chapter.Body.(jobdb.ExecutionChapter)
					if !ok {
						continue
					}
					record, err := jobdb.DecodeExecutionRecord(body.Record)
					require.NoError(t, err)
					if record.Kind == "resource_checkpoint" {
						checkpointDeadline = record.Deadline
					}
					if record.Kind != "result" || record.Output == nil {
						continue
					}
					for _, artifact := range record.Output.Artifacts {
						if artifact.Name == "_c2j/eagain-v1.json" {
							require.Empty(t, record.ErrorKind)
							results++
						}
					}
				}
				require.Equal(t, 1, results, "a replacement must use the committed extension result")
				return checkpointDeadline
			}
			recipeRoute := jobdb.Route{JobType: "recipe"}
			const replacement = "replacement"
			if scenario != "lost-backoff-ack" && scenario != "expired-at-wake" {
				out, err := runEagainRecipe(t, ctx, rt, key, true, false)
				require.NoError(t, err)
				require.Equal(t, wf.JobRunSuspended, out.Status, "%v", out.JobError)
				state, err := rt.GetJob(ctx, key)
				require.NoError(t, err)
				require.NotNil(t, state.ExecutionState.TaskWait)
				fault := &eagainFaultRuntime{ConsumerRuntime: rt}
				var expiry time.Time
				routeCalls := 0
				fault.beforeRoute = func(lease jobdb.RenewableExecutionLease) error {
					routeCalls++
					expiry = lease.LeaseExpiry()
					if scenario == "before-route" {
						return jobdb.ErrExecutionLeaseLost
					}
					return nil
				}
				fault.afterRoute = func() error { return errEagainLostAck }
				out, err = runEagainRecipeAs(t, ctx, fault, key, false, true, "interrupted", 2*time.Second)
				require.Error(t, err)
				require.Nil(t, out.Handoff)
				require.Equal(t, 1, routeCalls)
				assertProducerCommitted()
				assertLaunches("producer\n")
				assertNoSuccessor()
				state, err = rt.GetJob(ctx, key)
				require.NoError(t, err)
				require.Nil(t, state.ExecutionState.ResourceResume, "the route handoff is not a backoff acknowledgment")
				if scenario == "before-route" {
					require.NotNil(t, state.ExecutionState.TaskWait)
					time.Sleep(time.Until(expiry) + 30*time.Millisecond)
					out, err = runEagainRecipeAs(t, ctx, rt, key, false, true, replacement, time.Minute)
					require.NoError(t, err)
					require.Equal(t, wf.JobRunSuspended, out.Status, "%v", out.JobError)
					require.NotNil(t, out.Handoff)
					assertProducerCommitted()
				} else {
					require.Nil(t, state.ExecutionState.TaskWait)
					require.Equal(t, &recipeRoute, state.NextRoute)
				}
				if scenario == "expired-at-route" {
					time.Sleep(lifetime)
					out, err = runEagainRecipeAs(t, ctx, rt, key, false, false, replacement, time.Minute)
					require.NoError(t, err)
					require.Equal(t, wf.JobRunFailed, out.Status)
					require.ErrorContains(t, out.JobError, "scope total timed out")
					assertProducerCommitted()
					assertLaunches("producer\n")
					assertNoSuccessor()
					return
				}
			}
			fault := &eagainFaultRuntime{ConsumerRuntime: rt}
			var receipt jobdb.ResourceBackoffReceipt
			fault.afterWait = func(committed jobdb.ResourceBackoffReceipt) error {
				receipt = committed
				if scenario == "lost-backoff-ack" {
					return errEagainLostAck
				}
				return nil
			}
			out, err := runEagainRecipeAs(t, ctx, fault, key, false, false, replacement, time.Minute)
			if scenario == "lost-backoff-ack" {
				require.Error(t, err)
				require.True(t, jobdb.IsExecutionInfrastructureError(err), "%v", err)
				require.Nil(t, out.Handoff)
			} else {
				require.NoError(t, err)
				require.Equal(t, wf.JobRunSuspended, out.Status, "%v", out.JobError)
			}
			require.NotEmpty(t, receipt.Resume.Invocation, "SQLite must have committed the wait")
			parked, err := rt.GetJob(ctx, key)
			require.NoError(t, err)
			require.Equal(t, &receipt.Resume, parked.ExecutionState.ResourceResume)
			early, err := rt.GetJobLease(ctx, jobdb.GetJobLeaseRequest{JobKey: key, WorkerID: "early", Routes: []jobdb.Route{recipeRoute}})
			require.NoError(t, err)
			require.Nil(t, early)
			checkpointDeadline := assertProducerCommitted()
			assertLaunches("producer\n")
			assertNoSuccessor()
			if scenario == "expired-at-wake" {
				require.False(t, checkpointDeadline.IsZero(), "the wait must retain the enclosing deadline")
				require.True(t, checkpointDeadline.Before(receipt.WakeAt), "deadline %s must expire while parked until %s", checkpointDeadline, receipt.WakeAt)
			}
			waitForCompilerJobReady(t, rt, key, 1, time.Sleep)
			out, err = runEagainRecipeAs(t, ctx, rt, key, false, false, "after-wake", time.Minute)
			require.NoError(t, err)
			if scenario == "expired-at-wake" {
				require.Equal(t, wf.JobRunFailed, out.Status)
				require.ErrorContains(t, out.JobError, "scope total timed out")
				assertProducerCommitted()
				assertLaunches("producer\n")
				assertNoSuccessor()
				return
			}
			require.Equal(t, "resumed", gitLifecycleOutput(t, out)["result"])
			assertLaunches("producer\ncontinuation\n")
			contents, err := os.ReadFile(successor)
			require.NoError(t, err)
			require.Equal(t, "done", string(contents))
		})
	}
}
