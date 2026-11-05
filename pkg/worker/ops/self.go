package ops

import (
	"context"

	"github.com/colony-2/c2j/pkg/git/gitstate"
	"github.com/colony-2/c2j/pkg/gitprofile"
	"github.com/colony-2/c2j/pkg/jobcontext"
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
		return info
	}
}
