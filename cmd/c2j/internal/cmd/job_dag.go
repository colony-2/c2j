package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/colony-2/c2j/cmd/c2j/internal/defaults"
	"github.com/colony-2/c2j/pkg/jobinspect"
	"github.com/spf13/cobra"
)

type inspectionOpener func(context.Context, string) (jobinspect.API, func(), string, error)
type partialDAGError struct{ upgrade bool }

func (e partialDAGError) Error() string {
	if e.upgrade {
		return "submission family coverage is partial; upgrade the worker and CLI together for family inspection support"
	}
	return "submission tree is incomplete; use --json for branch and relationship details"
}
func (partialDAGError) ExitCode() int { return 2 }

func newJobDAGCmd(uri *string, embed, jsonOutput *bool, open inspectionOpener) *cobra.Command {
	options := jobinspect.DefaultDAGOptions()
	var output string
	c := &cobra.Command{Use: "dag JOB", Short: "Show a recorded submission family as a text tree or JSON", Long: `Show recorded submission ancestry and descendants, including siblings and cousins.
An edge means the parent submitted the child; waits and shared goals are not edges.
JOB may be self inside a task. Reads follow JobDB permissions in one tenant.
Use --subtree to skip ancestry and show only the selected job and descendants.

For agents, use --json: a version-1 document with full nodes, typed submission
edges, selected/root identities, observation times, limits, counts and coverage.
Check coverage.complete and branch reasons even when the command exits 2.
Text shortens long titles; JSON preserves every title and ID in full.
Text rows show a status symbol, title and aligned full ID; [selected] marks JOB.
Symbols: ▶ active, ○ ready, … waiting, ✓ succeeded, ✗ failed, ⊘ cancelled,
! cancellation requested or scheduler concern, ? unknown. Warnings appear only
when the tree is incomplete or inconsistent.

Defaults: 1000 nodes, 64 descendant levels, 30s. Fixed safety caps: 64 ancestor
hops, 200 child pages and 300 requests. Unfinished branches are explicit.
Exit 0 means observed listings exhausted, 2 means usable partial output was
written, and 1 means invalid input, entry/protocol failure or output failure.
This is an observation interval; rerun to refresh. Task helpers need family
inspection support: upgrade the worker and CLI together.`, Example: `  c2j job dag JOB
  c2j job dag self --json
  c2j job dag JOB --json --output family.json
  jq '{selected, nodes, edges, coverage}' family.json`, Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if err := options.Validate(); err != nil {
			return err
		}
		target := *uri
		if *embed {
			target = defaults.EmbedURL
		}
		api, close, current, err := open(cmd.Context(), target)
		if err != nil {
			return err
		}
		defer close()
		id := args[0]
		if id == "self" {
			if current == "" {
				return fmt.Errorf("self requires a current task")
			}
			id = current
		}
		d, err := jobinspect.ReadDAG(cmd.Context(), api, id, options)
		if err != nil {
			return err
		}
		var data []byte
		if *jsonOutput {
			data, err = json.MarshalIndent(d, "", "  ")
			data = append(data, '\n')
		} else {
			data, err = renderDAG(d)
		}
		if err != nil {
			return err
		}
		destination := output
		if destination == "" {
			destination = "-"
		}
		if err = writeInspectionFile(destination, cmd.OutOrStdout(), func(w io.Writer) error {
			n, e := w.Write(data)
			if e == nil && n != len(data) {
				e = io.ErrShortWrite
			}
			return e
		}); err != nil {
			return err
		}
		if !d.Coverage.Complete {
			upgrade := d.Coverage.Ancestry.Reason == "unsupported_protocol"
			for _, b := range d.Coverage.Children {
				upgrade = upgrade || b.Reason == "unsupported_protocol"
			}
			return partialDAGError{upgrade: upgrade}
		}
		return nil
	}}
	f := c.Flags()
	f.BoolVar(jsonOutput, "json", false, "Emit structured JSON with full nodes, edges and coverage (recommended for agents)")
	f.BoolVar(&options.Subtree, "subtree", false, "Show descendants of JOB without following its parents")
	f.IntVar(&options.MaxNodes, "max-nodes", options.MaxNodes, "Maximum retained jobs (positive)")
	f.IntVar(&options.MaxDepth, "max-depth", options.MaxDepth, "Maximum descendant levels from the highest retained ancestor (positive)")
	f.DurationVar(&options.Timeout, "timeout", options.Timeout, "Maximum observation duration (positive)")
	f.StringVar(&output, "output", "", "Write selected text/JSON format to a new file; - writes stdout")
	return c
}

// Quoting escapes terminal controls, including ESC, newlines and bidi controls.
func dagLabel(s string) string { q := strconv.QuoteToASCII(s); return q[1 : len(q)-1] }

// Keep the title line scannable without cutting an escape sequence. Full labels
// remain available in JSON; identities are never shortened.
func dagTitle(s string) string {
	if s == "" {
		return "(untitled job)"
	}
	const width = 72
	label := dagLabel(s)
	if len(label) <= width {
		return label
	}
	var title strings.Builder
	for _, r := range s {
		escaped := dagLabel(string(r))
		if title.Len()+len(escaped) > width-3 {
			break
		}
		title.WriteString(escaped)
	}
	return title.String() + "..."
}

func dagStatus(n jobinspect.FamilyNode) string {
	switch n.Status {
	case "CANCELLED":
		return "⊘"
	case "COMPLETED", "ARCHIVED":
		switch n.CompletionStatus {
		case "success", "succeeded":
			return "✓"
		case "cancelled":
			return "⊘"
		case "failed_app", "failed_system", "failed_timeout":
			return "✗"
		default:
			return "?"
		}
	}
	if n.CancelRequested {
		return "!"
	}
	switch n.Status {
	case "ACTIVE":
		return "▶"
	case "READY":
		return "○"
	case "PENDING_JOBS", "AWAITING_FUTURE":
		return "…"
	case "EXPIRED", "CRASH_CONCERN":
		return "!"
	default:
		return "?"
	}
}

func renderDAG(d *jobinspect.DAG) ([]byte, error) {
	var out bytes.Buffer
	nodes := map[string]jobinspect.FamilyNode{}
	children := map[string][]string{}
	for _, n := range d.Nodes {
		nodes[n.ID] = n
	}
	for _, e := range d.Edges {
		children[e.Parent] = append(children[e.Parent], e.Child)
	}
	// Edges and nodes are already sorted by the traversal contract.
	type row struct{ id, prefix, connector string }
	type displayRow struct{ label, id string }
	var rows []displayRow
	width := 0
	stack := []row{{id: d.Root.ID}}
	for len(stack) > 0 {
		item := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		n := nodes[item.id]
		label := item.prefix + item.connector + dagStatus(n) + " " + dagTitle(n.Title)
		if n.ID == d.Selected.ID {
			label += "  [selected]"
		}
		rows = append(rows, displayRow{label, dagLabel(n.ID)})
		// Titles and connectors are escaped ASCII; each status symbol occupies
		// one monospace column. Count runes rather than UTF-8 bytes for padding.
		if w := utf8.RuneCountInString(label); w > width {
			width = w
		}
		prefix := item.prefix
		if item.connector != "" {
			if item.connector == "`-- " {
				prefix += "    "
			} else {
				prefix += "|   "
			}
		}
		ids := children[item.id]
		for i := len(ids) - 1; i >= 0; i-- {
			connector := "|-- "
			if i == len(ids)-1 {
				connector = "`-- "
			}
			stack = append(stack, row{ids[i], prefix, connector})
		}
	}
	for _, row := range rows {
		fmt.Fprintf(&out, "%s%s  %s\n", row.label, strings.Repeat(" ", width-utf8.RuneCountInString(row.label)), row.id)
	}
	if d.Coverage.Complete {
		return out.Bytes(), nil
	}

	fmt.Fprintln(&out, "\nWarning: tree is incomplete or inconsistent; use --json for details.")
	a := d.Coverage.Ancestry
	if a.State != "exhausted" && a.State != "not_requested" {
		fmt.Fprintf(&out, "  Parents of %s: %s\n", dagLabel(a.ID), dagLabel(a.Reason))
	}
	for _, b := range d.Coverage.Children {
		if b.State != "exhausted" {
			fmt.Fprintf(&out, "  Children of %s: %s\n", dagLabel(b.ID), dagLabel(b.Reason))
		}
	}
	for _, a := range d.Coverage.Anomalies {
		fmt.Fprintf(&out, "  Relationship %s -> %s: %s\n", dagLabel(a.Parent), dagLabel(a.ID), dagLabel(a.Reason))
	}
	return out.Bytes(), nil
}
