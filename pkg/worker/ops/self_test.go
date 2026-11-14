package ops

import (
	"context"
	"github.com/colony-2/c2j/pkg/git/gitstate"
	"github.com/colony-2/c2j/pkg/jobcontext"
	"github.com/colony-2/c2j/pkg/jobinspect"
	coreops "github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/c2j/pkg/selfinfo"
	"github.com/stretchr/testify/require"
	"testing"
)

type selfInspection struct{ jobinspect.API }

func TestInspectionCapabilityDescribesBackendReadScope(t *testing.T) {
	for _, status := range []string{"active", "lost", "expired", "cancelled"} {
		ctx := selfinfo.WithProvider(context.Background(), func() selfinfo.Info { info := selfinfo.Unknown(""); info.Lease.Status = status; return info })
		ctx = jobinspect.WithAPI(ctx, selfInspection{})
		info := invocationSelf(ctx, jobcontext.Current{}, coreops.OperationPaths{}, gitstate.GlobalGitTaskContext{}, false)()
		capability := info.Capabilities["jobs.inspect"]
		require.Equal(t, "granted", capability.Permission)
		require.Equal(t, []string{"reads follow JobDB permissions for the selected tenant"}, capability.Restrictions)
		if status == "active" {
			require.Equal(t, "ready", capability.Availability)
		} else {
			require.Equal(t, "unavailable", capability.Availability)
		}
	}
}
