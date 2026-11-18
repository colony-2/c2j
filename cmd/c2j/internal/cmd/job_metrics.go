package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/colony-2/c2j/cmd/c2j/internal/defaults"
	"github.com/colony-2/c2j/pkg/jobinspect"
	"github.com/colony-2/c2j/pkg/metrics"
	"github.com/spf13/cobra"
)

func newJobMetricsCmd(uri *string, embed, jsonOutput *bool, open func(context.Context, string) (jobinspect.API, func(), string, error)) *cobra.Command {
	q := metrics.Query{}
	var output string
	var cutoff int64
	cmd := &cobra.Command{Use: "metrics JOB", Short: "Reduce committed task metrics by labels, scope, task or result ordinal", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		request := q
		if cmd.Flags().Changed("through-ordinal") {
			request.ThroughOrdinal = &cutoff
		}
		if cmd.Flags().Changed("attempt") && q.Attempt < 1 {
			return fmt.Errorf("attempt must be positive")
		}
		if e := request.Validate(); e != nil {
			return e
		}
		target := *uri
		if *embed {
			target = defaults.EmbedURL
		}
		api, close, current, e := open(cmd.Context(), target)
		if e != nil {
			return e
		}
		defer close()
		id := args[0]
		if id == "self" {
			if current == "" {
				return fmt.Errorf("self requires a current task")
			}
			id = current
		}
		result, readErr := api.Inspect(cmd.Context(), jobinspect.Request{Operation: "metrics", JobID: id, Metrics: &request})
		if result == nil || result.Metrics == nil {
			if readErr != nil {
				return readErr
			}
			return fmt.Errorf("metrics inspection unavailable; upgrade the worker and CLI together")
		}
		write := func(w io.Writer) error {
			if *jsonOutput || output != "" || request.Deltas {
				return json.NewEncoder(w).Encode(result.Metrics)
			}
			return renderMetrics(w, result.Metrics)
		}
		if output != "" {
			e = writeInspectionFile(output, cmd.OutOrStdout(), write)
		} else {
			e = write(cmd.OutOrStdout())
		}
		if e != nil {
			return e
		}
		return readErr
	}}
	f := cmd.Flags()
	f.StringVar(&q.Name, "name", "", "Select one metric name")
	f.StringVar(&q.Scope, "scope", "", "Native scope occurrence ID from job story")
	f.StringVar(&q.Task, "task", "", "Native task occurrence ID from job log")
	f.IntVar(&q.Attempt, "attempt", 0, "Narrow the selected task or scope to one attempt")
	f.Int64Var(&cutoff, "through-ordinal", 0, "Include results at or before this physical chapter ordinal")
	f.BoolVar(&q.Deltas, "deltas", false, "Emit ordered persisted reports as JSON")
	f.StringVar(&output, "output", "", "Write exact JSON to a new file; - writes stdout")
	return cmd
}
func renderMetrics(out io.Writer, r *metrics.Result) error {
	s := r.Snapshot
	fmt.Fprintf(out, "Reported metrics through result ordinal %d · %s history (%s)\n", s.Cutoff, s.HistoryView, s.Coverage.History)
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "METRIC / LABELS\tKIND\tVALUE / COUNT\tSUM\tMIN\tMEAN\tMAX")
	names := make([]string, 0, len(s.Series))
	for name := range s.Series {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		for _, p := range s.Series[name] {
			labels, _ := json.Marshal(p.Labels)
			if len(p.Labels) == 0 {
				labels = []byte("{}")
			}
			if p.Histogram != nil {
				h := p.Histogram
				min, max, mean := "—", "—", "—"
				if h.Count > 0 {
					min = fmt.Sprint(h.Min)
					max = fmt.Sprint(h.Max)
					mean = fmt.Sprintf("%.6g", metrics.Float(h.Sum)/float64(h.Count))
				}
				fmt.Fprintf(w, "%s %s\t%s [%s]\t%d\t%v\t%s\t%s\t%s\n", name, labels, p.Kind, p.Unit, h.Count, h.Sum, min, mean, max)
			} else {
				fmt.Fprintf(w, "%s %s\t%s [%s]\t%v\n", name, labels, p.Kind, p.Unit, p.Value)
			}
		}
	}
	if e := w.Flush(); e != nil {
		return e
	}
	if len(names) == 0 {
		fmt.Fprintln(out, "No measurements reported for this selection.")
	}
	channels := []string{"runtime", "producer"}
	for _, channel := range channels {
		counts := s.Coverage.Reports[channel]
		keys := make([]string, 0, len(counts))
		for k := range counts {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var parts []string
		for _, k := range keys {
			parts = append(parts, fmt.Sprintf("%s=%d", k, counts[k]))
		}
		if len(parts) > 0 {
			fmt.Fprintf(out, "%s reports: %s\n", channel, strings.Join(parts, ", "))
		}
	}
	cpu := s.Coverage.CPU
	if cpu.MeasuredSeconds > 0 {
		fmt.Fprintf(out, "CPU: %.3g average busy cores of the c2j process over %.3g s measured; subprocess CPU excluded.\n", cpu.ProcessSeconds/cpu.MeasuredSeconds, cpu.MeasuredSeconds)
	} else {
		fmt.Fprintf(out, "CPU: %s; no measured interval in this selection.\n", cpu.Availability)
	}
	reasons := make([]string, 0, len(cpu.ExcludedSeconds))
	for k := range cpu.ExcludedSeconds {
		reasons = append(reasons, k)
	}
	sort.Strings(reasons)
	for _, k := range reasons {
		fmt.Fprintf(out, "CPU coverage: %.3g task-window seconds excluded (%s).\n", cpu.ExcludedSeconds[k], k)
	}
	for _, d := range s.Coverage.Diagnostics {
		fmt.Fprintln(out, d)
	}
	return nil
}
