package thinpackrebase

import (
	"context"

	"github.com/colony-2/c2j/pkg/ops"
)

// ThinpackRebaseInput captures the parameters required to rebase a thin-pack backed workspace.
type ThinpackRebaseInput struct {
	RepoPath       string `json:"repo_path,omitempty" default:"{{ context.environment.worktree_path }}" validate:"required,dir"`
	TargetBaseHash string `json:"target_base_hash" validate:"required,hexadecimal,min=7,max=40"`
	UpstreamRemote string `json:"upstream_remote,omitempty"`
	PreserveAuthor *bool  `json:"preserve_author,omitempty"`
	UpdateRefs     string `json:"update_refs,omitempty"`
	BaseHash       string `json:"base_hash" default:"{{ context.git.resolved_hash }}" validate:"required,hexadecimal,min=7,max=40"`
	PersistHash    string `json:"persist_hash,omitempty" validate:"omitempty,hexadecimal,min=7,max=40"`
	BaseRepo       string `json:"base_repo,omitempty" default:"{{ context.git.repo }}"`
	GitAuthor      string `json:"git_author,omitempty" default:"{{ context.git.author }}"`
	CellName       string `json:"cell_name,omitempty" default:"{{ context.workflow.cell }}"`
}

// RebasedFromSummary records the base/persist pair prior to the rebase.
type RebasedFromSummary struct {
	BaseHash    string `json:"base_hash"`
	PersistHash string `json:"persist_hash"`
}

// ThinpackRebaseOutput returns details about the rebase along with metadata for downstream ops.
type ThinpackRebaseOutput struct {
	TargetBaseHash  string                 `json:"target_base_hash"`
	NewBaseHash     string                 `json:"new_base_hash"`
	NewPersistHash  string                 `json:"new_persist_hash"`
	UpdatedRef      string                 `json:"updated_ref,omitempty"`
	RebasedFrom     RebasedFromSummary     `json:"rebased_from"`
	GitContextPatch map[string]interface{} `json:"git_context_patch,omitempty"`
}

// ThinpackRebaseActivity implements the RegisterableOp interface.
type ThinpackRebaseActivity struct{}

// NewThinpackRebaseActivity constructs a thinpack rebase activity wrapper.
func NewThinpackRebaseActivity() *ThinpackRebaseActivity {
	return &ThinpackRebaseActivity{}
}

// GetOp exposes the activity in a recipe-friendly form.
func GetOp() ops.RegisterableOp {
	act := NewThinpackRebaseActivity()
	return ops.NewActivityMappedOpV2[ThinpackRebaseInput, ThinpackRebaseOutput](act.GetMetadata(), act.Execute)
}

// GetMetadata describes the activity for registry/discovery.
func (a *ThinpackRebaseActivity) GetMetadata() ops.OpMetadata {
	return ops.OpMetadata{
		Type:        "thinpackrebase",
		Description: "Rebases a thin-pack backed workspace onto a new base commit and refreshes git context metadata",
		Version:     "1.0.0",
	}
}

// Execute performs the thin-pack aware rebase operation.
func (a *ThinpackRebaseActivity) Execute(inv ops.OpDependencies, ctx context.Context, input ThinpackRebaseInput) (ThinpackRebaseOutput, error) {
	output, err := Run(ctx, input)
	if err != nil {
		return ThinpackRebaseOutput{}, err
	}
	return *output, nil
}
