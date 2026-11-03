// Package executiondiag publishes runtime-owned evidence for subprocess ops.
package executiondiag

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/colony-2/c2j/pkg/artifacts"
	"github.com/colony-2/c2j/pkg/gitprofile"
	"github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/c2j/pkg/ops/process"
	"github.com/colony-2/c2j/pkg/secrets"
	"github.com/colony-2/c2j/pkg/shellcmd"
	"github.com/colony-2/jobdb/pkg/jobdb"
)

const PreviewLimit = 10 * 1024

// Leave space for the worker's execution metadata within JobDB's 256 KiB default.
const OutputLimit = 128 * 1024
const LogLimit = 8 * 1024 * 1024

type Details struct {
	Version          int                      `json:"version"`
	ExitCode         int                      `json:"exit_code"`
	TimedOut         bool                     `json:"timed_out"`
	Diagnostics      map[string]artifacts.Ref `json:"diagnostics"`
	DiagnosticErrors []string                 `json:"diagnostic_errors,omitempty"`
}
type Failure struct {
	Cause     error
	Message   string
	Attribute string
	Details   Details
}

func (e *Failure) Error() string                     { return e.Message }
func (e *Failure) Unwrap() error                     { return e.Cause }
func (e *Failure) FailureAttributes() map[string]any { return map[string]any{e.Attribute: e.Details} }

type Root struct{ Name, Path string }
type Report struct {
	Capture                                    *process.Capture
	Name                                       string
	Directory                                  string
	Roots                                      []Root
	Selector, ResolvedSelector, ResolvedCommit string
	ctx                                        context.Context
	deps                                       ops.OpDependencies
	redaction                                  *secrets.Session
	releaseGit                                 func()
}

func New(ctx context.Context, deps ops.OpDependencies) *Report {
	r := &Report{Capture: process.NewCapture(ctx), ctx: ctx, deps: deps}
	if scope := gitprofile.FromContext(ctx); scope != nil {
		r.releaseGit = scope.RetainRedaction()
	}
	if redactor := secrets.RedactorFromContext(ctx); redactor != nil {
		r.redaction = redactor.NewSession()
	}
	node := path.Base(deps.GitContext().NodePath)
	if node == "." || node == "/" || node == "" {
		node = "execution"
	}
	safe := strings.Map(func(c rune) rune {
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.' {
			return c
		}
		return '_'
	}, node)
	if safe != node || len(safe) > 120 || safe == ".." {
		sum := sha256.Sum256([]byte(node))
		safe = prefix(safe, 100) + fmt.Sprintf("-%x", sum[:6])
	}
	r.Name = "_c2j/execution/" + safe + ".log"
	r.Roots = append(r.Roots, Root{"worktree", deps.WorktreePath()})
	if p, ok := deps.(ops.OperationPathProvider); ok {
		paths := p.OperationPaths()
		r.Roots = append(r.Roots, Root{"inbox", paths.Inbox}, Root{"outbox", paths.Outbox}, Root{"workdir", paths.Workdir})
	}
	return r
}

// Close releases redaction masks only after publication, including cancellation.
func (r *Report) Close() {
	if r.releaseGit != nil {
		r.releaseGit()
		r.releaseGit = nil
	}
	if r.redaction != nil {
		r.redaction.Close()
	}
}

// Prepare snapshots the selected directory before the child can mutate it.
func (r *Report) Prepare(req process.RunRequest) process.RunRequest {
	r.Directory = r.PortableDir(req.WorkingDir)
	req.Capture = r.Capture
	return req
}
func prefix(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = s[:max(0, n)]
	for !utf8.ValidString(s) && len(s) > 0 {
		s = s[:len(s)-1]
	}
	return s
}

// Preview's byte budget includes the notice. Redaction must precede this call.
func Preview(s string, incomplete bool, logName string) (string, bool) {
	s = strings.ToValidUTF8(strings.TrimRight(s, "\r\n"), "\uFFFD")
	encoded, _ := json.Marshal(s)
	if len(encoded) <= PreviewLimit && !incomplete {
		return s, false
	}
	notice := "\n[Output truncated. See execution log: " + logName + "]"
	if logName == "" {
		notice = "\n[Output truncated. Execution log unavailable.]"
	}
	lo, hi := 0, min(len(s), PreviewLimit-len(notice))
	for lo < hi {
		mid := (lo + hi + 1) / 2
		encoded, _ = json.Marshal(prefix(s, mid) + notice)
		if len(encoded) <= PreviewLimit {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	return prefix(s, lo) + notice, true
}
func Short(s string) string {
	if len(s) <= 2048 {
		return s
	}
	return prefix(s, 2000) + " [message truncated]"
}
func CheckOutput(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if len(b) > OutputLimit {
		return fmt.Errorf("operation output limit exceeded: %d serialized bytes (limit %d); see execution log", len(b), OutputLimit)
	}
	return nil
}
func (r *Report) safe(s string) string {
	var gitRedactor *secrets.Redactor
	if scope := gitprofile.FromContext(r.ctx); scope != nil {
		gitRedactor = scope.Redactor()
	}
	return secrets.RedactorFromContext(r.ctx).RedactParts([]string{s}, false, gitRedactor)[0]
}

func (r *Report) PortableDir(dir string) string {
	actual, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return "unavailable (requested: " + r.relative(dir, false) + "; directory could not be resolved)"
	}
	return r.relative(actual, true)
}
func (r *Report) relative(dir string, resolve bool) string {
	type match struct {
		root, rel string
		length    int
	}
	best := match{}
	for _, root := range r.Roots {
		if root.Path == "" {
			continue
		}
		base, err := filepath.Abs(root.Path)
		if err != nil {
			continue
		}
		if resolve {
			base, err = filepath.EvalSymlinks(base)
			if err != nil {
				continue
			}
		}
		abs, err := filepath.Abs(dir)
		if err != nil {
			continue
		}
		rel, err := filepath.Rel(base, abs)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			continue
		}
		if len(base) > best.length {
			best = match{root.Name, filepath.ToSlash(rel), len(base)}
		}
	}
	if best.length == 0 {
		return "outside known task roots; unavailable for portable replay"
	}
	return best.rel + " (relative to " + best.root + ")"
}
func (r *Report) display(script string) (string, bool) {
	script = r.safe(script)
	roots := append([]Root(nil), r.Roots...)
	sort.SliceStable(roots, func(i, j int) bool { return len(roots[i].Path) > len(roots[j].Path) })
	changed := false
	for _, root := range roots {
		if root.Path == "" || root.Path == string(filepath.Separator) {
			continue
		}
		// Match complete root prefixes, never /task/a in /task/another.
		var display strings.Builder
		rest := script
		for {
			i := strings.Index(rest, root.Path)
			if i < 0 {
				display.WriteString(rest)
				break
			}
			end := i + len(root.Path)
			boundary := func(c byte) bool { return strings.ContainsRune(" \t\r\n\"'`=:;,(){}[]", rune(c)) }
			before := i == 0 || boundary(rest[i-1])
			after := end == len(rest) || rest[end] == '/' || boundary(rest[end])
			display.WriteString(rest[:i])
			if before && after {
				display.WriteString("${" + root.Name + "}")
				changed = true
			} else {
				display.WriteString(root.Path)
			}
			rest = rest[end:]
		}
		script = display.String()
	}
	return script, changed
}

type boundedLog struct {
	bytes.Buffer
	omitted bool
}

func (b *boundedLog) Write(p []byte) (int, error) {
	n := min(len(p), LogLimit-b.Len())
	if n > 0 {
		_, _ = b.Buffer.Write(p[:n])
	}
	if n < len(p) {
		b.omitted = true
	}
	return len(p), nil
}
func (r *Report) Publish(opErr error, exit int, timedOut bool) Details {
	d := Details{Version: 2, ExitCode: exit, TimedOut: timedOut, Diagnostics: map[string]artifacts.Ref{}}
	c := r.Capture
	var b boundedLog
	directory := r.Directory
	if directory == "" {
		if c.Request.WorkingDir == "" {
			directory = "unavailable (working directory not selected)"
		} else {
			directory = r.PortableDir(c.Request.WorkingDir)
		}
	}
	fmt.Fprintf(&b, "Working directory: %s\n", r.safe(directory))
	script := c.Request.Run
	if len(c.Request.Command) > 0 {
		raw, _ := json.Marshal(c.Request.Command)
		script = string(raw)
	}
	script, normalized := r.display(script)
	if strings.Contains(script, "\n") {
		fmt.Fprintf(&b, "Submitted script:\n  %s\n", strings.ReplaceAll(script, "\n", "\n  "))
	} else {
		fmt.Fprintf(&b, "Command: %s\n", script)
	}
	if normalized {
		fmt.Fprintln(&b, "Command display: normalized task roots; not an executable replay; external absolute paths may remain")
	}
	if len(c.Request.Command) == 0 {
		shell := c.Request.Shell
		if shell == "" && !c.LaunchAttempted {
			fmt.Fprintln(&b, "Shell: unavailable (process setup did not select a shell)")
		} else if shell == "" {
			shell = shellcmd.DefaultShell()
		}
		argv, _ := shellcmd.BuildArgv(shell, "script")
		if shell != "" && len(argv) > 1 {
			fmt.Fprintf(&b, "Shell: %s %s\n", r.safe(filepath.Base(shell)), r.safe(argv[1]))
		}
	} else {
		fmt.Fprintln(&b, "Shell: none (direct argv)")
	}
	fmt.Fprintf(&b, "Node: %s\n", r.safe(r.deps.GitContext().NodePath))
	if jt := r.deps.JobTool(); jt != nil {
		if ordinal, ok := jt.(interface{ TaskOrdinal() int64 }); ok {
			fmt.Fprintf(&b, "Attempt: job %s, result ordinal %d\n", r.safe(jt.GetJobKey().JobId), ordinal.TaskOrdinal())
		}
	}
	if r.Selector != "" {
		fmt.Fprintf(&b, "Extension: %s\n", r.safe(r.Selector))
	}
	if r.ResolvedSelector != "" {
		fmt.Fprintf(&b, "Resolved extension: %s\n", r.safe(r.ResolvedSelector))
	}
	if r.ResolvedCommit != "" {
		fmt.Fprintf(&b, "Extension source revision: %s\n", r.safe(r.ResolvedCommit))
	}
	fmt.Fprintln(&b, "Execution environment identity: unavailable")
	if revision := r.deps.GitContext().ResolvedBaseHash; revision != "" {
		fmt.Fprintf(&b, "Workspace base revision: %s\n", r.safe(revision))
	}
	fmt.Fprintln(&b, "Tested worktree content identity: unknown; base revision does not identify all tested content")
	if c.Launched {
		fmt.Fprintf(&b, "Started: %s\n", c.Started.Format("2006-01-02T15:04:05.000Z"))
	} else {
		fmt.Fprintln(&b, "Started: not launched")
	}
	fmt.Fprintln(&b)
	if c.Launched {
		c.WriteLog(&b)
	} else {
		fmt.Fprintln(&b, "[No process output: not launched.]")
	}
	result := "exited with code 0"
	switch {
	case timedOut:
		result = "timed out"
	case errors.Is(c.Err, context.Canceled):
		result = "cancelled"
	case !c.Launched && c.LaunchAttempted:
		result = "launch failed (process did not start)"
	case !c.Launched:
		result = "rejected before launch"
	case c.Err != nil:
		var ee *exec.ExitError
		if errors.As(c.Err, &ee) && ee.ExitCode() >= 0 {
			result = fmt.Sprintf("exited with code %d", ee.ExitCode())
		} else {
			result = "process failed; no normal exit code"
		}
	}
	// Reserve the footer outside the bounded output so truncation cannot hide it.
	footer := "\nResult: " + result + "\n"
	if c.Launched {
		footer += fmt.Sprintf("Duration: %.3f seconds\n", c.Duration.Seconds())
	}
	if opErr != nil {
		footer += "Operation result: failed: " + Short(r.safe(opErr.Error())) + "\n"
	} else {
		footer += "Operation result: completed\n"
	}
	if b.omitted || b.Len()+len(footer) > LogLimit {
		footer = "\n[Execution log truncated: rendered log size limit exceeded.]\n" + footer
	}
	data := []byte(prefix(b.String(), LogLimit-len(footer)) + footer)
	if err := r.deps.AddOutputArtifact(jobdb.NewArtifactFromBytes(r.Name, data)); err != nil {
		d.DiagnosticErrors = append(d.DiagnosticErrors, Short(r.safe(err.Error())))
		return d
	}
	jt := r.deps.JobTool()
	ordinal, ok := jt.(interface{ TaskOrdinal() int64 })
	if !ok || jt.GetJobKey().JobId == "" {
		d.DiagnosticErrors = append(d.DiagnosticErrors, "durable artifact identity unavailable: "+r.Name)
		return d
	}
	d.Diagnostics[r.Name] = artifacts.NewStoredRef(jobdb.ArtifactKey{JobId: jt.GetJobKey().JobId, TaskOrdinal: ordinal.TaskOrdinal(), Name: r.Name, SizeBytes: int64(len(data))})
	return d
}
