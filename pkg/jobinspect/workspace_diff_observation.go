package jobinspect

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"

	"github.com/colony-2/jobdb/pkg/jobdb"
	runtimecore "github.com/colony-2/jobdb/pkg/jobdb/runtime/core"
)

// frozenWorkspaceRuntime lets the public projection decode a single immutable
// prefix. Neither projection nor any later source lookup can append to it.
type frozenWorkspaceRuntime struct {
	Runtime
	job      jobdb.JobInfo
	summary  jobdb.JobSummary
	chapters []jobdb.Chapter
}

func (r *frozenWorkspaceRuntime) GetJob(context.Context, jobdb.JobKey) (jobdb.JobInfo, error) {
	return r.job, nil
}
func (r *frozenWorkspaceRuntime) ListJobs(context.Context, jobdb.ListJobsRequest) (jobdb.ListJobsResponse, error) {
	return jobdb.ListJobsResponse{Jobs: []jobdb.JobSummary{r.summary}}, nil
}
func (r *frozenWorkspaceRuntime) ListChapters(context.Context, jobdb.ListChaptersRequest) ([]jobdb.Chapter, error) {
	return r.chapters, nil
}

type workspaceObservation struct {
	h        *history
	id       string
	chapters []jobdb.Chapter
}

func (s *Service) observeWorkspace(ctx context.Context, id string) (*workspaceObservation, error) {
	if s.Authorize != nil {
		if err := s.Authorize(ctx, id); err != nil {
			return nil, WorkspaceDiffDiagnostic("denied", "job inspection denied", err)
		}
	}
	key := jobdb.JobKey{TenantId: s.Tenant, JobId: id}
	job, err := s.Runtime.GetJob(ctx, key)
	if err != nil {
		return nil, WorkspaceDiffDiagnostic("storage", "cannot read job identity", err)
	}
	page, err := s.Runtime.ListJobs(ctx, jobdb.ListJobsRequest{TenantIds: []string{s.Tenant}, JobKeys: []jobdb.JobKey{key}, IncludeHidden: true, PageSize: 1})
	if err != nil {
		return nil, WorkspaceDiffDiagnostic("storage", "cannot read job summary", err)
	}
	if len(page.Jobs) != 1 || page.Jobs[0].JobKey != key {
		return nil, WorkspaceDiffDiagnostic("storage", "job identity unavailable", jobdb.ErrJobNotFound)
	}
	chapters, err := s.Runtime.ListChapters(ctx, jobdb.ListChaptersRequest{JobKey: key})
	if err != nil {
		return nil, WorkspaceDiffDiagnostic("storage", "cannot capture execution prefix", err)
	}
	sort.Slice(chapters, func(i, j int) bool { return chapters[i].Ordinal < chapters[j].Ordinal })
	hash := sha256.New()
	identity, _ := json.Marshal(key)
	hash.Write(identity)
	copy := make([]jobdb.Chapter, len(chapters))
	for i, chapter := range chapters {
		if chapter.Ordinal != int64(i) {
			return nil, WorkspaceDiffDiagnostic("protocol", "execution prefix has missing or duplicate chapters", nil)
		}
		// The exported codec clones all nested mutable bytes without inspecting
		// private reconstruction metadata or depending on its representation.
		encoded, err := runtimecore.EncodeChapter(runtimecore.EncodeChapterRequest{Chapter: chapter})
		if err != nil {
			return nil, WorkspaceDiffDiagnostic("protocol", "cannot freeze execution chapter", err)
		}
		copy[i], err = runtimecore.DecodeChapter(encoded)
		if err != nil {
			return nil, WorkspaceDiffDiagnostic("protocol", "cannot decode execution chapter", err)
		}
		b, err := json.Marshal(encoded)
		if err != nil {
			return nil, WorkspaceDiffDiagnostic("protocol", "cannot identify execution chapter", err)
		}
		hash.Write(b)
	}
	if err := context.Cause(ctx); err != nil {
		return nil, WorkspaceDiffDiagnostic("canceled", "observation canceled", err)
	}
	frozen := *s
	frozen.Authorize = nil
	frozen.Runtime = &frozenWorkspaceRuntime{Runtime: s.Runtime, job: job, summary: page.Jobs[0], chapters: copy}
	h, err := frozen.load(ctx, id)
	if err != nil {
		return nil, WorkspaceDiffDiagnostic("protocol", "cannot project execution prefix", err)
	}
	for _, entry := range h.run.Execution {
		if entry.Record.Output != nil {
			if _, _, err := decodeOutput(entry.Record.Output.Data); err != nil {
				return nil, WorkspaceDiffDiagnostic("protocol", "unsupported saved output envelope", err)
			}
		}
		if entry.Record.Version != jobdb.ExecutionPatternVersion {
			return nil, WorkspaceDiffDiagnostic("protocol", "unsupported execution protocol", nil)
		}
	}
	return &workspaceObservation{h: h, id: hex.EncodeToString(hash.Sum(nil)), chapters: copy}, nil
}

func workspaceCutoff(observation string, logical int) string {
	b, _ := json.Marshal(struct {
		Observation string
		Logical     int
	}{observation, logical})
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
