package changestest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"

	"github.com/colony-2/c2j/pkg/jobinspect"
	"github.com/colony-2/jobdb/pkg/jobdb"
)

// Inventory drains chapter windows and every inspection task/attempt/artifact
// page, including source bytes. A page size of one makes incomplete traversal
// visible in small fixtures. Compare bytes before and after read-only intervals.
func Inventory(ctx context.Context, rt jobinspect.Runtime, key jobdb.JobKey) ([]byte, error) {
	type bytesIdentity struct {
		Ordinal      int64
		Name, Digest string
		Size         int64
	}
	var chapters []jobdb.Chapter
	var contents []bytesIdentity
	for start := int64(0); ; {
		end := start + 1
		page, e := rt.ListChapters(ctx, jobdb.ListChaptersRequest{JobKey: key, StartOrdinal: start, EndOrdinal: &end})
		if e != nil {
			return nil, e
		}
		// Windows can contain ordinal gaps. Stop only after the authoritative
		// unbounded tail is empty, rather than stopping at a sparse window.
		if len(page) == 0 {
			tail, e := rt.ListChapters(ctx, jobdb.ListChaptersRequest{JobKey: key, StartOrdinal: start})
			if e != nil {
				return nil, e
			}
			if len(tail) == 0 {
				break
			}
			start = tail[0].Ordinal
			continue
		}
		for _, ch := range page {
			chapters = append(chapters, ch)
			for _, art := range ch.Artifacts {
				r, e := rt.OpenArtifact(ctx, jobdb.ArtifactRef{JobKey: key, Ordinal: ch.Ordinal, Name: art.Name, Digest: art.Digest})
				if e != nil {
					return nil, e
				}
				body, e := r.Open()
				if e != nil {
					return nil, e
				}
				h := sha256.New()
				n, e := io.Copy(h, body)
				ce := body.Close()
				if e != nil {
					return nil, e
				}
				if ce != nil {
					return nil, ce
				}
				contents = append(contents, bytesIdentity{ch.Ordinal, art.Name, hex.EncodeToString(h.Sum(nil)), n})
			}
		}
		start = page[len(page)-1].Ordinal + 1
	}
	api := &jobinspect.Service{Runtime: rt, Tenant: key.TenantId}
	var artifacts []jobinspect.Artifact
	var tasks []jobinspect.Task
	for cursor := ""; ; {
		p, e := api.Inspect(ctx, jobinspect.Request{Operation: "log", JobID: key.JobId, Limit: 1, Cursor: cursor})
		if e != nil {
			return nil, e
		}
		tasks = append(tasks, p.Tasks...)
		if p.NextCursor == "" {
			break
		}
		cursor = p.NextCursor
	}
	readArts := func(task string, attempt int) error {
		for cursor := ""; ; {
			p, e := api.Inspect(ctx, jobinspect.Request{Operation: "artifacts", JobID: key.JobId, Task: task, Attempt: attempt, Limit: 1, Cursor: cursor})
			if e != nil {
				return e
			}
			artifacts = append(artifacts, p.Artifacts...)
			if p.NextCursor == "" {
				break
			}
			cursor = p.NextCursor
		}
		return nil
	}
	for _, task := range tasks {
		for _, attempt := range task.Attempts {
			if e := readArts(task.ID, attempt); e != nil {
				return nil, e
			}
		}
	}
	if e := readArts("", 0); e != nil {
		return nil, e
	}
	return json.Marshal(struct {
		Chapters  []jobdb.Chapter
		Bytes     []bytesIdentity
		Tasks     []jobinspect.Task
		Artifacts []jobinspect.Artifact
	}{chapters, contents, tasks, artifacts})
}
