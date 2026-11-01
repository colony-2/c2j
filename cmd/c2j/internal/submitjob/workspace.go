package submitjob

import (
	"context"
	"fmt"
	"strings"

	"github.com/colony-2/c2j/pkg/childbroker"
	configpkg "github.com/colony-2/c2j/pkg/config"
	"github.com/colony-2/c2j/pkg/git/common"
	"github.com/colony-2/c2j/pkg/git/gitstate"
	"github.com/colony-2/c2j/pkg/git/selectorcache"
	"github.com/colony-2/c2j/pkg/recipejob"
	"github.com/colony-2/c2j/pkg/workspacesubmit"
)

func resolveSubmissionWorkspace(ctx context.Context, opts Options, workspaceInfo *childbroker.WorkspaceInfo) (targetCell, *workspacesubmit.Source, error) {
	if !opts.FromWorkspace && !opts.FromWorkspaceNow {
		t, e := resolveTargetCell(ctx, opts)
		return t, nil, e
	}
	if workspaceInfo != nil {
		if strings.TrimSpace(opts.Cell) != "" {
			t, e := resolveTargetCell(ctx, opts)
			return t, nil, e
		}
		g := workspaceInfo.Git
		return targetCell{RepositorySource: g.BaseRepo, DefaultRef: g.BaseRef, CellName: g.GetWorkspaceCellName(), CellResolution: g.CellResolution}, nil, nil
	}
	root, err := common.FindGitRootContext(ctx, opts.WorkingDir)
	if err != nil {
		return targetCell{}, nil, err
	}
	self, err := resolveSelfTarget(ctx, root)
	if err != nil {
		if _, configErr := configpkg.LoadProjectConfig(root); configErr != configpkg.ErrConfigNotFound {
			return targetCell{}, nil, err
		}
		// A plain repository without c2j configuration can use origin, or its local path.
		repo := root
		if origin, e := common.ExecuteGitCommand(ctx, root, "remote", "get-url", "origin"); e == nil && strings.TrimSpace(string(origin)) != "" {
			repo = strings.TrimSpace(string(origin))
		}
		self, err = resolveExplicitTarget(ctx, root, repo)
		if err != nil {
			return targetCell{}, nil, err
		}
	}
	target := self
	if strings.TrimSpace(opts.Cell) != "" {
		target, err = resolveTargetCell(ctx, opts)
		if err != nil {
			return targetCell{}, nil, err
		}
		equal, e := recipejob.RepositorySourcesEqual(self.RepositorySource, target.RepositorySource)
		if e != nil {
			return targetCell{}, nil, e
		}
		if !equal || self.CellName != target.CellName {
			return targetCell{}, nil, fmt.Errorf("target cell does not match the submitting workspace")
		}
	}
	base, err := selectorcache.Default().Resolve(ctx, selectorcache.ResolveRequest{RepositoryURL: self.RepositorySource, Ref: self.DefaultRef})
	if err != nil {
		return targetCell{}, nil, fmt.Errorf("resolve workspace base: %w", err)
	}
	source := &workspacesubmit.Source{Worktree: root, Git: gitstate.GlobalGitTaskContext{BaseRepo: self.RepositorySource, BaseRef: self.DefaultRef, ResolvedBaseHash: base.Commit, CellName: self.CellName, CellResolution: self.CellResolution}}
	return target, source, nil
}
