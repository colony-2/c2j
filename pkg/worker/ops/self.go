package ops

import (
	"context"

	"github.com/colony-2/c2j/pkg/git/gitstate"
	"github.com/colony-2/c2j/pkg/gitprofile"
	"github.com/colony-2/c2j/pkg/jobcontext"
	"github.com/colony-2/c2j/pkg/jobinspect"
	coreops "github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/c2j/pkg/permissions"
	"github.com/colony-2/c2j/pkg/selfinfo"
)

func invocationSelf(ctx context.Context, current jobcontext.Current, paths coreops.OperationPaths, git gitstate.GlobalGitTaskContext, canSubmit bool) selfinfo.Provider {
	runtime := selfinfo.FromContext(ctx)
	workspace := selfinfo.Workspace{Status: "present", CellName: selfinfo.String(git.GetWorkspaceCellName()), Repo: selfinfo.String(git.BaseRepo), BaseRef: selfinfo.String(git.BaseRef), BaseRevision: selfinfo.String(git.ResolvedBaseHash), ContentIdentity: "unknown", Paths: &selfinfo.Paths{Workdir: paths.Workdir, Worktree: paths.WorktreePath, Inbox: paths.Inbox, Outbox: paths.Outbox}}
	if git.Workspace != nil {
		workspace.ScopeID = selfinfo.String(git.Workspace.ScopeID)
	}
	return func() selfinfo.Info {
		info := selfinfo.Unknown("runtime_metadata_unavailable")
		if runtime != nil {
			info = runtime()
		}
		info.SetCurrent(current, "runtime_helper")
		info.Workspace = workspace
		info.SetCapabilities(canSubmit, paths.WorktreePath != "", gitprofile.FromContext(ctx) != nil, permissions.FromContext(ctx) != nil)
		if jobinspect.FromContext(ctx) != nil {
			info.Capabilities["jobs.inspect"] = selfinfo.Capability{Permission: "granted", Availability: "ready", Command: selfinfo.String("c2j job log self"), Route: selfinfo.String("child_job_broker"), Restrictions: []string{"reads follow JobDB permissions for the selected tenant"}}
			switch info.Lease.Status {
			case "lost", "expired", "cancelled":
				c := info.Capabilities["jobs.inspect"]
				c.Availability = "unavailable"
				c.ReasonCode = selfinfo.String("execution_" + info.Lease.Status)
				info.Capabilities["jobs.inspect"] = c
			}
		}
		return info
	}
}
