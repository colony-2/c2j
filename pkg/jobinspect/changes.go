package jobinspect

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"sort"
	"strings"
	"sync"
)

func changesDigest(v any) string {
	b, _ := json.Marshal(v)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

type changesRead struct {
	o          *changesObjects
	id         ChangesIdentity
	commits    []ChangesCommit
	diagnostic *ChangesError
}

func (c *ChangesService) prepare(ctx context.Context, q ChangesRequest) (r *changesRead, err error) {
	o, err := c.objects(ctx)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			if o.ctx.Err() != nil {
				err = ChangesDiagnostic(o.ctx.Err())
			}
			o.close()
		}
	}()
	h, v, candidates, err := c.selectLatest(o.ctx, q)
	if err != nil {
		return nil, err
	}
	w := v.w
	o.lineageExamined = v.lineageScans
	id := ChangesIdentity{Version: ChangesVersion, Tenant: c.service.Tenant, JobID: q.JobID, Repository: w.Repository, Cell: w.Cell, Scope: w.ScopeID, State: candidateProducer(q.JobID, v), Boundary: w.Boundary, ActiveBase: w.BaseCommit, Base: w.BaseCommit, BaseOrigin: "active", EmptyBase: w.EmptyBase, Head: w.HeadCommit, Sources: []ChangesSource{}}
	id.ActiveBaseOrigin = changesBaseOrigin(v)
	if q.Base != "" {
		id.Base = q.Base
		id.BaseOrigin = "explicit"
		id.EmptyBase = false
	}
	preID := changesDigest(struct {
		Identity ChangesIdentity
		Primary  *Artifact
	}{id, v.art})
	if q.Selection != "" && !strings.HasPrefix(q.Selection, preID+".") {
		return nil, changesErr("stale_selection", "latest main-workspace state changed; refresh Changes")
	}
	format := "sha1"
	if len(w.HeadCommit) == 64 || len(w.BaseCommit) == 64 {
		format = "sha256"
	}
	if id.Base != "" && len(id.Base) != map[string]int{"sha1": 40, "sha256": 64}[format] {
		return nil, changesErr("invalid_objects", "comparison base object format disagrees")
	}
	// Existing empty-state evidence does not encode the object format of an
	// unborn repository. Do not invent a SHA-1 empty comparison for unknown data.
	if id.Head == "" {
		if !w.EmptyBase || q.Base != "" {
			return nil, changesErr("workspace_unavailable", "unborn workspace cannot resolve an explicit comparison")
		}
		id.Unborn = true
		id.SourceID = changesDigest(id.Sources)
		id.Selection = preID + "." + changesDigest(id)
		if q.Selection != "" && q.Selection != id.Selection {
			return nil, changesErr("stale_selection", "unborn workspace selection changed")
		}
		return &changesRead{o: o, id: id, commits: []ChangesCommit{}}, nil
	}
	id.ObjectFormat = format
	o.repositoryIdentity = w.Repository
	if err = o.init(format); err != nil {
		return nil, err
	}
	if v.art != nil {
		b, e := o.bundle(h, v)
		if e != nil {
			return nil, e
		}
		if b.format != format {
			return nil, changesErr("invalid_objects", "bundle format contradicts recorded endpoints")
		}
		if err = o.loadBundle(h, b, candidates, 0); err != nil {
			return nil, err
		}
	}
	// Missing endpoint objects use eligible retained lineage first, then the
	// deliberately configured recorded repository. Offline never opens a path.
	for _, hash := range []string{id.Head, id.Base} {
		if hash == "" {
			continue
		}
		present, e := o.has(hash)
		if e != nil {
			return nil, e
		}
		if present {
			continue
		}
		found := false
		for i := len(candidates) - 1; i >= 0; i-- {
			o.lineageExamined++
			if o.lineageExamined > o.c.config.Limits.LineageCandidates {
				return nil, changesLimit("lineage_candidates")
			}
			prior := candidates[i]
			if prior.order > v.order || prior.art == nil || prior.w.HeadCommit != hash {
				continue
			}
			b, e := o.bundle(h, prior)
			if e != nil {
				return nil, e
			}
			if err = o.loadBundle(h, b, candidates, 0); err != nil {
				return nil, err
			}
			found = true
			break
		}
		if !found {
			if err = o.repository(hash); err != nil {
				return nil, err
			}
		}
	}
	if err = o.checkObjects(); err != nil {
		return nil, err
	}
	if id.EmptyBase {
		var b bytes.Buffer
		if err = o.run(strings.NewReader(""), &b, 1024, "git_output", nil, "hash-object", "-w", "-t", "tree", "--stdin"); err != nil {
			return nil, err
		}
		id.BaseTree = strings.TrimSpace(b.String())
	} else {
		if id.BaseTree, err = o.tree(id.Base); err != nil {
			return nil, err
		}
	}
	if id.HeadTree, err = o.tree(id.Head); err != nil {
		return nil, err
	}
	r = &changesRead{o: o, id: id, commits: []ChangesCommit{}}
	if err = r.chain(); err != nil {
		return nil, err
	}
	// Tree and blob closure is checked for every listed commit and both All
	// endpoints, including gitlinks (which are opaque, never followed).
	trees := map[string]bool{id.BaseTree: true, id.HeadTree: true}
	for _, commit := range r.commits {
		trees[commit.Tree] = true
	}
	for tree := range trees {
		if err = o.checkTree(tree); err != nil {
			return nil, err
		}
	}
	r.id.Sources = sortedChangesSources(o.sources)
	sort.Strings(o.repositoryObjects)
	r.id.RepositoryObjects = append([]string(nil), o.repositoryObjects...)
	r.id.SourceID = changesDigest(struct {
		Sources []ChangesSource
		Objects []string
	}{r.id.Sources, r.id.RepositoryObjects})
	r.id.Selection = preID + "." + changesDigest(r.id)
	if q.Selection != "" && q.Selection != r.id.Selection {
		return nil, changesErr("stale_selection", "latest main-workspace selection changed; refresh Changes")
	}
	return r, nil
}

func (o *changesObjects) commit(hash string) (ChangesCommit, error) {
	if err := o.validateCommit(hash); err != nil {
		return ChangesCommit{}, err
	}
	b, e := o.git("show", "--no-patch", "--no-notes", "--no-show-signature", "--format=%H%x00%T%x00%P%x00%an%x00%ae%x00%aI%x00%cn%x00%ce%x00%cI%x00%s", hash, "--")
	if e != nil {
		// show may reject a present commit because its tree is malformed. Prove
		// absence independently before reporting a missing prerequisite.
		if ChangesDiagnostic(e).Code == "invalid_objects" {
			if err := o.checkTreeObjects([]changesTreeObject{{hash, "commit"}}); err != nil {
				return ChangesCommit{}, err
			}
		}
		return ChangesCommit{}, e
	}
	f := strings.Split(strings.TrimSuffix(string(b), "\n"), "\x00")
	if len(f) != 10 || f[0] != hash || !fullHash(f[1]) {
		return ChangesCommit{}, changesErr("invalid_objects", "invalid commit metadata")
	}
	parents := strings.Fields(f[2])
	for _, p := range parents {
		if !fullHash(p) || len(p) != len(hash) {
			return ChangesCommit{}, changesErr("invalid_objects", "invalid commit parent")
		}
	}
	return ChangesCommit{Hash: hash, Tree: f[1], Parents: parents, Author: ChangesPerson{f[3], f[4], f[5]}, Committer: ChangesPerson{f[6], f[7], f[8]}, Subject: f[9]}, nil
}

func (r *changesRead) chain() error {
	seen := map[string]bool{}
	for hash := r.id.Head; hash != r.id.Base; {
		if hash == "" {
			return changesErr("invalid_range", "selected base is not an ancestor of recorded head")
		}
		if seen[hash] {
			return changesErr("invalid_objects", "commit ancestry cycle")
		}
		seen[hash] = true
		if len(r.commits) >= r.o.c.config.Limits.Commits {
			return changesLimit("commits")
		}
		v, e := r.o.commit(hash)
		if e != nil {
			return e
		}
		if len(v.Parents) > 1 {
			if e := r.validateMergedAncestry(hash, len(r.commits)); e != nil {
				return e
			}
			r.commits = []ChangesCommit{}
			r.diagnostic = changesErr("unsupported_history", "merge inside comparison range; individual and range reads are unavailable")
			return nil
		}
		r.commits = append(r.commits, v)
		if len(v.Parents) == 0 {
			if !r.id.EmptyBase {
				return changesErr("invalid_range", "selected base is not an ancestor of recorded head")
			}
			hash = ""
		} else {
			hash = v.Parents[0]
		}
	}
	for i, j := 0, len(r.commits)-1; i < j; i, j = i+1, j-1 {
		r.commits[i], r.commits[j] = r.commits[j], r.commits[i]
	}
	for i := range r.commits {
		r.commits[i].Position = i + 1
	}
	return nil
}

// A merge is unsupported only after proving the captured ancestry and selected
// boundary. This prevents an off-chain explicit base or missing parent from
// becoming an apparently successful All comparison with a misleading status.
func (r *changesRead) validateMergedAncestry(head string, already int) error {
	todo := []string{head}
	seen := map[string]bool{}
	reached := r.id.EmptyBase
	for len(todo) > 0 {
		hash := todo[len(todo)-1]
		todo = todo[:len(todo)-1]
		if hash == r.id.Base {
			reached = true
			continue
		}
		if seen[hash] {
			continue
		}
		seen[hash] = true
		if already+len(seen) > r.o.c.config.Limits.Commits {
			return changesLimit("commits")
		}
		v, e := r.o.commit(hash)
		if e != nil {
			return e
		}
		todo = append(todo, v.Parents...)
	}
	if !reached {
		return changesErr("invalid_range", "selected base is not an ancestor of recorded head")
	}
	return nil
}

// changesTreeObject records the type required by the referring tree's mode.
type changesTreeObject struct{ hash, kind string }

func (o *changesObjects) checkTree(tree string) error {
	if o.checkedTrees[tree] {
		return nil
	}
	if err := o.checkTreeObjects([]changesTreeObject{{tree, "tree"}}); err != nil {
		return err
	}
	// Prove structure and actual entry types before descending. Git traversal
	// can accept malformed trees or interpret a wrong-type directory as a treeish.
	todo := []string{tree}
	seen := map[string]bool{tree: true}
	for len(todo) > 0 {
		hash := todo[len(todo)-1]
		todo = todo[:len(todo)-1]
		b, err := o.git("cat-file", "tree", hash)
		if err != nil {
			return err
		}
		expected, children, err := decodeChangesTree(b, len(tree)/2)
		if err != nil {
			return err
		}
		if err := o.checkTreeObjects(expected); err != nil {
			return err
		}
		for _, child := range children {
			if !seen[child] && !o.checkedTrees[child] {
				seen[child] = true
				todo = append(todo, child)
			}
		}
	}
	if o.checkedTrees == nil {
		o.checkedTrees = map[string]bool{}
	}
	for hash := range seen {
		o.checkedTrees[hash] = true
	}
	return nil
}

func (o *changesObjects) checkTreeObjects(expected []changesTreeObject) error {
	if len(expected) == 0 {
		return nil
	}
	var input strings.Builder
	for _, obj := range expected {
		input.WriteString(obj.hash + "\n")
	}
	var out bytes.Buffer
	if err := o.run(strings.NewReader(input.String()), &out, o.c.config.Limits.ObjectBytes, "object_bytes", nil, "cat-file", "--batch-check=%(objectname) %(objecttype) %(objectsize)"); err != nil {
		return err
	}
	lines := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
	if len(lines) != len(expected) {
		return changesErr("invalid_objects", "incomplete tree closure inventory")
	}
	for i, line := range lines {
		f := strings.Fields(line)
		if len(f) == 2 && f[0] == expected[i].hash && f[1] == "missing" {
			return missingChangesObjects(f[0])
		}
		if len(f) != 3 || f[0] != expected[i].hash || f[1] != expected[i].kind {
			return changesErr("invalid_objects", "invalid tree closure")
		}
	}
	return nil
}

type changesCursor struct {
	Version                        int
	Tenant, JobID, Selection, Base string
	Limit, Next                    int
}

func (c *ChangesService) History(ctx context.Context, q ChangesRequest) (out *ChangesHistory, err error) {
	defer changesOperation("history", &err)
	if err := normalizeChangesRequest(&q, c.config.Limits); err != nil {
		return nil, err
	}
	var cursor changesCursor
	if q.Cursor != "" {
		b, e := base64.RawURLEncoding.DecodeString(q.Cursor)
		if e != nil || json.Unmarshal(b, &cursor) != nil {
			return nil, changesErr("invalid_cursor", "malformed continuation")
		}
		if cursor.Version != ChangesVersion {
			return nil, changesErr("protocol", "unsupported cursor version")
		}
		if cursor.Tenant != c.service.Tenant || cursor.JobID != q.JobID || cursor.Base != q.Base || cursor.Limit != q.Limit || cursor.Next < 1 || cursor.Selection == "" || (q.Selection != "" && q.Selection != cursor.Selection) {
			return nil, changesErr("invalid_cursor", "cursor does not match request")
		}
		q.Selection = cursor.Selection
	}
	r, err := c.prepare(ctx, q)
	if err != nil {
		return nil, err
	}
	defer r.o.close()
	h := &ChangesHistory{ChangesIdentity: r.id, History: "complete", Diff: "complete", Complete: true, Total: len(r.commits), Commits: []ChangesCommit{}}
	if r.id.Unborn {
		h.Diff = "unavailable"
	}
	if r.diagnostic != nil {
		h.History = "unsupported"
		h.Complete = false
		h.Diagnostic = r.diagnostic
		return h, nil
	}
	start := cursor.Next
	if start > len(r.commits) || (q.Cursor != "" && start == len(r.commits)) {
		return nil, changesErr("invalid_cursor", "cursor position is outside full sequence")
	}
	end := min(start+q.Limit, len(r.commits))
	h.Commits = append(h.Commits, r.commits[start:end]...)
	if end < len(r.commits) {
		b, _ := json.Marshal(changesCursor{ChangesVersion, c.service.Tenant, q.JobID, r.id.Selection, q.Base, q.Limit, end})
		h.NextCursor = base64.RawURLEncoding.EncodeToString(b)
	}
	return h, nil
}

func validateDiffRequest(q DiffRequest) error {
	if q.Cursor != "" || q.Limit != 0 {
		return changesErr("invalid_request", "diff does not accept pagination")
	}
	modes := 0
	if q.All {
		modes++
	}
	if q.Commit != "" {
		modes++
		if !fullHash(q.Commit) {
			return changesErr("invalid_range", "commit requires a full hash")
		}
	}
	if q.From != "" || q.Through != "" {
		modes++
		if !fullHash(q.From) || !fullHash(q.Through) {
			return changesErr("invalid_range", "range requires two full hashes")
		}
	}
	if modes > 1 {
		return changesErr("invalid_range", "diff selectors are mutually exclusive")
	}
	return nil
}

func (c *ChangesService) OpenDiff(ctx context.Context, q DiffRequest) (meta *ChangesDiff, body io.ReadCloser, err error) {
	defer changesOperation("diff", &err)
	if err = validateDiffRequest(q); err != nil {
		return nil, nil, err
	}
	if err = normalizeChangesRequest(&q.ChangesRequest, c.config.Limits); err != nil {
		return nil, nil, err
	}
	r, err := c.prepare(ctx, q.ChangesRequest)
	if err != nil {
		return nil, nil, err
	}
	defer r.o.close()
	if r.id.Unborn {
		return nil, nil, changesErr("workspace_unavailable", "unborn workspace has no recorded object format for a tree comparison")
	}
	meta = &ChangesDiff{ChangesIdentity: r.id, History: "complete", Diff: "complete", Complete: true, FromCommit: r.id.Base, ToCommit: r.id.Head, FromTree: r.id.BaseTree, ToTree: r.id.HeadTree}
	if r.diagnostic != nil {
		meta.History = "unsupported"
		meta.Diagnostic = r.diagnostic
	}
	first, last := q.From, q.Through
	if q.Commit != "" {
		first = q.Commit
		last = q.Commit
	}
	if first != "" {
		if r.diagnostic != nil {
			return nil, nil, r.diagnostic
		}
		from, to := -1, -1
		for i, v := range r.commits {
			if v.Hash == first {
				from = i
			}
			if v.Hash == last {
				to = i
			}
		}
		if from < 0 || to < from {
			return nil, nil, changesErr("invalid_range", "endpoints must be ordered members of the complete sequence")
		}
		meta.ToCommit = r.commits[to].Hash
		meta.ToTree = r.commits[to].Tree
		if from > 0 {
			meta.FromCommit = r.commits[from-1].Hash
			meta.FromTree = r.commits[from-1].Tree
		}
	}
	// Complete bounded spooling precedes exposing any bytes. The spool is moved
	// outside the object directory so EOF/Close/cancellation owns only one file.
	f, e := os.CreateTemp(r.o.root, "patch-")
	if e != nil {
		return nil, nil, changesErr("temporary_storage", "cannot create patch spool")
	}
	spoolPath := f.Name()
	defer func() {
		if err != nil {
			f.Close()
			os.Remove(spoolPath)
		}
	}()
	if err = r.o.run(nil, f, c.config.Limits.PatchBytes, "patch_bytes", nil, "diff", "--binary", "--full-index", "--no-ext-diff", "--no-textconv", "--no-renames", "--src-prefix=a/", "--dst-prefix=b/", "--no-color", meta.FromTree, meta.ToTree, "--"); err != nil {
		return nil, nil, err
	}
	info, e := f.Stat()
	if e != nil {
		return nil, nil, e
	}
	meta.Bytes = info.Size()
	if _, err = f.Seek(0, 0); err != nil {
		return nil, nil, err
	}
	reservation, e := os.CreateTemp(c.config.TempDir, "c2j-diff-")
	if e != nil {
		return nil, nil, changesErr("temporary_storage", "cannot transfer patch spool")
	}
	target := reservation.Name()
	if e = reservation.Close(); e != nil {
		os.Remove(target)
		return nil, nil, e
	}
	if e = os.Rename(spoolPath, target); e != nil {
		os.Remove(target)
		return nil, nil, changesErr("temporary_storage", "cannot transfer patch spool")
	}
	spoolPath = target
	b := &changesSpool{f: f, path: target, ctx: ctx}
	b.mu.Lock()
	b.stop = context.AfterFunc(ctx, func() { _ = b.Close() })
	b.mu.Unlock()
	return meta, b, nil
}

type changesSpool struct {
	mu     sync.Mutex
	f      *os.File
	path   string
	ctx    context.Context
	stop   func() bool
	closed bool
}

func (b *changesSpool) Read(p []byte) (int, error) {
	if err := b.ctx.Err(); err != nil {
		_ = b.Close()
		return 0, ChangesDiagnostic(err)
	}
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return 0, io.EOF
	}
	n, err := b.f.Read(p)
	b.mu.Unlock()
	if err != nil {
		_ = b.Close()
	}
	return n, err
}
func (b *changesSpool) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return nil
	}
	b.closed = true
	if b.stop != nil {
		b.stop()
	}
	return errors.Join(b.f.Close(), os.Remove(b.path))
}

var _ ChangesAPI = (*ChangesService)(nil)
var _ ChangesAPI = (*Service)(nil)
