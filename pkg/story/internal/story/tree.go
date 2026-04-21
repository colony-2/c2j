package story

import (
	"fmt"
	"time"

	recipeartifacts "github.com/colony-2/c2j/pkg/artifacts"
	"github.com/colony-2/c2j/pkg/story/internal/model"
	"github.com/colony-2/swf-go/pkg/swf"
)

type treeBuilder struct {
	nextID   int64
	segments []string
	stack    []*model.JobRunStoryNode
}

func newTreeBuilder() *treeBuilder {
	return &treeBuilder{
		nextID:   1,
		segments: make([]string, 0, 16),
		stack:    make([]*model.JobRunStoryNode, 0, 16),
	}
}

func (b *treeBuilder) newNode(kind model.JobRunStoryNodeKind, title string) *model.JobRunStoryNode {
	id := fmt.Sprintf("n_%d", b.nextID)
	b.nextID++
	return &model.JobRunStoryNode{
		ID:            id,
		Kind:          kind,
		Title:         title,
		Status:        model.JobRunStoryNodeStatusUnknown,
		StartedAt:     nil,
		FinishedAt:    nil,
		Path:          make([]string, 0, 8),
		InvokeSeq:     0,
		Attempt:       1,
		PriorAttempts: make([]*model.JobRunStoryNode, 0),
		Input:         nil,
		Output:        nil,
		ArtifactKeys:  make([]swf.ArtifactKey, 0),
		ArtifactRefs:  make([]recipeartifacts.Ref, 0),
		Children:      make([]*model.JobRunStoryNode, 0),
	}
}

func (b *treeBuilder) push(segment string, n *model.JobRunStoryNode) {
	if len(b.stack) > 0 {
		parent := b.stack[len(b.stack)-1]
		parent.Children = append(parent.Children, n)
	}
	b.segments = append(b.segments, segment)
	b.stack = append(b.stack, n)
}

func (b *treeBuilder) pop() *model.JobRunStoryNode {
	if len(b.stack) == 0 {
		return nil
	}
	n := b.stack[len(b.stack)-1]
	b.stack = b.stack[:len(b.stack)-1]
	if len(b.segments) > 0 {
		b.segments = b.segments[:len(b.segments)-1]
	}
	return n
}

func (b *treeBuilder) current() *model.JobRunStoryNode {
	if len(b.stack) == 0 {
		return nil
	}
	return b.stack[len(b.stack)-1]
}

func inferFinishedTimes(root *model.JobRunStoryNode, jobFinishedAt *time.Time) {
	if root == nil {
		return
	}
	inferFinishedTimesRec(root, jobFinishedAt)
}

func inferStartedTimes(root *model.JobRunStoryNode) {
	if root == nil {
		return
	}
	inferStartedTimesRec(root)
}

func inferStartedTimesRec(n *model.JobRunStoryNode) {
	if n == nil {
		return
	}
	for _, ch := range n.Children {
		inferStartedTimesRec(ch)
	}

	if n.StartedAt != nil {
		return
	}

	earliest := (*time.Time)(nil)
	for _, ch := range n.Children {
		if ch == nil || ch.StartedAt == nil || ch.StartedAt.IsZero() {
			continue
		}
		if earliest == nil || ch.StartedAt.Before(*earliest) {
			t := *ch.StartedAt
			earliest = &t
		}
	}
	if earliest != nil {
		t := *earliest
		n.StartedAt = &t
	}
}

func inferFinishedTimesRec(n *model.JobRunStoryNode, jobFinishedAt *time.Time) {
	if n == nil {
		return
	}
	for _, ch := range n.Children {
		inferFinishedTimesRec(ch, jobFinishedAt)
	}

	// Fill sibling-based finished_at first.
	for i := 0; i < len(n.Children)-1; i++ {
		cur := n.Children[i]
		next := n.Children[i+1]
		if cur == nil || next == nil {
			continue
		}
		if cur.FinishedAt != nil {
			continue
		}
		if !isTerminal(cur.Status) {
			continue
		}
		if next.StartedAt != nil {
			t := *next.StartedAt
			cur.FinishedAt = &t
		}
	}

	// For the last child, use parent's finished_at (if known) or job finished_at.
	if len(n.Children) > 0 {
		last := n.Children[len(n.Children)-1]
		if last != nil && last.FinishedAt == nil && isTerminal(last.Status) {
			if n.FinishedAt != nil {
				t := *n.FinishedAt
				last.FinishedAt = &t
			} else if jobFinishedAt != nil {
				t := *jobFinishedAt
				last.FinishedAt = &t
			}
		}
	}

	// If this node is terminal and has no finished_at, derive from children or job finished time.
	if n.FinishedAt == nil && isTerminal(n.Status) {
		if len(n.Children) > 0 {
			last := n.Children[len(n.Children)-1]
			if last != nil {
				if last.FinishedAt != nil {
					t := *last.FinishedAt
					n.FinishedAt = &t
				} else if last.StartedAt != nil {
					t := *last.StartedAt
					n.FinishedAt = &t
				}
			}
		} else if jobFinishedAt != nil {
			// Only apply the job-finished fallback to leaf nodes, otherwise it tends to overfill.
			t := *jobFinishedAt
			n.FinishedAt = &t
		}
	}
}

func isTerminal(st model.JobRunStoryNodeStatus) bool {
	switch st {
	case model.JobRunStoryNodeStatusSucceeded, model.JobRunStoryNodeStatusFailed, model.JobRunStoryNodeStatusCanceled, model.JobRunStoryNodeStatusSkipped:
		return true
	default:
		return false
	}
}
