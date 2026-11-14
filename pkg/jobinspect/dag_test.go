package jobinspect

import (
	"context"
	"errors"
	"fmt"
	"io"
	"testing"
	"time"

	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
)

type dagAPI struct {
	calls    []Request
	nodes    map[string]FamilyNode
	children map[string][]FamilyNode
	scripted func(context.Context, Request) (*FamilyPage, error)
}

func (a *dagAPI) Inspect(ctx context.Context, q Request) (*Response, error) {
	a.calls = append(a.calls, q)
	if a.scripted != nil {
		p, err := a.scripted(ctx, q)
		if err != nil {
			return nil, err
		}
		if p != nil {
			return &Response{Version: 1, Family: p}, nil
		}
	}
	p := &FamilyPage{Version: 1, Nodes: []FamilyNode{}}
	if q.Operation == "family-node" {
		n, ok := a.nodes[q.JobID]
		if !ok {
			return nil, fmt.Errorf("unavailable")
		}
		p.Nodes = append(p.Nodes, n)
	} else {
		p.Nodes = append(p.Nodes, a.children[q.JobID]...)
	}
	return &Response{Version: 1, Family: p}, nil
}
func (*dagAPI) Download(context.Context, Request) (io.ReadCloser, error) {
	panic("unexpected download")
}
func node(id, parent string) FamilyNode {
	return FamilyNode{ID: id, Tenant: "tenant", Parent: parent, Title: "first " + id}
}
func familyFixture() *dagAPI {
	a := &dagAPI{nodes: map[string]FamilyNode{}, children: map[string][]FamilyNode{}}
	for _, n := range []FamilyNode{node("root", ""), node("parent", "root"), node("selected", "parent"), node("uncle", "root"), node("sibling", "parent"), node("cousin", "uncle"), node("child", "selected")} {
		a.nodes[n.ID] = n
		if n.Parent != "" {
			a.children[n.Parent] = append(a.children[n.Parent], n)
		}
	}
	return a
}
func boundary(d *DAG, id string) DAGBoundary {
	for _, b := range d.Coverage.Children {
		if b.ID == id {
			return b
		}
	}
	return DAGBoundary{}
}
func TestDAGFamilyAndSubtree(t *testing.T) {
	for _, subtree := range []bool{false, true} {
		t.Run(fmt.Sprint(subtree), func(t *testing.T) {
			a := familyFixture()
			o := DefaultDAGOptions()
			o.Subtree = subtree
			d, err := ReadDAG(context.Background(), a, "selected", o)
			require.NoError(t, err)
			require.True(t, d.Coverage.Complete)
			if subtree {
				require.Len(t, d.Nodes, 2)
				require.Equal(t, "not_requested", d.Coverage.Ancestry.State)
				require.False(t, d.Root.Confirmed)
				require.Equal(t, "selected", d.Root.ID)
			} else {
				require.Len(t, d.Nodes, 7)
				require.Len(t, d.Edges, 6)
				require.True(t, d.Root.Confirmed)
				require.Equal(t, "root", d.Root.ID)
			}
			require.Equal(t, len(a.calls), d.Counts.Requests)
			require.Equal(t, len(d.Nodes), d.Counts.Nodes)
			require.Equal(t, len(d.Nodes), d.Counts.ChildPages)
			for _, e := range d.Edges {
				require.Equal(t, "submission", e.Type)
			}
			for _, q := range a.calls {
				require.Equal(t, 1, q.Family.Version)
				require.Equal(t, 100, q.Limit)
			}
		})
	}
}
func TestDAGPaginationDuplicatesAndFirstObservation(t *testing.T) {
	a := &dagAPI{nodes: map[string]FamilyNode{"root": node("root", "")}}
	a.scripted = func(_ context.Context, q Request) (*FamilyPage, error) {
		if q.Operation != "family-children" || q.JobID != "root" {
			return nil, nil
		}
		switch q.Cursor {
		case "":
			return &FamilyPage{Version: 1, Nodes: []FamilyNode{}, NextCursor: "opaque+/="}, nil
		case "opaque+/=":
			return &FamilyPage{Version: 1, Nodes: []FamilyNode{node("child", "root")}, NextCursor: "last"}, nil
		default:
			n := node("child", "root")
			n.Title = "later"
			return &FamilyPage{Version: 1, Nodes: []FamilyNode{n}}, nil
		}
	}
	o := DefaultDAGOptions()
	o.MaxNodes = 2
	d, err := ReadDAG(context.Background(), a, "root", o)
	require.NoError(t, err)
	require.True(t, d.Coverage.Complete)
	require.Len(t, d.Nodes, 2)
	require.Len(t, d.Edges, 1)
	require.Equal(t, 4, d.Counts.ChildPages)
	require.Equal(t, "first child", d.Nodes[0].Title)
	require.Equal(t, "opaque+/=", a.calls[2].Cursor)
	// A fresh invocation observes a new label.
	a.scripted = func(_ context.Context, q Request) (*FamilyPage, error) {
		if q.Operation == "family-node" {
			n := node("root", "")
			n.Title = "refreshed"
			return &FamilyPage{Version: 1, Nodes: []FamilyNode{n}}, nil
		}
		return nil, nil
	}
	d, err = ReadDAG(context.Background(), a, "root", o)
	require.NoError(t, err)
	require.Equal(t, "refreshed", d.Nodes[0].Title)
}
func TestDAGRepeatedCursors(t *testing.T) {
	for _, sequence := range [][]string{{"a", "a"}, {"a", "b", "a"}} {
		a := &dagAPI{nodes: map[string]FamilyNode{"root": node("root", "")}}
		i := 0
		a.scripted = func(_ context.Context, q Request) (*FamilyPage, error) {
			if q.Operation == "family-node" {
				return nil, nil
			}
			next := sequence[i]
			i++
			return &FamilyPage{Version: 1, Nodes: []FamilyNode{}, NextCursor: next}, nil
		}
		d, err := ReadDAG(context.Background(), a, "root", DefaultDAGOptions())
		require.NoError(t, err)
		require.False(t, d.Coverage.Complete)
		require.True(t, d.Root.Confirmed)
		require.Equal(t, "repeated_cursor", boundary(d, "root").Reason)
		require.Equal(t, "a", boundary(d, "root").NextCursor)
		require.Equal(t, len(sequence), d.Counts.ChildPages)
	}
}
func TestDAGFailuresAndGlobalStops(t *testing.T) {
	for _, failure := range []error{errors.New("backend broke"), &jobdb.CellError{Code: "permission_denied", Status: 403}, jobdb.ErrExecutionLeaseLost, context.Canceled} {
		t.Run(failure.Error(), func(t *testing.T) {
			a := familyFixture()
			a.scripted = func(_ context.Context, q Request) (*FamilyPage, error) {
				if q.Operation == "family-children" && q.JobID == "parent" {
					return nil, failure
				}
				return nil, nil
			}
			d, err := ReadDAG(context.Background(), a, "selected", DefaultDAGOptions())
			require.NoError(t, err)
			require.False(t, d.Coverage.Complete)
			require.True(t, d.Root.Confirmed)
			require.Equal(t, FamilyErrorReason(failure), boundary(d, "parent").Reason)
			global := failure == jobdb.ErrExecutionLeaseLost || failure == context.Canceled
			require.Equal(t, !global, boundary(d, "selected").State == "exhausted")
		})
	}
	a := familyFixture()
	delete(a.nodes, "root")
	d, err := ReadDAG(context.Background(), a, "selected", DefaultDAGOptions())
	require.NoError(t, err)
	require.False(t, d.Root.Confirmed)
	require.Equal(t, "failed", d.Coverage.Ancestry.State)
	require.Equal(t, "parent", d.Root.ID)
	require.Len(t, d.Nodes, 4) // selected,parent,sibling,child
}
func TestDAGAnomaliesAndCycles(t *testing.T) {
	cases := []struct {
		name   string
		n      FamilyNode
		reason string
	}{
		{"foreign", FamilyNode{ID: "foreign", Tenant: "another", Parent: "root"}, "invalid_identity"},
		{"empty", node("", "root"), "invalid_identity"},
		{"mismatch", node("stranger", "wrong"), "parent_mismatch"},
		{"conflict", node("selected", "root"), "conflicting_parent"},
		{"self", node("root", "root"), "cycle"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := familyFixture()
			a.children["root"] = append(a.children["root"], tc.n)
			d, err := ReadDAG(context.Background(), a, "selected", DefaultDAGOptions())
			require.NoError(t, err)
			require.False(t, d.Coverage.Complete)
			require.Equal(t, tc.reason, d.Coverage.Anomalies[0].Reason)
			require.Len(t, d.Nodes, 7)
			require.Len(t, d.Edges, 6)
		})
	}
	for _, nodes := range []map[string]FamilyNode{{"selected": node("selected", "selected")}, {"selected": node("selected", "parent"), "parent": node("parent", "selected")}} {
		a := &dagAPI{nodes: nodes}
		d, err := ReadDAG(context.Background(), a, "selected", DefaultDAGOptions())
		require.NoError(t, err)
		require.False(t, d.Root.Confirmed)
		require.False(t, d.Coverage.Complete)
		require.Equal(t, "cycle", d.Coverage.Ancestry.Reason)
	}
	// A consistent cyclic observation reaches the edge validator.
	a := &dagAPI{nodes: map[string]FamilyNode{"selected": node("selected", "selected")}, children: map[string][]FamilyNode{"selected": {node("selected", "selected")}}}
	d, err := ReadDAG(context.Background(), a, "selected", DefaultDAGOptions())
	require.NoError(t, err)
	require.Empty(t, d.Edges)
	require.Equal(t, "cycle", boundaryAnomaly(d))
}
func boundaryAnomaly(d *DAG) string { return d.Coverage.Anomalies[len(d.Coverage.Anomalies)-1].Reason }
func TestDAGBudgets(t *testing.T) {
	cases := []struct {
		name, reason string
		change       func(*DAGOptions)
	}{
		{"nodes", "node_limit", func(o *DAGOptions) { o.MaxNodes = 4 }},
		{"midpage", "node_limit", func(o *DAGOptions) { o.Subtree = true; o.MaxNodes = 2 }},
		{"ancestors", "ancestor_limit", func(o *DAGOptions) { o.ancestorLimit = 1 }},
		{"depth", "depth_limit", func(o *DAGOptions) { o.MaxDepth = 1 }},
		{"requests", "request_limit", func(o *DAGOptions) { o.requestLimit = 4 }},
		{"pages", "page_limit", func(o *DAGOptions) { o.pageLimit = 1 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := familyFixture()
			o := DefaultDAGOptions()
			id := "selected"
			if tc.name == "midpage" {
				id = "root"
			}
			tc.change(&o)
			d, err := ReadDAG(context.Background(), a, id, o)
			require.NoError(t, err)
			require.False(t, d.Coverage.Complete)
			found := d.Coverage.Ancestry.Reason == tc.reason
			for _, b := range d.Coverage.Children {
				found = found || b.Reason == tc.reason
			}
			require.True(t, found, "missing %s: %+v", tc.reason, d.Coverage)
			require.LessOrEqual(t, d.Counts.Nodes, d.Limits.MaxNodes)
			require.LessOrEqual(t, d.Counts.Requests, d.Limits.Requests)
			require.LessOrEqual(t, d.Counts.ChildPages, d.Limits.ChildPages)
			require.LessOrEqual(t, d.Counts.AncestorHops, d.Limits.AncestorHops)
			require.Contains(t, d.Nodes, a.nodes[id])
		})
	}
	// Equality alone is not exhaustion: two reads completely cover a single root.
	a := &dagAPI{nodes: map[string]FamilyNode{"root": node("root", "")}}
	o := DefaultDAGOptions()
	o.MaxNodes = 1
	o.pageLimit = 1
	o.requestLimit = 2
	d, err := ReadDAG(context.Background(), a, "root", o)
	require.NoError(t, err)
	require.True(t, d.Coverage.Complete)
	// Failed calls count, and remaining branches stop at the request cap.
	a = familyFixture()
	a.scripted = func(_ context.Context, q Request) (*FamilyPage, error) {
		if q.Operation == "family-children" {
			return nil, errors.New("read failure")
		}
		return nil, nil
	}
	o = DefaultDAGOptions()
	o.requestLimit = 4
	d, err = ReadDAG(context.Background(), a, "selected", o)
	require.NoError(t, err)
	require.Equal(t, 4, d.Counts.Requests)
	require.Equal(t, "request_limit", boundary(d, "parent").Reason)
}
func TestDAGTimeoutAndObservation(t *testing.T) {
	a := familyFixture()
	o := DefaultDAGOptions()
	o.Timeout = 10 * time.Millisecond
	a.scripted = func(ctx context.Context, q Request) (*FamilyPage, error) {
		if q.Operation == "family-children" {
			<-ctx.Done()
			return nil, ctx.Err()
		}
		return nil, nil
	}
	d, err := ReadDAG(context.Background(), a, "selected", o)
	require.NoError(t, err)
	require.Equal(t, 4, d.Counts.Requests)
	require.Equal(t, "timeout", boundary(d, "root").Reason)
	require.False(t, d.Observed.FinishedAt.Before(d.Observed.StartedAt))
	// Deterministic clock expiry between accepted observations makes no new call.
	a = familyFixture()
	tick := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	calls := 0
	o = DefaultDAGOptions()
	o.now = func() time.Time {
		calls++
		if calls > 2 {
			return tick.Add(time.Hour)
		}
		return tick
	}
	d, err = ReadDAG(context.Background(), a, "selected", o)
	require.NoError(t, err)
	require.Equal(t, 1, d.Counts.Requests)
	require.Equal(t, "timeout", d.Coverage.Ancestry.Reason)
	require.Equal(t, tick, d.Observed.StartedAt)
	require.Equal(t, tick.Add(time.Hour), d.Observed.FinishedAt)
}
func TestDAGEntryAndProtocolFailures(t *testing.T) {
	for _, bad := range []*FamilyPage{nil, {Version: 2, Nodes: []FamilyNode{}}, {Version: 1}, {Version: 1, Nodes: []FamilyNode{}}, {Version: 1, Nodes: []FamilyNode{node("wrong", "")}}, {Version: 1, Nodes: []FamilyNode{node("selected", "")}, NextCursor: "unexpected"}} {
		a := &dagAPI{scripted: func(context.Context, Request) (*FamilyPage, error) {
			if bad == nil {
				return nil, errors.New("unavailable")
			}
			return bad, nil
		}}
		_, err := ReadDAG(context.Background(), a, "selected", DefaultDAGOptions())
		require.Error(t, err)
	}
	a := familyFixture()
	o := DefaultDAGOptions()
	o.MaxNodes = 0
	_, err := ReadDAG(context.Background(), a, "selected", o)
	require.Error(t, err)
	require.Empty(t, a.calls)
}

func TestDAGDepthAndPendingPages(t *testing.T) {
	a := familyFixture()
	o := DefaultDAGOptions()
	o.MaxDepth = 1
	d, err := ReadDAG(context.Background(), a, "selected", o)
	require.NoError(t, err)
	require.Equal(t, "depth_limit", boundary(d, "selected").Reason)
	require.Equal(t, "depth_limit", boundary(d, "parent").Reason)
	require.Equal(t, "exhausted", boundary(d, "root").State)
	require.Equal(t, 3, d.Counts.AncestorHops+1)
	// Missing ancestor retention capacity globally leaves all listings unfinished.
	o = DefaultDAGOptions()
	o.MaxNodes = 1
	d, err = ReadDAG(context.Background(), a, "selected", o)
	require.NoError(t, err)
	require.Len(t, d.Nodes, 1)
	require.Equal(t, "node_limit", boundary(d, "selected").Reason)
	require.False(t, d.Root.Confirmed)
	// Empty pages count, keep their opaque continuation, and leave queued nodes limited.
	a = familyFixture()
	a.scripted = func(_ context.Context, q Request) (*FamilyPage, error) {
		if q.Operation == "family-children" {
			return &FamilyPage{Version: 1, Nodes: []FamilyNode{}, NextCursor: "opaque+/="}, nil
		}
		return nil, nil
	}
	o = DefaultDAGOptions()
	o.pageLimit = 1
	d, err = ReadDAG(context.Background(), a, "selected", o)
	require.NoError(t, err)
	require.Equal(t, 1, d.Counts.ChildPages)
	require.Equal(t, "opaque+/=", boundary(d, "root").NextCursor)
	for _, b := range d.Coverage.Children {
		require.Equal(t, "page_limit", b.Reason)
	}
}

func TestDAGDeadlineBeforeParentAttemptDoesNotCountHop(t *testing.T) {
	a := familyFixture()
	o := DefaultDAGOptions()
	tick := time.Now().UTC()
	ticks := 0
	o.now = func() time.Time {
		ticks++
		if ticks >= 4 {
			return tick.Add(time.Hour)
		}
		return tick
	}
	d, err := ReadDAG(context.Background(), a, "selected", o)
	require.NoError(t, err)
	require.Equal(t, 1, d.Counts.Requests)
	require.Zero(t, d.Counts.AncestorHops)
	require.Zero(t, d.Counts.ChildPages)
	require.Equal(t, "limited", d.Coverage.Ancestry.State)
	require.Equal(t, "timeout", d.Coverage.Ancestry.Reason)
}
func TestDAGSubtreeSelfParentAndDescendantConflict(t *testing.T) {
	a := &dagAPI{nodes: map[string]FamilyNode{"selected": node("selected", "selected")}}
	o := DefaultDAGOptions()
	o.Subtree = true
	d, err := ReadDAG(context.Background(), a, "selected", o)
	require.NoError(t, err)
	require.False(t, d.Coverage.Complete)
	require.False(t, d.Root.Confirmed)
	require.Equal(t, "cycle", d.Coverage.Anomalies[0].Reason)
	a = familyFixture()
	a.children["uncle"] = append(a.children["uncle"], node("child", "uncle"))
	d, err = ReadDAG(context.Background(), a, "selected", DefaultDAGOptions())
	require.NoError(t, err)
	require.False(t, d.Coverage.Complete)
	require.True(t, d.Root.Confirmed)
	require.Equal(t, "conflicting_parent", d.Coverage.Anomalies[0].Reason)
}

func TestDAGRejectsSuccessfulResponseAfterCancellation(t *testing.T) {
	a := familyFixture()
	o := DefaultDAGOptions()
	o.Timeout = time.Millisecond
	a.scripted = func(ctx context.Context, q Request) (*FamilyPage, error) {
		<-ctx.Done()
		return &FamilyPage{Version: 1, Nodes: []FamilyNode{node(q.JobID, "")}}, nil
	}
	_, err := ReadDAG(context.Background(), a, "selected", o)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Len(t, a.calls, 1)
}
func TestDAGInvalidAncestorFromSummaryService(t *testing.T) {
	rt := &familyRuntime{page: jobdb.ListJobsResponse{Jobs: []jobdb.JobSummary{{JobKey: jobdb.JobKey{TenantId: "foreign", JobId: "root"}}}}}
	service := &Service{Runtime: rt, Tenant: "tenant"}
	a := familyFixture()
	a.nodes["selected"] = node("selected", "root")
	a.scripted = func(ctx context.Context, q Request) (*FamilyPage, error) {
		if q.Operation == "family-node" && q.JobID == "root" {
			_, err := service.Inspect(ctx, q)
			return nil, err
		}
		return nil, nil
	}
	d, err := ReadDAG(context.Background(), a, "selected", DefaultDAGOptions())
	require.NoError(t, err)
	require.False(t, d.Root.Confirmed)
	require.Equal(t, "invalid_identity", d.Coverage.Ancestry.Reason)
	require.Equal(t, "invalid_identity", d.Coverage.Anomalies[0].Reason)
}
