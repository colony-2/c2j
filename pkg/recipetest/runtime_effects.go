package recipetest

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	"github.com/colony-2/c2j/pkg/secrets"
	"github.com/colony-2/c2j/pkg/swfutil"
	"github.com/colony-2/jobdb/pkg/jobdb"
)

type EffectSnapshotConfig struct {
	Files            []string `json:"files,omitempty"`
	MaxBytes         int      `json:"max_bytes,omitempty"`
	MaxArtifactBytes int      `json:"max_artifact_bytes,omitempty"`
}

type RetainedContent struct {
	SHA256        string `json:"sha256"`
	ContentBase64 string `json:"content_base64"`
	Redacted      bool   `json:"redacted"`
}
type RetainedTask struct {
	InvocationID  string                       `json:"invocation_id"`
	TaskType      string                       `json:"task_type"`
	Attempt       int                          `json:"attempt"`
	ResultOrdinal int64                        `json:"result_ordinal"`
	Output        RetainedContent              `json:"output"`
	Artifacts     map[string]RetainedContent   `json:"artifacts"`
	ArtifactKeys  map[string]jobdb.ArtifactKey `json:"artifact_keys"`
}
type RetainedFile struct {
	Exists  bool            `json:"exists"`
	Content RetainedContent `json:"content"`
}
type RetainedRepository struct {
	Head        string                  `json:"head"`
	Tree        string                  `json:"tree"`
	Refs        string                  `json:"refs"`
	IndexSHA256 string                  `json:"index_sha256"`
	DiffSHA256  string                  `json:"diff_sha256"`
	Status      string                  `json:"status"`
	Files       map[string]RetainedFile `json:"files"`
}
type RetainedChild struct {
	JobID   string `json:"job_id"`
	Cell    string `json:"cell"`
	JobType string `json:"job_type"`
}
type EffectSnapshot struct {
	Complete        bool                          `json:"complete"`
	Errors          []string                      `json:"errors"`
	JobID           string                        `json:"job_id"`
	DispatchCount   int                           `json:"dispatch_count"`
	SuccessfulTasks map[string]RetainedTask       `json:"successful_tasks"`
	Repositories    map[string]RetainedRepository `json:"repositories"`
	Children        []RetainedChild               `json:"children"`
}

func contentDigest(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }

func (f *runtimeFixture) snapshotEffects(ctx context.Context, rt jobdb.WorkflowRuntime, key jobdb.JobKey) *EffectSnapshot {
	spec := f.c.Runtime.Controls.Snapshot
	snapshot := &EffectSnapshot{Complete: true, Errors: []string{}, JobID: key.JobId, SuccessfulTasks: map[string]RetainedTask{}, Repositories: map[string]RetainedRepository{}, Children: []RetainedChild{}}
	fail := func(err error) { snapshot.Complete = false; snapshot.Errors = append(snapshot.Errors, err.Error()) }
	maxBytes := 1024 * 1024
	if spec.MaxBytes > 0 {
		maxBytes = spec.MaxBytes
	}
	maxArtifact := 256 * 1024
	if spec.MaxArtifactBytes > 0 {
		maxArtifact = spec.MaxArtifactBytes
	}
	redactor := secrets.NewRedactor()
	defer redactor.Close()
	for _, v := range f.c.Runtime.Secrets {
		if err := redactor.Register(v); err != nil {
			fail(err)
		}
	}
	retain := func(data []byte) RetainedContent {
		safe := []byte(redactor.Redact(string(data)))
		return RetainedContent{SHA256: contentDigest(data), ContentBase64: base64.StdEncoding.EncodeToString(safe), Redacted: !reflect.DeepEqual(data, safe)}
	}
	f.mu.Lock()
	snapshot.DispatchCount = len(f.report.Controls.Dispatch)
	repos := map[string]string{}
	for cell, path := range f.cells {
		repos["fixture:"+cell] = path
	}
	for identity, path := range f.controlWorktrees {
		repos["invocation:"+identity] = path
	}
	f.mu.Unlock()
	run, err := jobdb.GetJobRun(ctx, rt, jobdb.GetJobRunRequest{JobKey: key, IncludeOutputs: true, IncludeArtifacts: true})
	if err != nil {
		fail(fmt.Errorf("read persisted execution: %w", err))
	} else {
		tasks, err := swfutil.ExecutionTasks(run)
		if err != nil {
			fail(err)
		} else {
			for _, task := range tasks {
				for _, attempt := range task.Attempts {
					if attempt.State != jobdb.TaskAttemptStateSucceeded {
						continue
					}
					if attempt.Output == nil {
						fail(fmt.Errorf("successful task %s output unavailable", task.InvocationID))
						continue
					}
					record := RetainedTask{InvocationID: task.InvocationID, TaskType: task.TaskType, Attempt: attempt.Attempt, ResultOrdinal: attempt.Ordinal, Output: retain(attempt.Output.Data), Artifacts: map[string]RetainedContent{}, ArtifactKeys: map[string]jobdb.ArtifactKey{}}
					for _, artifact := range attempt.Output.Artifacts {
						if artifact.Key == nil {
							fail(fmt.Errorf("artifact %s has no native key", artifact.Name))
							continue
						}
						k := *artifact.Key

						data, err := readEffectArtifact(ctx, rt, key.TenantId, k, maxArtifact)
						if err != nil {
							fail(err)
							continue
						}
						record.Artifacts[artifact.Name] = retain(data)
						record.ArtifactKeys[artifact.Name] = k
					}
					snapshot.SuccessfulTasks[fmt.Sprintf("%s/attempt:%d", task.InvocationID, attempt.Attempt)] = record
				}
			}
		}
	}
	for name, path := range repos {
		git := func(args ...string) (string, error) {
			b, err := exec.CommandContext(ctx, "git", append([]string{"-C", path}, args...)...).Output()
			if err != nil {
				return "", fmt.Errorf("repository %s unavailable: %w", name, err)
			}
			if len(b) > maxBytes {
				return "", fmt.Errorf("repository %s evidence exceeds limit", name)
			}
			return string(b), nil
		}
		r := RetainedRepository{Files: map[string]RetainedFile{}}
		fields := []struct {
			target *string
			args   []string
		}{
			{&r.Head, []string{"rev-parse", "HEAD"}}, {&r.Tree, []string{"rev-parse", "HEAD^{tree}"}}, {&r.Refs, []string{"for-each-ref", "--format=%(refname) %(objectname)"}}, {&r.Status, []string{"status", "--porcelain=v1", "-z", "--untracked-files=all"}},
		}
		for _, field := range fields {
			value, err := git(field.args...)
			if err != nil {
				fail(err)
			} else {
				*field.target = strings.TrimSuffix(value, "\n")
			}
		}
		index, err := git("ls-files", "--stage", "-z")
		if err != nil {
			fail(err)
		} else {
			r.IndexSHA256 = contentDigest([]byte(index))
		}
		diff, err := git("diff", "--binary", "HEAD", "--")
		if err != nil {
			fail(err)
		} else {
			r.DiffSHA256 = contentDigest([]byte(diff))
		}
		realRoot, err := filepath.EvalSymlinks(path)
		if err != nil {
			fail(fmt.Errorf("repository %s missing: %w", name, err))
			continue
		}
		for _, file := range spec.Files {
			selected, err := fixturePath(realRoot, file)
			if err != nil {
				fail(err)
				continue
			}
			info, err := os.Lstat(selected)
			if os.IsNotExist(err) {
				r.Files[file] = RetainedFile{}
				continue
			}
			if err != nil {
				fail(err)
				continue
			}
			if !info.Mode().IsRegular() || info.Size() > int64(maxArtifact) {
				fail(fmt.Errorf("selected file %s is nonregular or exceeds limit", file))
				continue
			}
			// Parent symlinks must not escape the selected repository.
			real, err := filepath.EvalSymlinks(selected)
			if err != nil {
				fail(err)
				continue
			}
			rel, err := filepath.Rel(realRoot, real)
			if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				fail(fmt.Errorf("selected file %s escapes repository", file))
				continue
			}
			data, err := os.ReadFile(real)
			if err != nil {
				fail(err)
				continue
			}
			r.Files[file] = RetainedFile{Exists: true, Content: retain(data)}
		}
		snapshot.Repositories[name] = r
	}
	token := ""
	seen := map[string]bool{}
	for {
		list, err := rt.ListJobs(ctx, jobdb.ListJobsRequest{TenantIds: []string{key.TenantId}, ParentJobIDs: []string{key.JobId}, IncludeHidden: true, PageSize: 200, PageToken: token})
		if err != nil {
			fail(fmt.Errorf("child identity listing unavailable: %w", err))
			break
		}
		for _, child := range list.Jobs {
			snapshot.Children = append(snapshot.Children, RetainedChild{JobID: child.JobKey.JobId, Cell: child.Cell, JobType: child.JobType})
		}
		token = list.NextPageToken
		if token == "" {
			break
		}
		if seen[token] {
			fail(fmt.Errorf("child listing cursor repeated"))
			break
		}
		seen[token] = true
	}
	sort.Slice(snapshot.Children, func(i, j int) bool { return snapshot.Children[i].JobID < snapshot.Children[j].JobID })
	raw, err := json.Marshal(snapshot)
	if err != nil || len(raw) > maxBytes {
		fail(fmt.Errorf("effect snapshot exceeds limit or cannot be encoded"))
	}
	return snapshot
}

func (f *runtimeFixture) captureBaseline(ctx context.Context, rt jobdb.WorkflowRuntime, tenant string, b *barrierState) error {
	if f.c.Runtime.Controls.Snapshot == nil {
		return nil
	}
	f.mu.Lock()
	job := b.entry.JobID
	_, seen := f.report.Controls.Baselines[b.spec.ID]
	f.mu.Unlock()
	if seen {
		return nil
	}
	snapshot := f.snapshotEffects(ctx, rt, jobdb.JobKey{TenantId: tenant, JobId: job})
	f.mu.Lock()
	f.report.Controls.Baselines[b.spec.ID] = snapshot
	f.mu.Unlock()
	if !snapshot.Complete {
		return f.controlError(fmt.Errorf("baseline %s incomplete: %s", b.spec.ID, strings.Join(snapshot.Errors, "; ")))
	}
	return nil
}

func readEffectArtifact(ctx context.Context, rt jobdb.WorkflowRuntime, tenant string, k jobdb.ArtifactKey, limit int) ([]byte, error) {
	reader, err := rt.OpenArtifact(ctx, jobdb.ArtifactRef{JobKey: jobdb.JobKey{TenantId: tenant, JobId: k.JobId}, Ordinal: k.TaskOrdinal, Name: k.Name})
	if err != nil {
		return nil, fmt.Errorf("artifact %s unavailable: %w", k.Name, err)
	}
	if reader == nil {
		return nil, fmt.Errorf("artifact %s reader unavailable", k.Name)
	}
	stream, err := reader.Open()
	if err != nil {
		return nil, err
	}
	if stream == nil {
		return nil, fmt.Errorf("artifact %s stream unavailable", k.Name)
	}
	data, readErr := io.ReadAll(io.LimitReader(stream, int64(limit)+1))
	closeErr := stream.Close()
	if readErr != nil {
		return nil, readErr
	}
	if closeErr != nil {
		return nil, closeErr
	}
	if len(data) > limit || int64(len(data)) != k.SizeBytes {
		return nil, fmt.Errorf("artifact %s exceeds limit or has incomplete bytes", k.Name)
	}
	return data, nil
}
