package submitjob

import (
	"fmt"
	"strings"

	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/spf13/pflag"
)

type WaitKind string

const (
	WaitJob        WaitKind = "wait-job"
	WaitJobSuccess WaitKind = "wait-job-success"
	WaitGoal       WaitKind = "wait-goal"
)

// Wait is one literal flag occurrence. IDs are never split at commas.
type Wait struct {
	Kind WaitKind
	ID   string
}

// AddWaitFlags keeps cross-flag order and explicitly empty occurrences.
func (o *Options) AddWaitFlags(flags *pflag.FlagSet) {
	for _, flag := range []struct {
		kind WaitKind
		help string
	}{
		{WaitJob, "Require this exact job to reach any terminal outcome; repeatable"},
		{WaitJobSuccess, "Require this exact job to succeed (terminal failure blocks execution); repeatable"},
		{WaitGoal, "Unsupported: use --wait-job or --wait-job-success"},
	} {
		kind := flag.kind
		flags.Var(&waitFlag{append: func(value string) { o.Waits = append(o.Waits, Wait{Kind: kind, ID: value}) }}, string(kind), flag.help)
	}
	flags.Var(&waitFlag{append: func(value string) { o.WaitUntil = append(o.WaitUntil, value) }}, "wait-until", "Earliest start: RFC3339, local date/time or clock, or positive duration from submission (e.g. 1h30m); once")
}

type waitFlag struct{ append func(string) }

func (*waitFlag) String() string           { return "" }
func (*waitFlag) Type() string             { return "string" }
func (f *waitFlag) Set(value string) error { f.append(value); return nil }

func (o Options) dependencies() ([]jobdb.Dependency, error) {
	var out []jobdb.Dependency
	seen := make(map[Wait]bool)
	jobs := make(map[string]WaitKind)
	for _, raw := range o.Waits {
		w := Wait{Kind: raw.Kind, ID: strings.TrimSpace(raw.ID)}
		if w.ID == "" {
			return nil, fmt.Errorf("--%s requires a nonempty literal ID", w.Kind)
		}
		d := jobdb.Dependency{Condition: jobdb.JobPrereqSuccess}
		switch w.Kind {
		case WaitJob, WaitJobSuccess:
			if w.ID == strings.TrimSpace(o.JobID) {
				return nil, fmt.Errorf("--%s cannot wait on the submitted job itself", w.Kind)
			}
			if prior, ok := jobs[w.ID]; ok && prior != w.Kind {
				return nil, fmt.Errorf("job %q cannot use both --wait-job and --wait-job-success", w.ID)
			}
			jobs[w.ID] = w.Kind
			d.Target.JobID = w.ID
			if w.Kind == WaitJob {
				d.Condition = jobdb.JobPrereqComplete
			}
		case WaitGoal:
			return nil, fmt.Errorf("--wait-goal is unsupported; use --wait-job or --wait-job-success")
		default:
			return nil, fmt.Errorf("unknown wait kind %q", w.Kind)
		}
		if !seen[w] {
			out = append(out, d)
			seen[w] = true
		}
	}
	return out, nil
}
