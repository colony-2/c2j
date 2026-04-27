package gitstate

import "fmt"

// buildCommitMessage renders the structured commit metadata block used for git persistence.
func buildCommitMessage(ctx *GitTaskContext, persistHash, thinPackPath string) string {
	if persistHash == "" {
		persistHash = ctx.PersistHash
	}
	// ThinPackPath removed - no longer used
	return fmt.Sprintf(`Recipe node %s seq %d

---
git:
  base_ref: %s
  resolved_base_hash: %s
  parent_hash: %s
  persist_hash: %s
workflow:
  cell: %s
invocation:
  path: %s
  seq: %d
  hash: %s

---
`,
		ctx.NodePath,
		ctx.InvokeSeq,
		ctx.BaseRef,
		ctx.ResolvedBaseHash,
		ctx.ParentHash,
		persistHash,
		ctx.CellName,
		ctx.NodePath,
		ctx.InvokeSeq,
		ctx.InvokeHash,
	)
}
