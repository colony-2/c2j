package jobinspect

import (
	"context"
	"fmt"
	"sort"
	"time"
)

type DAGOptions struct {
	Subtree  bool
	MaxNodes int
	MaxDepth int
	Timeout  time.Duration
	// Tests may lower internal safety caps and control observation time. Callers
	// cannot raise the CLI's fixed request, page or ancestor caps.
	now                                    func() time.Time
	ancestorLimit, pageLimit, requestLimit int
}

func DefaultDAGOptions() DAGOptions {
	return DAGOptions{MaxNodes: 1000, MaxDepth: 64, Timeout: 30 * time.Second}
}
func (o DAGOptions) Validate() error {
	if o.MaxNodes < 1 || o.MaxDepth < 1 || o.Timeout <= 0 {
		return fmt.Errorf("max-nodes, max-depth and timeout must be positive")
	}
	return nil
}

type DAGIdentity struct {
	Tenant string `json:"tenant"`
	ID     string `json:"id"`
}
type SubmissionEdge struct {
	Type   string `json:"type"`
	Parent string `json:"parent"`
	Child  string `json:"child"`
}
type DAGRoot struct {
	ID        string `json:"id"`
	Confirmed bool   `json:"confirmed"`
}
type DAGObservation struct {
	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at"`
}
type DAGLimits struct {
	MaxNodes     int           `json:"max_nodes"`
	MaxDepth     int           `json:"max_depth"`
	Timeout      time.Duration `json:"timeout_ns"`
	AncestorHops int           `json:"ancestor_hops"`
	ChildPages   int           `json:"child_pages"`
	Requests     int           `json:"requests"`
	PageSize     int           `json:"page_size"`
}
type DAGCounts struct {
	Nodes        int `json:"nodes"`
	AncestorHops int `json:"ancestor_hops"`
	ChildPages   int `json:"child_pages"`
	Requests     int `json:"requests"`
}
type DAGBoundary struct {
	ID         string `json:"id"`
	State      string `json:"state"`
	Reason     string `json:"reason"`
	NextCursor string `json:"next_cursor"`
}
type DAGAnomaly struct {
	ID     string `json:"id"`
	Parent string `json:"parent"`
	Reason string `json:"reason"`
}
type DAGCoverage struct {
	Complete  bool          `json:"complete"`
	Ancestry  DAGBoundary   `json:"ancestry"`
	Children  []DAGBoundary `json:"children"`
	Anomalies []DAGAnomaly  `json:"anomalies"`
}
type DAG struct {
	Version  int              `json:"version"`
	Selected DAGIdentity      `json:"selected"`
	Scope    string           `json:"scope"`
	Nodes    []FamilyNode     `json:"nodes"`
	Edges    []SubmissionEdge `json:"edges"`
	Root     DAGRoot          `json:"root"`
	Observed DAGObservation   `json:"observed"`
	Limits   DAGLimits        `json:"limits"`
	Counts   DAGCounts        `json:"counts"`
	Coverage DAGCoverage      `json:"coverage"`
}

type dagReader struct {
	api      API
	ctx      context.Context
	now      func() time.Time
	deadline time.Time
	result   *DAG
	stop     string
}

func (r *dagReader) budget() string {
	if r.stop != "" {
		return r.stop
	}
	if err := r.ctx.Err(); err != nil {
		r.stop = FamilyErrorReason(err)
	} else if !r.now().Before(r.deadline) {
		r.stop = "timeout"
	} else if r.result.Counts.Requests >= r.result.Limits.Requests {
		r.stop = "request_limit"
	}
	return r.stop
}
func (r *dagReader) read(op, id, cursor string) (*FamilyPage, error) {
	if reason := r.budget(); reason != "" {
		return nil, fmt.Errorf("discovery stopped: %s", reason)
	}
	r.result.Counts.Requests++
	if op == "family-children" {
		r.result.Counts.ChildPages++
	} else if r.result.Selected.ID != "" {
		r.result.Counts.AncestorHops++
	}
	out, err := r.api.Inspect(r.ctx, Request{Operation: op, JobID: id, Limit: r.result.Limits.PageSize, Cursor: cursor, Family: &FamilyOptions{Version: 1}})
	if err == nil {
		err = r.ctx.Err()
	}
	if err != nil {
		switch reason := FamilyErrorReason(err); reason {
		case "lease_lost", "canceled", "timeout":
			r.stop = reason
		}
		return nil, err
	}
	if err = ValidateFamilyResponse(out); err != nil {
		return nil, err
	}
	return out.Family, nil
}
func (r *dagReader) reason(err error) string {
	if r.stop != "" {
		return r.stop
	}
	return FamilyErrorReason(err)
}

// ReadDAG observes a bounded submission family. It returns an error only before
// retaining the entry. Later failures are explicit coverage in the usable DAG.
func ReadDAG(ctx context.Context, api API, id string, options DAGOptions) (*DAG, error) {
	if err := options.Validate(); err != nil {
		return nil, err
	}
	if ctx == nil || api == nil || id == "" || id == "self" {
		return nil, fmt.Errorf("DAG inspection requires context, API and an explicit job ID")
	}
	now := options.now
	if now == nil {
		now = time.Now
	}
	started := now().UTC()
	ctx, cancel := context.WithTimeout(ctx, options.Timeout)
	defer cancel()
	limits := DAGLimits{MaxNodes: options.MaxNodes, MaxDepth: options.MaxDepth, Timeout: options.Timeout, AncestorHops: 64, ChildPages: 200, Requests: 300, PageSize: 100}
	if options.ancestorLimit > 0 {
		limits.AncestorHops = options.ancestorLimit
	}
	if options.pageLimit > 0 {
		limits.ChildPages = options.pageLimit
	}
	if options.requestLimit > 0 {
		limits.Requests = options.requestLimit
	}
	d := &DAG{Version: 1, Scope: "family", Nodes: []FamilyNode{}, Edges: []SubmissionEdge{}, Observed: DAGObservation{StartedAt: started}, Limits: limits, Coverage: DAGCoverage{Children: []DAGBoundary{}, Anomalies: []DAGAnomaly{}}}
	r := &dagReader{api: api, ctx: ctx, now: now, deadline: started.Add(options.Timeout), result: d}
	page, err := r.read("family-node", id, "")
	if err != nil {
		return nil, fmt.Errorf("read selected job %q: %w", id, err)
	}
	if len(page.Nodes) != 1 || page.NextCursor != "" || page.Nodes[0].ID != id || page.Nodes[0].Tenant == "" {
		return nil, fmt.Errorf("invalid selected job identity or exact family response")
	}
	entry := page.Nodes[0]
	d.Selected = DAGIdentity{Tenant: entry.Tenant, ID: id}
	nodes := map[string]FamilyNode{id: entry}
	parents := map[string]string{} // accepted edges only
	chain := []string{id}          // entry to oldest retained ancestor
	anomaly := func(n FamilyNode, reason string) {
		d.Coverage.Anomalies = append(d.Coverage.Anomalies, DAGAnomaly{ID: n.ID, Parent: n.Parent, Reason: reason})
	}
	cycle := func(parent, child string) bool {
		for p := parent; p != ""; p = parents[p] {
			if p == child {
				return true
			}
		}
		return false
	}
	current := entry
	ancestry := DAGBoundary{ID: id, State: "exhausted"}
	if options.Subtree {
		d.Scope = "subtree"
		ancestry.State = "not_requested"
		if entry.Parent == entry.ID {
			anomaly(entry, "cycle")
		}
	} else {
		for current.Parent != "" {
			ancestry.ID = current.ID
			if _, exists := nodes[current.Parent]; exists {
				anomaly(current, "cycle")
				ancestry.State, ancestry.Reason = "failed", "cycle"
				break
			}
			if d.Counts.AncestorHops >= limits.AncestorHops {
				ancestry.State, ancestry.Reason = "limited", "ancestor_limit"
				break
			}
			if len(nodes) >= limits.MaxNodes {
				r.stop = "node_limit"
				ancestry.State, ancestry.Reason = "limited", r.stop
				break
			}
			if reason := r.budget(); reason != "" {
				ancestry.State, ancestry.Reason = "limited", reason
				break
			}
			before := d.Counts.Requests
			p, e := r.read("family-node", current.Parent, "")
			if e != nil {
				ancestry.State, ancestry.Reason = "failed", r.reason(e)
				if ancestry.Reason == "invalid_identity" {
					anomaly(FamilyNode{ID: current.Parent}, "invalid_identity")
				}
				if d.Counts.Requests == before {
					ancestry.State = "limited"
				}
				break
			}
			if len(p.Nodes) != 1 || p.NextCursor != "" || p.Nodes[0].ID != current.Parent || p.Nodes[0].Tenant != entry.Tenant {
				ancestry.State, ancestry.Reason = "failed", "invalid_identity"
				anomaly(FamilyNode{ID: current.Parent}, "invalid_identity")
				break
			}
			parent := p.Nodes[0]
			nodes[parent.ID] = parent
			parents[current.ID] = parent.ID
			chain = append(chain, parent.ID)
			current = parent
		}
		if ancestry.State == "exhausted" {
			ancestry.ID = current.ID
		}
	}
	d.Root = DAGRoot{ID: chain[len(chain)-1], Confirmed: !options.Subtree && ancestry.State == "exhausted" && current.Parent == ""}
	d.Coverage.Ancestry = ancestry
	chainIDs := map[string]bool{}
	for _, id := range chain {
		chainIDs[id] = true
	}
	type queued struct {
		id    string
		depth int
	}
	queue := []queued{}
	for i := len(chain) - 1; i >= 0; i-- {
		queue = append(queue, queued{chain[i], len(chain) - 1 - i})
	}
	boundaries := map[string]DAGBoundary{}
	for qi := 0; qi < len(queue); qi++ {
		item := queue[qi]
		b := DAGBoundary{ID: item.id, State: "not_attempted"}
		if reason := r.budget(); reason != "" {
			b.State, b.Reason = "limited", reason
			boundaries[item.id] = b
			continue
		}
		if item.depth >= limits.MaxDepth {
			b.State, b.Reason = "limited", "depth_limit"
			boundaries[item.id] = b
			continue
		}
		seenCursors := map[string]bool{"": true}
		for {
			if d.Counts.ChildPages >= limits.ChildPages {
				r.stop = "page_limit"
				b.State, b.Reason = "limited", r.stop
				break
			}
			if reason := r.budget(); reason != "" {
				b.State, b.Reason = "limited", reason
				break
			}
			before := d.Counts.Requests
			p, e := r.read("family-children", item.id, b.NextCursor)
			if e != nil {
				b.State, b.Reason = "failed", r.reason(e)
				if d.Counts.Requests == before {
					b.State = "limited"
				}
				break
			}
			for _, n := range p.Nodes {
				if n.ID == "" || n.Tenant != entry.Tenant {
					anomaly(n, "invalid_identity")
					continue
				}
				if n.ID == n.Parent {
					anomaly(n, "cycle")
					if chainIDs[n.ID] {
						d.Root.Confirmed = false
					}
					continue
				}
				if old, ok := nodes[n.ID]; ok && old.Parent != n.Parent {
					anomaly(n, "conflicting_parent")
					if chainIDs[n.ID] {
						d.Root.Confirmed = false
					}
					continue
				}
				if n.Parent != item.id {
					anomaly(n, "parent_mismatch")
					continue
				}
				if cycle(item.id, n.ID) {
					anomaly(n, "cycle")
					if chainIDs[n.ID] {
						d.Root.Confirmed = false
					}
					continue
				}
				if _, ok := nodes[n.ID]; !ok {
					if len(nodes) >= limits.MaxNodes {
						r.stop = "node_limit"
						break
					}
					nodes[n.ID] = n
					// Chain nodes are seeded before discovery. Insert newly found
					// siblings before deeper chain nodes to preserve breadth first order.
					at := qi + 1
					for at < len(queue) && queue[at].depth <= item.depth+1 {
						at++
					}
					queue = append(queue, queued{})
					copy(queue[at+1:], queue[at:])
					queue[at] = queued{n.ID, item.depth + 1}
				}
				parents[n.ID] = item.id
			}
			if r.stop != "" {
				b.State, b.Reason = "limited", r.stop
				b.NextCursor = p.NextCursor
				break
			}
			b.NextCursor = p.NextCursor
			if p.NextCursor == "" {
				b.State = "exhausted"
				break
			}
			if seenCursors[p.NextCursor] {
				b.State, b.Reason = "failed", "repeated_cursor"
				break
			}
			seenCursors[p.NextCursor] = true
		}
		boundaries[item.id] = b
	}
	d.Coverage.Complete = (ancestry.State == "exhausted" || ancestry.State == "not_requested") && len(d.Coverage.Anomalies) == 0
	for _, n := range nodes {
		d.Nodes = append(d.Nodes, n)
		b := boundaries[n.ID]
		d.Coverage.Children = append(d.Coverage.Children, b)
		if b.State != "exhausted" {
			d.Coverage.Complete = false
		}
	}
	for child, parent := range parents {
		d.Edges = append(d.Edges, SubmissionEdge{Type: "submission", Parent: parent, Child: child})
	}
	sort.Slice(d.Nodes, func(i, j int) bool { return d.Nodes[i].ID < d.Nodes[j].ID })
	sort.Slice(d.Edges, func(i, j int) bool {
		a, b := d.Edges[i], d.Edges[j]
		if a.Parent != b.Parent {
			return a.Parent < b.Parent
		}
		return a.Child < b.Child
	})
	sort.Slice(d.Coverage.Children, func(i, j int) bool { return d.Coverage.Children[i].ID < d.Coverage.Children[j].ID })
	d.Counts.Nodes = len(nodes)
	d.Observed.FinishedAt = now().UTC()
	return d, nil
}
