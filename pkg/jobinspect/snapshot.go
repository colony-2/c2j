package jobinspect

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/colony-2/c2j/pkg/artifacts"
	"github.com/colony-2/c2j/pkg/git/gitstate"
	coretask "github.com/colony-2/c2j/pkg/task"
)

func (s *Service) snapshot(ctx context.Context, h *history, q Request, selected *attempt) (*Snapshot, error) {
	boundary := q.Boundary
	if boundary == "" {
		boundary = "after"
	}
	if boundary != "before" && boundary != "after" {
		return nil, fmt.Errorf("boundary must be before or after")
	}
	if selected == nil && boundary == "before" {
		return nil, fmt.Errorf("before requires --task")
	}
	out := &Snapshot{Version: Version, JobID: q.JobID, Workspaces: []Workspace{}, Artifacts: []Artifact{}}
	cut := int(^uint(0) >> 1)
	if selected != nil {
		out.Task = selected.task.ID
		out.Attempt = selected.task.Attempt
		cut = selected.order
	}
	var attempts []*attempt
	for _, as := range h.attempts {
		for _, a := range as {
			if a.order <= cut {
				attempts = append(attempts, a)
			}
		}
	}
	sort.SliceStable(attempts, func(i, j int) bool { return attempts[i].order < attempts[j].order })
	states := map[string]Workspace{}
	for _, a := range attempts {
		if a == selected && boundary == "before" {
			continue
		}
		// Artifacts are evidence, including failed attempts. Never flatten names
		// across producers or attempts: two check.log files must both survive.
		for _, art := range a.task.Artifacts {
			if internalArtifact(art.Name) {
				continue
			}
			art.Path = artifactPath(art)
			out.Artifacts = append(out.Artifacts, art)
		}
		if a.task.Status != "succeeded" && a != selected {
			continue
		}
		w := workspaceAt(a, "after")
		if w.Repository == "" {
			continue
		}
		// Retain unavailable states too: silently omitting an older workspace
		// without a recorded checkout would make an incomplete export look whole.
		states[workspaceKey(w)] = w
	}
	if selected != nil && boundary == "before" {
		w := workspaceAt(selected, "before")
		if w.Repository != "" {
			states[workspaceKey(w)] = w
		}
	}
	if selected != nil && selected.activity.WorkspaceAfter == nil && boundary == "after" && selected.task.Status != "succeeded" {
		w := workspaceAt(selected, "after")
		if w.Repository != "" {
			states[workspaceKey(w)] = w
		}
	}
	keys := make([]string, 0, len(states))
	for k := range states {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		w := states[k]
		if q.Workspace != "" && q.Workspace != w.ScopeID && q.Workspace != w.Cell {
			continue
		}
		if w.Availability == "available" && w.HeadCommit != "" && w.HeadCommit != w.BaseCommit {
			// Locate the pack by its recorded head/base/scope, not by the last
			// artifact with the same reserved filename anywhere in the job.
			for i := len(attempts) - 1; i >= 0; i-- {
				a := attempts[i]
				if a.task.Status != "succeeded" && a != selected {
					continue
				}
				for _, edge := range []string{"after", "before"} {
					candidate := workspaceAt(a, edge)
					if candidate.HeadCommit != w.HeadCommit || candidate.BaseCommit != w.BaseCommit || workspaceKey(candidate) != k {
						continue
					}
					arts := a.task.Artifacts
					if edge == "before" {
						arts = a.inputArtifacts
					}
					for _, art := range arts {
						if art.Name == gitstate.ThinPackArtifactName && art.Reference.Stored != nil {
							copy := art
							w.Bundle = &copy
							break
						}
					}
					if w.Bundle == nil && edge == "before" && a.in.Restore != nil {
						w.Bundle = &Artifact{Name: gitstate.ThinPackArtifactName, Reference: artifacts.NewStoredRef(*a.in.Restore), Availability: "stored", Size: a.in.Restore.SizeBytes, Task: a.task.ID, Attempt: a.task.Attempt}
					}
					if w.Bundle != nil {
						break
					}
				}
				if w.Bundle != nil {
					break
				}
			}
			if w.Bundle == nil {
				w.Availability = "unavailable"
				w.Reason = "saved commit has no retained snapshot artifact"
			} else {
				r, err := s.Download(ctx, Request{JobID: q.JobID, Reference: &w.Bundle.Reference.Stored.Key})
				if err != nil {
					w.Availability = "unavailable"
					w.Reason = "snapshot bytes unavailable: " + err.Error()
				} else {
					prereqs, heads, err := BundleHeader(r)
					_ = r.Close()
					if err != nil {
						w.Availability = "unavailable"
						w.Reason = err.Error()
					} else if !contains(heads, w.HeadCommit) {
						w.Availability = "unavailable"
						w.Reason = "bundle does not advertise the recorded target commit"
					} else {
						w.Format = "git-bundle"
						w.Prerequisites = prereqs
					}
				}
			}
		}
		w.Directory = "workspaces/" + shortID(k)
		out.Workspaces = append(out.Workspaces, w)
	}
	if q.Workspace != "" && len(out.Workspaces) == 0 {
		return nil, fmt.Errorf("workspace %q not found at selected boundary", q.Workspace)
	}
	if len(out.Workspaces) == 1 {
		out.Workspaces[0].Directory = "worktree"
	}
	if selected == nil {
		for _, art := range h.resultArtifacts {
			if internalArtifact(art.Name) {
				continue
			}
			art.Path = artifactPath(art)
			out.Artifacts = append(out.Artifacts, art)
		}
	}
	return out, nil
}
func workspaceAt(a *attempt, boundary string) Workspace {
	g := a.in.Git
	w := Workspace{WorkspaceState: coretask.WorkspaceState{Repository: g.BaseRepo, Cell: g.GetWorkspaceCellName(), BaseRef: g.BaseRef, BaseCommit: g.ResolvedBaseHash, HeadCommit: g.PersistHash}, Task: a.task.ID, Attempt: a.task.Attempt, Boundary: boundary, State: "succeeded_attempt", Availability: "available"}
	if g.Workspace != nil {
		w.ScopeID = g.Workspace.ScopeID
		w.EmptyBase = g.Workspace.HasEmptyBase(g.ResolvedBaseHash, g.ParentHash)
	}
	if w.BaseCommit == "" {
		w.BaseCommit = g.ParentHash
	}
	if w.HeadCommit == "" && fullHash(g.BaseRef) {
		w.HeadCommit = g.BaseRef
		w.BaseCommit = g.BaseRef
	}
	if boundary == "before" {
		w.State = "incoming"
		if a.activity.WorkspaceBefore != nil {
			w.WorkspaceState = *a.activity.WorkspaceBefore
		}
	} else {
		if a.task.Status != "succeeded" {
			w.State = "failed_attempt"
		}
		if a.activity.WorkspaceAfter != nil {
			w.WorkspaceState = *a.activity.WorkspaceAfter
		} else if a.activity.Git.PersistHash != "" {
			w.HeadCommit = a.activity.Git.PersistHash
			w.BaseCommit = a.activity.Git.ParentHash
		} else if a.task.Status != "succeeded" {
			w.HeadCommit = ""
			w.EmptyBase = false
		}
	}
	if w.ScopeID == "" {
		w.ScopeID = a.activity.Scope
	}
	if w.HeadCommit == "" && !w.EmptyBase {
		w.Availability = "unavailable"
		w.Reason = "exact checkout was not recorded; refusing to substitute today's ref tip"
	}
	if w.HeadCommit != "" && !fullHash(w.HeadCommit) {
		w.Availability = "unavailable"
		w.Reason = "recorded target is not a full commit hash"
	}
	if w.BaseCommit != "" && !fullHash(w.BaseCommit) {
		w.Availability = "unavailable"
		w.Reason = "recorded base is not a full commit hash"
	}
	return w
}
func workspaceKey(w Workspace) string { return w.Repository + "\x00" + w.ScopeID }
func internalArtifact(name string) bool {
	return name == gitstate.ThinPackArtifactName || name == "diff_from_parent.diff" || name == "diff_from_base.diff"
}
func shortID(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:8]) }
func artifactPath(a Artifact) string {
	producer := a.Task
	if producer == "" {
		producer = "job-result"
	}
	return "artifacts/" + shortID(producer) + fmt.Sprintf("/attempt-%d/", a.Attempt) + a.Name
}
func contains(in []string, v string) bool {
	for _, s := range in {
		if s == v {
			return true
		}
	}
	return false
}
func fullHash(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	_, e := hex.DecodeString(s)
	return e == nil
}

// BundleHeader reads the bounded textual Git bundle header, not its object body.
// Prerequisites come from the bytes, including multiple prerequisites after merges.
func BundleHeader(r io.Reader) (prerequisites, heads []string, err error) {
	s := bufio.NewScanner(io.LimitReader(r, 1<<20))
	s.Buffer(make([]byte, 4096), 64<<10)
	if !s.Scan() || (s.Text() != "# v2 git bundle" && s.Text() != "# v3 git bundle") {
		return nil, nil, fmt.Errorf("unsupported snapshot format: expected Git bundle")
	}
	for s.Scan() {
		line := s.Text()
		if line == "" {
			return prerequisites, heads, nil
		}
		if strings.HasPrefix(line, "@") {
			continue
		}
		parts := strings.SplitN(line, " ", 2)
		hash := strings.TrimPrefix(parts[0], "-")
		if len(parts) != 2 || !fullHash(hash) {
			return nil, nil, fmt.Errorf("invalid bundle header")
		}
		if strings.HasPrefix(parts[0], "-") {
			prerequisites = append(prerequisites, hash)
		} else {
			heads = append(heads, hash)
		}
	}
	return nil, nil, fmt.Errorf("incomplete or oversized bundle header")
}
