package executionruntime

import (
	"context"
	"testing"
	"time"

	"github.com/colony-2/c2j/internal/joblabelscope"
	"github.com/colony-2/c2j/pkg/execution"
	"github.com/colony-2/c2j/pkg/joblabel"
	"github.com/colony-2/c2j/pkg/permissions"
	"github.com/colony-2/jobdb/pkg/jobdb"
	wf "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

type labelTestOwner struct {
	jobdb.ExecutionLease
	key        jobdb.JobKey
	expiry     time.Time
	delegation *jobdb.DelegationInfo
}

func (owner *labelTestOwner) Job() jobdb.JobHandle              { return jobdb.JobHandle{JobKey: owner.key} }
func (owner *labelTestOwner) LeaseID() string                   { return "lease" }
func (owner *labelTestOwner) LeaseExpiry() time.Time            { return owner.expiry }
func (owner *labelTestOwner) Delegation() *jobdb.DelegationInfo { return owner.delegation }

type labelTestWriter struct {
	*labelTestOwner
	calls int
}

func (writer *labelTestWriter) EditJobMetadata(ctx context.Context, edit jobdb.JobMetadataEdit) (jobdb.JobMetadataEntry, error) {
	writer.calls++
	return jobdb.JobMetadataEntry{Version: 1, Present: true, Value: edit.Set}, ctx.Err()
}

func TestJobLabelsLeaseScope(t *testing.T) {
	key := jobdb.JobKey{TenantId: "labels", JobId: "job"}
	task := wf.NewTaskContext(key, 1, nil, nil, nil)
	value, color := "foe", joblabel.Blue
	edit := joblabel.Edit{Action: "set", Key: "friend", ExpectedVersion: new(joblabel.Version), Value: &value, Color: &color}
	for _, scenario := range []struct {
		name string
		err  error
	}{
		{"ordinary", nil},
		{"delegated", jobdb.ErrLeasePermissionDenied},
		{"expired", jobdb.ErrExecutionLeaseLost},
		{"canceled", jobdb.ErrExecutionLeaseLost},
		{"superseded", jobdb.ErrExecutionLeaseLost},
		{"missing-port", jobdb.ErrJobMetadataUnsupported},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			runtime := New(nil, execution.Allocation{})
			require.NoError(t, runtime.ConfigureJobLabelEdits(key, "labels/edit", ""))
			owner := &labelTestOwner{key: key, expiry: time.Now().Add(time.Minute)}
			writer := &labelTestWriter{labelTestOwner: owner}
			var native jobdb.ExecutionLease = writer
			if scenario.name == "missing-port" {
				native = owner
			}
			original := runtime.wrapLease(native, &leaseState{}).(*lease)
			active, cancel := context.WithCancel(context.Background())
			defer cancel()
			scope := runtime.bindJobLabelEdit(active, task, original, "labels/edit", "job_label_edit:job_label_edit", permissions.State{Version: permissions.Version})
			call := joblabelscope.BuiltinEdit(scope)
			require.NotNil(t, call)
			switch scenario.name {
			case "delegated":
				owner.delegation = &jobdb.DelegationInfo{}
			case "expired":
				owner.expiry = time.Now().Add(-time.Second)
			case "canceled":
				cancel()
			case "superseded":
				runtime.wrapLease(native, &leaseState{})
			}
			state, err := call(context.Background(), edit)
			if scenario.err == nil {
				require.NoError(t, err)
				require.Equal(t, joblabel.Present, state.Status)
				require.Equal(t, 1, writer.calls)
			} else {
				require.ErrorIs(t, err, scenario.err)
				require.Zero(t, writer.calls)
			}
		})
	}
	for _, policy := range []permissions.State{
		{Version: -1},
		{Version: permissions.Version, Grant: &permissions.JobDB{}},
		{Version: permissions.Version, Limit: &permissions.JobDB{}},
		{Version: permissions.Version, ChildGrant: &permissions.JobDB{}},
		{Version: permissions.Version, Overrides: []permissions.JobDBOverrides{{}}},
		{Version: permissions.Version, ChildHookLineage: &permissions.HookLineage{}},
		{Version: permissions.Version, Inputs: &permissions.InputPolicy{Foreign: true}},
	} {
		require.False(t, labelPolicyAllows(policy))
	}
}
