package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/colony-2/c2j/pkg/childbroker"
	"github.com/colony-2/c2j/pkg/jobcontext"
	"github.com/colony-2/c2j/pkg/jobinspect"
	"github.com/colony-2/c2j/pkg/selfinfo"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

type dagCLIAPI struct {
	invalidTime             bool
	failEntry, failChildren bool
	title                   string
	family                  []jobinspect.FamilyNode
	calls                   []jobinspect.Request
}

func (a *dagCLIAPI) Inspect(_ context.Context, q jobinspect.Request) (*jobinspect.Response, error) {
	a.calls = append(a.calls, q)
	if q.Operation == "family-node" && a.failEntry {
		return nil, fmt.Errorf("entry unavailable")
	}
	if q.Operation == "family-children" && a.failChildren {
		return nil, fmt.Errorf("child unavailable")
	}
	p := &jobinspect.FamilyPage{Version: 1, Nodes: []jobinspect.FamilyNode{}}
	if a.family != nil {
		for _, n := range a.family {
			if (q.Operation == "family-node" && n.ID == q.JobID) || (q.Operation == "family-children" && n.Parent == q.JobID) {
				p.Nodes = append(p.Nodes, n)
			}
		}
		return &jobinspect.Response{Version: 1, Family: p}, nil
	}
	if q.Operation == "family-node" {
		p.Nodes = append(p.Nodes, jobinspect.FamilyNode{ID: q.JobID, Tenant: "tenant", Cell: "c2j", Title: "Selected\n\x1b[31m", Status: "ACTIVE", CancelRequested: true})
	}
	if q.Operation == "family-children" && q.JobID == "selected-full-id" {
		p.Nodes = append(p.Nodes, jobinspect.FamilyNode{ID: "z-child-full-id", Tenant: "tenant", Parent: q.JobID, Cell: "cortex", Title: strings.Repeat("title", 100), Status: "ARCHIVED"}, jobinspect.FamilyNode{ID: "a-child-full-id", Tenant: "tenant", Parent: q.JobID, Status: "COMPLETED", CompletionStatus: "success"})
	}
	if a.invalidTime && len(p.Nodes) > 0 {
		p.Nodes[0].CreatedAt = time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)
	}
	if a.title != "" && len(p.Nodes) > 0 {
		p.Nodes[0].Title = a.title
	}
	return &jobinspect.Response{Version: 1, Family: p}, nil
}
func (*dagCLIAPI) Download(context.Context, jobinspect.Request) (io.ReadCloser, error) {
	panic("unexpected")
}
func dagCLI(a jobinspect.API, current string, out io.Writer) *cobra.Command {
	var uri string
	var embed, jsonOutput bool
	root := &cobra.Command{Use: "job", SilenceUsage: true, SilenceErrors: true}
	root.PersistentFlags().BoolVar(&jsonOutput, "json", false, "")
	root.AddCommand(newJobDAGCmd(&uri, &embed, &jsonOutput, func(context.Context, string) (jobinspect.API, func(), string, error) {
		return a, func() {}, current, nil
	}))
	root.SetOut(out)
	root.SetErr(io.Discard)
	return root
}
func TestJobDAGTextAndJSON(t *testing.T) {
	for _, format := range []string{"text", "json"} {
		t.Run(format, func(t *testing.T) {
			api := &dagCLIAPI{}
			var out bytes.Buffer
			c := dagCLI(api, "selected-full-id", &out)
			args := []string{"dag", "self"}
			if format == "json" {
				args = append(args, "--json")
			}
			c.SetArgs(args)
			require.NoError(t, c.Execute())
			require.Equal(t, "selected-full-id", api.calls[0].JobID)
			if format == "text" {
				text := out.String()
				require.Contains(t, text, "selected-full-id")
				require.Contains(t, text, "[selected]")
				require.Contains(t, text, `Selected\n\x1b[31m`)
				require.NotContains(t, text, "\x1b")
				require.Contains(t, text, "! Selected")
				require.Contains(t, text, "✓ (untitled job)")
				require.Less(t, strings.Index(text, "a-child-full-id"), strings.Index(text, "z-child-full-id"))
				require.Len(t, strings.Split(strings.TrimSuffix(text, "\n"), "\n"), 3)
				require.NotContains(t, text, "Warning:")
			} else {
				var d jobinspect.DAG
				require.NoError(t, json.Unmarshal(out.Bytes(), &d))
				require.Equal(t, 1, d.Version)
				require.Equal(t, "selected-full-id", d.Selected.ID)
				require.True(t, d.Coverage.Complete)
				require.NotNil(t, d.Coverage.Anomalies)
				require.Equal(t, "submission", d.Edges[0].Type)
				require.True(t, d.Root.Confirmed)
			}
		})
	}
}
func TestJobDAGArgumentsBeforeOpening(t *testing.T) {
	for _, args := range [][]string{{}, {"a", "b"}, {"a", "--max-nodes", "0"}, {"a", "--max-depth", "-1"}, {"a", "--timeout", "0s"}, {"a", "--timeout", "bad"}} {
		var uri string
		var embed, jsonOutput bool
		c := newJobDAGCmd(&uri, &embed, &jsonOutput, func(context.Context, string) (jobinspect.API, func(), string, error) {
			t.Fatal("opened backend")
			return nil, nil, "", nil
		})
		c.SetOut(io.Discard)
		c.SetErr(io.Discard)
		c.SetArgs(args)
		require.Error(t, c.Execute())
	}
	var out bytes.Buffer
	c := dagCLI(&dagCLIAPI{}, "", &out)
	c.SetArgs([]string{"dag", "self"})
	require.ErrorContains(t, c.Execute(), "self requires")
	require.Empty(t, out.String())
	a := &dagCLIAPI{failEntry: true}
	c = dagCLI(a, "", &out)
	c.SetArgs([]string{"dag", "selected"})
	require.ErrorContains(t, c.Execute(), "entry unavailable")
	require.Empty(t, out.String())
}
func TestJobDAGFilesAndPartialExit(t *testing.T) {
	for _, partial := range []bool{false, true} {
		for _, jsonOutput := range []bool{false, true} {
			t.Run(fmt.Sprintf("partial=%v/json=%v", partial, jsonOutput), func(t *testing.T) {
				a := &dagCLIAPI{failChildren: partial}
				file := filepath.Join(t.TempDir(), "dag.out")
				var out bytes.Buffer
				args := []string{"dag", "selected-full-id", "--output", file}
				if jsonOutput {
					args = append(args, "--json")
				}
				c := dagCLI(a, "", &out)
				c.SetArgs(args)
				err := c.Execute()
				if partial {
					var coded exitCoder
					require.ErrorAs(t, err, &coded)
					require.Equal(t, 2, coded.ExitCode())
				} else {
					require.NoError(t, err)
				}
				raw, err := os.ReadFile(file)
				require.NoError(t, err)
				require.NotEmpty(t, raw)
				require.Empty(t, out.String())
				if jsonOutput {
					var d jobinspect.DAG
					require.NoError(t, json.Unmarshal(raw, &d))
					require.Equal(t, !partial, d.Coverage.Complete)
				} else {
					require.Contains(t, string(raw), "selected-full-id")
					require.Equal(t, partial, strings.Contains(string(raw), "Warning:"))
					if partial {
						require.Contains(t, string(raw), "Children of selected-full-id: unavailable")
					}
				}
				c = dagCLI(a, "", &out)
				c.SetArgs(args)
				require.Error(t, c.Execute())
				after, err := os.ReadFile(file)
				require.NoError(t, err)
				require.Equal(t, raw, after)
			})
		}
	}
	// Stdout is never truncated at the old inspection terminal byte cap.
	a := &dagCLIAPI{title: strings.Repeat("large", 70000)}
	var out bytes.Buffer
	c := dagCLI(a, "", &out)
	c.SetArgs([]string{"dag", "selected-full-id", "--json", "--output", "-"})
	require.NoError(t, c.Execute())
	require.True(t, json.Valid(out.Bytes()))
	require.Greater(t, len(out.Bytes()), 256<<10)
}

type brokenDAGWriter struct{}

func (brokenDAGWriter) Write([]byte) (int, error) { return 0, errors.New("write failed") }
func TestJobDAGOutputFailure(t *testing.T) {
	c := dagCLI(&dagCLIAPI{failChildren: true}, "", brokenDAGWriter{})
	c.SetArgs([]string{"dag", "selected"})
	err := c.Execute()
	require.ErrorContains(t, err, "write failed")
	var partial partialDAGError
	require.False(t, errors.As(err, &partial))
	dir := t.TempDir()
	file := filepath.Join(dir, "failed")
	err = writeInspectionFile(file, io.Discard, func(w io.Writer) error {
		_, e := w.Write([]byte("prefix"))
		require.NoError(t, e)
		return errors.New("render failed")
	})
	require.Error(t, err)
	require.NoFileExists(t, file)
	// Simulate a close failure after a successful callback; cleanup owns this new file.
	err = writeInspectionFile(file, io.Discard, func(w io.Writer) error { return w.(*os.File).Close() })
	require.Error(t, err)
	require.NoFileExists(t, file)
}
func TestJobDAGHelp(t *testing.T) {
	var out bytes.Buffer
	c := dagCLI(&dagCLIAPI{}, "", &out)
	c.SetArgs([]string{"dag", "--help"})
	require.NoError(t, c.Execute())
	for _, word := range []string{"submission", "siblings", "--subtree", "--max-nodes", "--max-depth", "--timeout", "partial", "upgrade", "--json", "coverage.complete", "nodes", "edges", "recommended for agents", "--json --output family.json"} {
		require.Contains(t, out.String(), word)
	}
}

func TestJobDAGReadableFamily(t *testing.T) {
	longTitle := strings.Repeat("x", 68) + "\x1b[31m very long title"
	family := []jobinspect.FamilyNode{
		{ID: "parent-full-id", Tenant: "tenant", Cell: "recipes", Title: "Build application", Status: "ACTIVE"},
		{ID: "selected-full-id", Parent: "parent-full-id", Tenant: "tenant", Cell: "c2j", Title: "Implement DAG", Status: "ACTIVE"},
		{ID: "sibling-full-id", Parent: "parent-full-id", Tenant: "tenant", Cell: "c2ops", Title: longTitle, Status: "READY"},
		{ID: "descendant-full-id", Parent: "selected-full-id", Tenant: "tenant", Cell: "cortex", Title: "Explain ancestry", Status: "COMPLETED", CompletionStatus: "success"},
	}
	var out bytes.Buffer
	c := dagCLI(&dagCLIAPI{family: family}, "selected-full-id", &out)
	c.SetArgs([]string{"dag", "self"})
	require.NoError(t, c.Execute())
	text := out.String()
	lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	require.Len(t, lines, 4, "one line per job, without routine metadata")
	labels := []string{
		"▶ Build application",
		"|-- ▶ Implement DAG  [selected]",
		"|   `-- ✓ Explain ancestry",
		"`-- ○ " + strings.Repeat("x", 68) + "...",
	}
	ids := []string{"parent-full-id", "selected-full-id", "descendant-full-id", "sibling-full-id"}
	idColumn := -1
	for i, line := range lines {
		require.True(t, strings.HasPrefix(line, labels[i]+"  "), line)
		require.True(t, strings.HasSuffix(line, ids[i]), line)
		column := utf8.RuneCountInString(line[:strings.Index(line, ids[i])])
		if idColumn == -1 {
			idColumn = column
		}
		require.Equal(t, idColumn, column, "full IDs must share one column")
	}
	for _, unwanted := range []string{"very long title", "\x1b", "Ancestry:", "Coverage:", "Observed:", "Tenant:", "Agents:", "Warning:", "recipes", "c2ops"} {
		require.NotContains(t, text, unwanted)
	}

	// The inherited and local flag positions both produce the same structured
	// family, retaining the parent, sibling, descendant and unabridged labels.
	for _, args := range [][]string{{"dag", "self", "--json"}, {"--json", "dag", "self"}} {
		out.Reset()
		c = dagCLI(&dagCLIAPI{family: family}, "selected-full-id", &out)
		c.SetArgs(args)
		require.NoError(t, c.Execute())
		var d jobinspect.DAG
		require.NoError(t, json.Unmarshal(out.Bytes(), &d))
		require.Len(t, d.Nodes, 4)
		require.Len(t, d.Edges, 3)
		require.Equal(t, "parent-full-id", d.Root.ID)
		require.Equal(t, "selected-full-id", d.Selected.ID)
		require.True(t, d.Coverage.Complete)
		require.Equal(t, longTitle, d.Nodes[3].Title)
		require.NotContains(t, out.String(), "Agents:")
	}
}

func TestJobDAGStatusSymbols(t *testing.T) {
	for _, tc := range []struct {
		status, completion string
		cancelRequested    bool
		symbol             string
	}{
		{"ACTIVE", "", false, "▶"},
		{"READY", "", false, "○"},
		{"PENDING_JOBS", "", false, "…"},
		{"AWAITING_FUTURE", "", false, "…"},
		{"COMPLETED", "success", false, "✓"},
		{"COMPLETED", "succeeded", false, "✓"},
		{"COMPLETED", "failed_app", false, "✗"},
		{"COMPLETED", "failed_system", false, "✗"},
		{"COMPLETED", "failed_timeout", false, "✗"},
		{"COMPLETED", "", false, "?"},
		{"COMPLETED", "future_outcome", false, "?"},
		{"CANCELLED", "", false, "⊘"},
		{"COMPLETED", "cancelled", false, "⊘"},
		{"ACTIVE", "", true, "!"},
		{"COMPLETED", "success", true, "✓"},
		{"CRASH_CONCERN", "", false, "!"},
		{"EXPIRED", "", false, "!"},
		{"FUTURE_STATUS", "", false, "?"},
	} {
		t.Run(fmt.Sprintf("%s/%s/cancel=%v", tc.status, tc.completion, tc.cancelRequested), func(t *testing.T) {
			var out bytes.Buffer
			n := jobinspect.FamilyNode{ID: "job", Tenant: "tenant", Title: "A job", Status: tc.status, CompletionStatus: tc.completion, CancelRequested: tc.cancelRequested}
			c := dagCLI(&dagCLIAPI{family: []jobinspect.FamilyNode{n}}, "", &out)
			c.SetArgs([]string{"dag", "job"})
			require.NoError(t, c.Execute())
			require.Equal(t, tc.symbol+" A job  [selected]  job\n", out.String())
		})
	}
}

func TestJobDAGWarningsAndSubtree(t *testing.T) {
	family := []jobinspect.FamilyNode{
		{ID: "parent", Tenant: "tenant", Title: "Parent", Status: "ACTIVE"},
		{ID: "selected", Parent: "parent", Tenant: "tenant", Title: "Selected", Status: "READY"},
		{ID: "child", Parent: "selected", Tenant: "tenant", Title: "Child", Status: "ACTIVE"},
	}
	for _, subtree := range []bool{false, true} {
		var out bytes.Buffer
		c := dagCLI(&dagCLIAPI{family: family}, "", &out)
		args := []string{"dag", "selected"}
		if subtree {
			args = append(args, "--subtree")
		} else {
			args = append(args, "--max-depth", "1")
		}
		c.SetArgs(args)
		err := c.Execute()
		if subtree {
			require.NoError(t, err)
			require.NotContains(t, out.String(), "Warning:")
			require.NotContains(t, out.String(), "parent")
			require.Contains(t, out.String(), "child")
		} else {
			var coded exitCoder
			require.ErrorAs(t, err, &coded)
			require.Equal(t, 2, coded.ExitCode())
			require.Contains(t, out.String(), "Warning: tree is incomplete")
			require.Contains(t, out.String(), "Children of selected: depth_limit")
		}
	}
	// A malformed self-parent must warn without making the renderer recurse.
	n := jobinspect.FamilyNode{ID: "job", Parent: "job", Tenant: "tenant", Title: "Cycle", Status: "ACTIVE"}
	var out bytes.Buffer
	c := dagCLI(&dagCLIAPI{family: []jobinspect.FamilyNode{n}}, "", &out)
	c.SetArgs([]string{"dag", "job"})
	var coded exitCoder
	require.ErrorAs(t, c.Execute(), &coded)
	require.Equal(t, 2, coded.ExitCode())
	require.Contains(t, out.String(), "Warning:")
	require.Contains(t, out.String(), "Parents of job: cycle")
	require.Contains(t, out.String(), "Relationship job -> job: cycle")
}

func TestJobDAGAuthenticatedSelf(t *testing.T) {
	a := &dagCLIAPI{}
	current := jobcontext.Current{TenantID: "tenant", JobID: "selected-full-id", GoalID: "goal"}
	s, err := childbroker.Start(jobinspect.WithAPI(context.Background(), a), childbroker.Options{Current: current, Self: func() selfinfo.Info { return selfinfo.Unknown("") }})
	require.NoError(t, err)
	defer s.Close()
	for k, v := range jobcontext.EnvForCurrent(current) {
		t.Setenv(k, v)
	}
	for k, v := range s.Env() {
		t.Setenv(k, v)
	}
	t.Setenv("C2J_JOBDB", "invalid://ambient")
	var out bytes.Buffer
	c := newJobCmd()
	c.SetOut(&out)
	c.SetErr(io.Discard)
	c.SetArgs([]string{"dag", "self", "--json", "--jobdb", "invalid://override"})
	require.NoError(t, c.Execute())
	require.Contains(t, out.String(), `"id": "selected-full-id"`)
	s.Close()
	c = newJobCmd()
	c.SetOut(io.Discard)
	c.SetErr(io.Discard)
	c.SetArgs([]string{"dag", "self", "--jobdb", "invalid://override"})
	require.ErrorContains(t, c.Execute(), "helper unavailable")
	for k := range s.Env() {
		t.Setenv(k, "")
	}
	c = newJobCmd()
	c.SetOut(io.Discard)
	c.SetErr(io.Discard)
	c.SetArgs([]string{"dag", "self", "--jobdb", "invalid://override"})
	require.ErrorContains(t, c.Execute(), "upgrade")
}

type shortDAGWriter struct{}

func (shortDAGWriter) Write(p []byte) (int, error) { return len(p) - 1, nil }
func TestJobDAGSerializationAndShortWrite(t *testing.T) {
	var out bytes.Buffer
	api := &dagCLIAPI{invalidTime: true, failChildren: true}
	c := dagCLI(api, "", &out)
	file := filepath.Join(t.TempDir(), "dag.json")
	c.SetArgs([]string{"dag", "selected", "--json", "--output", file})
	err := c.Execute()
	require.Error(t, err)
	var partial partialDAGError
	require.False(t, errors.As(err, &partial))
	require.NoFileExists(t, file)
	require.Empty(t, out.String())
	c = dagCLI(&dagCLIAPI{failChildren: true}, "", shortDAGWriter{})
	c.SetArgs([]string{"dag", "selected"})
	require.ErrorIs(t, c.Execute(), io.ErrShortWrite)
}
