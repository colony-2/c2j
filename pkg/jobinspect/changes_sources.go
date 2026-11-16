package jobinspect

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/colony-2/c2j/pkg/git/gitstate"
	"github.com/colony-2/jobdb/pkg/jobdb"
)

type changesBundle struct {
	path                 string
	prerequisites, heads []string
	format               string
	source               ChangesSource
	v                    changesCandidate
	history              *history
	predecessors         []changesCandidate
}

func changesKeyID(source ChangesSource) string {
	b := fmt.Sprintf("%s\x00%d\x00%s", source.Key.JobId, source.Key.TaskOrdinal, source.Key.Name)
	h := sha256.Sum256([]byte(b))
	return hex.EncodeToString(h[:])
}

func checkChangesObjectSizes(r io.Reader, l ChangesLimits) error {
	s := bufio.NewScanner(r)
	count := 0
	var total int64
	for s.Scan() {
		f := strings.Fields(s.Text())
		if len(f) != 3 || !fullHash(f[0]) {
			return changesErr("invalid_objects", "invalid object inventory")
		}
		n, e := parseObjectSize(f[2])
		if e != nil || n < 0 {
			return changesErr("invalid_objects", "invalid object size")
		}
		count++
		if count > l.Objects {
			return changesLimit("objects")
		}
		if n > l.ObjectBytes {
			return changesLimit("object_bytes")
		}
		if n > l.InflatedBytes-total {
			return changesLimit("inflated_bytes")
		}
		total += n
	}
	return s.Err()
}

func changesBundleHeader(r io.Reader, limit int64) (prereqs, heads []string, format string, err error) {
	s := bufio.NewScanner(io.LimitReader(r, limit+1))
	s.Buffer(make([]byte, 4096), int(limit)+1)
	var used int64
	if !s.Scan() {
		return nil, nil, "", changesErr("invalid_objects", "empty bundle header")
	}
	if int64(len(s.Bytes())+1) > limit {
		return nil, nil, "", changesLimit("header_bytes")
	}
	if s.Text() != "# v2 git bundle" && s.Text() != "# v3 git bundle" {
		return nil, nil, "", changesErr("invalid_objects", "expected a Git bundle")
	}
	v3 := s.Text() == "# v3 git bundle"
	used += int64(len(s.Bytes()) + 1)
	format = "sha1"
	for s.Scan() {
		line := s.Text()
		used += int64(len(line) + 1)
		if used > limit {
			return nil, nil, "", changesLimit("header_bytes")
		}
		if line == "" {
			if len(heads) == 0 {
				return nil, nil, "", changesErr("invalid_objects", "bundle advertises no commits")
			}
			return prereqs, heads, format, nil
		}
		if strings.HasPrefix(line, "@") {
			if v3 && line == "@object-format=sha256" {
				format = "sha256"
				continue
			}
			if v3 && line == "@object-format=sha1" {
				continue
			}
			return nil, nil, "", changesErr("invalid_objects", "unsupported bundle capability")
		}
		h, _, ok := strings.Cut(line, " ")
		prereq := strings.HasPrefix(h, "-")
		h = strings.TrimPrefix(h, "-")
		size := 40
		if format == "sha256" {
			size = 64
		}
		if !ok || !fullHash(h) || len(h) != size {
			return nil, nil, "", changesErr("invalid_objects", "invalid advertised object hash")
		}
		if prereq {
			prereqs = append(prereqs, h)
		} else {
			heads = append(heads, h)
		}
	}
	if used >= limit || s.Err() != nil {
		return nil, nil, "", changesLimit("header_bytes")
	}
	return nil, nil, "", changesErr("invalid_objects", "incomplete bundle header")
}

func (o *changesObjects) bundle(h *history, v changesCandidate) (*changesBundle, error) {
	if v.art == nil || v.art.Reference.Stored == nil {
		return nil, changesErr("missing_source", "bundle reference is unavailable")
	}
	k := v.art.Reference.Stored.Key
	if k.Validate() != nil || k.SizeBytes < 0 {
		return nil, changesErr("invalid_objects", "invalid stored bundle key")
	}
	if v.art.Size != k.SizeBytes {
		return nil, changesErr("source_mismatch", "bundle size metadata disagrees")
	}
	if k.Name != gitstate.ThinPackArtifactName {
		return nil, changesErr("source_mismatch", "reference is not a workspace bundle")
	}
	source := ChangesSource{Key: k}
	// Recover the original producer, including forwarded input/restore keys.
	producerHistory := h
	if k.JobId != h.job.ID {
		var err error
		producerHistory, err = o.c.service.load(o.ctx, k.JobId)
		if err != nil {
			return nil, ChangesDiagnostic(err)
		}
	}
	var producer *attempt
	for _, as := range producerHistory.attempts {
		for _, a := range as {
			for _, art := range a.task.Artifacts {
				if art.Reference.Stored != nil && sameKey(art.Reference.Stored.Key, k) && (producer == nil || a.order < producer.order) {
					if art.Reference.Stored.Key.SizeBytes != k.SizeBytes {
						return nil, changesErr("source_mismatch", "bundle producer key size differs")
					}
					producer = a
				}
			}
		}
	}
	if producer == nil {
		return nil, changesErr("missing_source", "original bundle producer occurrence is unavailable")
	}
	pw := changesWorkspace(producer, "after")
	if !sameChangesRepository(pw.Repository, v.w.Repository) || pw.HeadCommit != v.w.HeadCommit || pw.BaseCommit != v.w.BaseCommit || pw.EmptyBase != v.w.EmptyBase {
		return nil, changesErr("source_mismatch", "bundle producer does not describe selected candidate")
	}
	if k.JobId == h.job.ID && pw.ScopeID != v.w.ScopeID {
		return nil, changesErr("source_mismatch", "bundle producer belongs to another workspace")
	}
	source.Producer = ChangesProducer{JobID: k.JobId, Task: producer.task.ID, Attempt: producer.task.Attempt}
	if source.Producer.Attempt <= 0 {
		return nil, changesErr("source_mismatch", "bundle attempt is unresolved")
	}
	// Foreign sources are eligible only through the selected fixed incoming key.
	fixedIncoming := v.a != nil && v.a.in.Restore != nil && sameKey(*v.a.in.Restore, k)
	if k.JobId != h.job.ID && !fixedIncoming && !changesRestartSource(h, v, k, o.c.service.Tenant) {
		return nil, changesErr("job_denied", "foreign bundle is not a fixed incoming edge")
	}
	l := o.c.config.Limits
	if v.art.Size > l.ArtifactBytes {
		return nil, changesLimit("artifact_bytes")
	}
	if o.c.service.Authorize != nil {
		if err := o.c.service.Authorize(o.ctx, k.JobId); err != nil {
			return nil, ChangesDiagnostic(err)
		}
	}
	stored, err := o.c.service.Runtime.OpenArtifact(o.ctx, jobdb.ArtifactRef{JobKey: jobdb.JobKey{TenantId: o.c.service.Tenant, JobId: k.JobId}, Ordinal: k.TaskOrdinal, Name: k.Name})
	if err != nil {
		return nil, ChangesDiagnostic(err)
	}
	r, err := stored.Open()
	if err != nil {
		return nil, ChangesDiagnostic(err)
	}
	defer r.Close()
	stop := context.AfterFunc(o.ctx, func() { _ = r.Close() })
	defer stop()
	f, err := os.CreateTemp(o.root, "bundle-")
	if err != nil {
		return nil, changesErr("temporary_storage", "cannot store selected bundle")
	}
	defer f.Close()
	used, err := changesDirectoryUsage(o.storageRoot(), l.TemporaryBytes)
	if err != nil {
		return nil, err
	}
	remaining := min(l.ArtifactBytes, l.DownloadBytes-o.downloaded, l.TemporaryBytes-used)
	if remaining < 0 {
		return nil, changesLimit("download_bytes")
	}
	n, err := io.Copy(f, io.LimitReader(changesContextReader{o.ctx, r}, remaining+1))
	o.downloaded += n
	if err != nil {
		return nil, ChangesDiagnostic(err)
	}
	if n > remaining {
		if used+n > l.TemporaryBytes {
			return nil, changesLimit("temporary_bytes")
		}
		if o.downloaded > l.DownloadBytes {
			return nil, changesLimit("download_bytes")
		}
		return nil, changesLimit("artifact_bytes")
	}
	if v.art.Size >= 0 && n != v.art.Size {
		return nil, changesErr("missing_source", "retained bundle byte count differs")
	}
	if _, err = f.Seek(0, 0); err != nil {
		return nil, changesErr("temporary_storage", "cannot read retained bundle")
	}
	p, heads, format, err := changesBundleHeader(f, l.HeaderBytes)
	if err != nil {
		return nil, err
	}
	if !contains(heads, v.w.HeadCommit) {
		return nil, changesErr("source_mismatch", "bundle does not advertise recorded head")
	}
	if err = inspectChangesPack(o.ctx, f.Name(), format, l); err != nil {
		return nil, err
	}
	b := &changesBundle{path: f.Name(), prerequisites: p, heads: heads, format: format, source: source, v: v, history: producerHistory}
	b.v.order = producer.order
	if k.JobId != h.job.ID {
		for _, as := range producerHistory.attempts {
			for _, prior := range as {
				if prior.order >= producer.order || !sameChangesRepository(prior.in.Git.BaseRepo, pw.Repository) {
					continue
				}
				scope := ""
				if prior.in.Git.Workspace != nil {
					scope = prior.in.Git.Workspace.ScopeID
				}
				if scope != pw.ScopeID {
					continue
				}
				state := changesWorkspace(prior, "after")
				for _, art := range prior.task.Artifacts {
					if art.Name == gitstate.ThinPackArtifactName && art.Reference.Stored != nil {
						copy := art
						b.predecessors = append(b.predecessors, changesCandidate{w: state, a: prior, art: &copy, order: prior.order})
					}
				}
			}
		}
		sort.Slice(b.predecessors, func(i, j int) bool { return b.predecessors[i].order < b.predecessors[j].order })
	}
	return b, nil
}

func changesRestartSource(h *history, v changesCandidate, key jobdb.ArtifactKey, tenant string) bool {
	for order, entry := range h.run.Execution {
		r := entry.Record
		if r.Kind != "restart" || r.Source == nil || r.Source.TenantId != tenant || r.Source.JobId != key.JobId {
			continue
		}
		for _, as := range h.attempts {
			for _, a := range as {
				if a.order >= order {
					continue
				}
				w := changesWorkspace(a, "after")
				if !sameChangesRepository(w.Repository, v.w.Repository) || w.ScopeID != v.w.ScopeID || w.BaseCommit != v.w.BaseCommit || w.HeadCommit != v.w.HeadCommit || w.EmptyBase != v.w.EmptyBase {
					continue
				}
				for _, art := range a.task.Artifacts {
					if art.Reference.Stored != nil && sameKey(art.Reference.Stored.Key, key) {
						return true
					}
				}
			}
		}
	}
	return false
}

func (o *changesObjects) repository(hashes ...string) error {
	if o.c.config.ResolveRepository == nil {
		return missingChangesObjects(hashes...)
	}
	// Identity is assigned by prepare before any configured access.
	repo := o.repositoryIdentity
	access, err := o.c.config.ResolveRepository(o.ctx, repo)
	if err != nil {
		return ChangesDiagnostic(err)
	}
	if access.URL == "" || strings.HasPrefix(access.URL, "-") || strings.Contains(access.URL, "::") {
		return changesErr("repository_denied", "configured repository route is unavailable")
	}
	for _, entry := range access.Environment {
		name, _, ok := strings.Cut(entry, "=")
		if !ok {
			return changesErr("repository_denied", "invalid configured credential environment")
		}
		switch name {
		case "GIT_ASKPASS", "GIT_SSH_COMMAND", "GIT_SSH_VARIANT", "GIT_SSL_CAINFO", "GIT_SSL_CERT", "GIT_SSL_KEY", "SSH_ASKPASS", "SSH_AUTH_SOCK", "HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY", "http_proxy", "https_proxy", "all_proxy", "no_proxy":
		default:
			return changesErr("repository_denied", "configured environment may only supply credential and transport settings")
		}
	}
	for _, hash := range hashes {
		if !fullHash(hash) {
			return changesErr("invalid_objects", "repository access requires exact object hashes")
		}
		present, e := o.has(hash)
		if e != nil {
			return e
		}
		if present {
			continue
		}
		err = o.run(nil, io.Discard, 64<<10, "git_output", &access, "fetch", "--quiet", "--no-tags", "--no-write-fetch-head", "--no-recurse-submodules", "--", access.URL, hash)
		if err != nil {
			return err
		}
		present, e = o.has(hash)
		if e != nil {
			return e
		}
		if !present {
			return missingChangesObjects(hash)
		}
		o.repositoryObjects = append(o.repositoryObjects, hash)
		if err = o.checkObjects(); err != nil {
			return err
		}
	}
	return nil
}

// loadBundle walks only eligible, earlier main-workspace records. Exact
// prerequisite heads select sources; no scan of other jobs or moving refs occurs.
func (o *changesObjects) loadBundle(h *history, b *changesBundle, candidates []changesCandidate, depth int) error {
	if b.source.Key.JobId != h.job.ID {
		h = b.history
		candidates = b.predecessors
	}
	id := changesKeyID(b.source)
	if o.loaded[id] {
		return nil
	}
	if o.loading[id] {
		return changesErr("invalid_objects", "bundle prerequisite cycle")
	}
	if depth > o.c.config.Limits.Depth {
		return changesLimit("depth")
	}
	if len(o.loaded)+len(o.loading) >= o.c.config.Limits.PrerequisiteSources+1 {
		return changesLimit("prerequisite_sources")
	}
	o.loading[id] = true
	defer delete(o.loading, id)
	for _, hash := range b.prerequisites {
		if hash == b.v.w.HeadCommit {
			return changesErr("invalid_objects", "bundle prerequisite contains its own head")
		}
		present, e := o.has(hash)
		if e != nil {
			return e
		}
		if present {
			continue
		}
		var chosen *changesBundle
		var earlier []changesCandidate
		for i := len(candidates) - 1; i >= 0; i-- {
			o.lineageExamined++
			if o.lineageExamined > o.c.config.Limits.LineageCandidates {
				return changesLimit("lineage_candidates")
			}
			v := candidates[i]
			if v.order >= b.v.order || v.art == nil {
				continue
			}
			earlier = append(earlier, v)
			if v.w.HeadCommit != hash {
				continue
			}
			next, err := o.bundle(h, v)
			if err != nil {
				return err
			}
			if next.format != b.format {
				return changesErr("invalid_objects", "prerequisite object formats disagree")
			}
			chosen = next
			break
		}
		if chosen != nil {
			if err := o.loadBundle(h, chosen, candidates, depth+1); err != nil {
				return err
			}
			present, e = o.has(hash)
			if e != nil {
				return e
			}
			if present {
				if _, err := o.git("merge-base", "--is-ancestor", hash, chosen.v.w.HeadCommit); err != nil {
					if ChangesDiagnostic(err).Code == "invalid_objects" {
						return changesErr("missing_closure", "earlier bundle does not establish prerequisite ancestry")
					}
					return err
				}
			}
		} else {
			// Prove cumulative coverage in isolation. A nearer rewritten branch
			// must neither hide an applicable older bundle nor supply unrelated
			// objects/provenance to this read. Failed probes still consume budgets.
			covered := false
			for _, prior := range earlier {
				var err error
				covered, err = o.probePrerequisite(h, prior, candidates, depth+1, hash, b.format)
				if err != nil {
					return err
				}
				if covered {
					break
				}
			}
			if !covered {
				if err := o.repository(hash); err != nil {
					return err
				}
			}
		}
		present, e = o.has(hash)
		if e != nil {
			return e
		}
		if !present {
			return missingChangesObjects(hash)
		}
	}
	if _, err := o.git("bundle", "unbundle", b.path); err != nil {
		return err
	}
	present, e := o.has(b.v.w.HeadCommit)
	if e != nil {
		return e
	}
	if !present {
		return changesErr("source_mismatch", "bundle did not supply its recorded commit")
	}
	if err := o.checkObjects(); err != nil {
		return err
	}
	o.loaded[id] = true
	o.sources = append(o.sources, b.source)
	return nil
}

func sortedChangesSources(in []ChangesSource) []ChangesSource {
	out := append([]ChangesSource{}, in...)
	sort.Slice(out, func(i, j int) bool { return changesKeyID(out[i]) < changesKeyID(out[j]) })
	return out
}
