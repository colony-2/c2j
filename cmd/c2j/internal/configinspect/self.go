package configinspect

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/colony-2/c2j/cmd/c2j/internal/defaults"
	"github.com/colony-2/c2j/pkg/childbroker"
	configpkg "github.com/colony-2/c2j/pkg/config"
	"github.com/colony-2/c2j/pkg/jobcontext"
	"github.com/colony-2/c2j/pkg/secrets"
	"github.com/colony-2/c2j/pkg/selfinfo"
)

type SelfReport struct {
	SelfInfo
	selfinfo.Info
}

func RunSelf(ctx context.Context, opts SelfOptions) error {
	getenv := opts.Getenv
	if getenv == nil {
		getenv = os.Getenv
	}
	stdout := opts.Stdout
	if stdout == nil {
		stdout = os.Stdout
	}
	workingDir := opts.WorkingDir
	if workingDir == "" {
		var err error
		workingDir, err = os.Getwd()
		if err != nil {
			return fmt.Errorf("cannot resolve current working directory")
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	// Cell configuration can fail or use commands; bound it independently of the
	// invocation helper so a broken checkout cannot hide running-task identity.
	configCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	cell, diagnostics := inspectCell(configCtx, workingDir, getenv)
	cancel()
	info := inspectExecution(ctx, getenv, cell.JobDB != "")
	info.Diagnostics = append(info.Diagnostics, diagnostics...)
	if err := ctx.Err(); err != nil {
		return err
	}
	report := SelfReport{SelfInfo: cell, Info: info}
	redactor := secrets.NewRedactor()
	defer redactor.Close()
	for _, name := range []string{jobcontext.ChildJobTokenEnv, jobcontext.ChildJobSessionIDEnv} {
		if value := getenv(name); value != "" {
			_ = redactor.Register(value)
		}
	}
	raw, err := selfinfo.MarshalSafe(report, redactor.Redact)
	if err != nil {
		return fmt.Errorf("cannot encode self report")
	}
	// Use the same sanitized representation for both renderers.
	if err = json.Unmarshal(raw, &report); err != nil {
		return fmt.Errorf("cannot decode self report")
	}
	if opts.JSONOutput {
		if cell.Repo == "" {
			var object map[string]any
			decoder := json.NewDecoder(strings.NewReader(string(raw)))
			decoder.UseNumber()
			if err = decoder.Decode(&object); err != nil {
				return err
			}
			for _, key := range []string{"short_name", "repo", "ref", "root_repo", "root_ref", "pattern"} {
				if object[key] == "" {
					object[key] = nil
				}
			}
			if cell.JobDB == "" {
				object["jobdb"] = nil
			}
			return json.NewEncoder(stdout).Encode(object)
		}
		_, err = fmt.Fprintln(stdout, string(raw))
		return err
	}
	return writeSelf(stdout, report)
}

func inspectCell(ctx context.Context, dir string, getenv func(string) string) (SelfInfo, []selfinfo.Diagnostic) {
	var info SelfInfo
	diagnostics := []selfinfo.Diagnostic{}
	cfg, err := configpkg.LoadProjectConfig(dir)
	if err != nil {
		code, message := "cell_config_invalid", "Cell configuration could not be loaded"
		if errors.Is(err, configpkg.ErrConfigNotFound) {
			code, message = "cell_unavailable", "No cell configuration or supported auto-detection"
		}
		diagnostics = append(diagnostics, selfinfo.Diagnostic{Code: code, Message: message})
	} else {
		info.ConfigPath = cfg.Path()
		info.Packages = cfg.Packages()
		info.RepositoryExecution = cfg.Execution()
		for _, field := range []struct {
			name   string
			read   func(context.Context) (string, error)
			target *string
		}{
			{"repo", cfg.SelfRepo, &info.Repo}, {"ref", cfg.SelfRef, &info.Ref}, {"root_repo", cfg.RootRepo, &info.RootRepo}, {"root_ref", cfg.RootRef, &info.RootRef}, {"pattern", cfg.Pattern, &info.Pattern},
		} {
			value, e := field.read(ctx)
			if e != nil {
				diagnostics = append(diagnostics, selfinfo.Diagnostic{Code: "cell_field_unavailable", Message: "Could not resolve cell field: " + field.name})
				continue
			}
			*field.target = strings.TrimSpace(value)
		}
		if info.Repo != "" {
			info.ShortName = cfg.SelfCell()
		}
	}
	info.JobDB = strings.TrimSpace(getenv(defaults.JobDBEnv))
	if info.JobDB == "" && cfg != nil {
		value, e := cfg.JobDBURI(ctx)
		if e != nil {
			diagnostics = append(diagnostics, selfinfo.Diagnostic{Code: "submission_destination_unavailable", Message: "Could not resolve configured JobDB destination"})
		} else {
			info.JobDB = strings.TrimSpace(value)
		}
	}
	if info.JobDB != "" {
		if _, e := defaults.ParseJobDBURI(info.JobDB); e != nil {
			info.JobDB = ""
			diagnostics = append(diagnostics, selfinfo.Diagnostic{Code: "invalid_submission_destination", Message: "Configured JobDB destination is invalid"})
		}
	}
	return info, diagnostics
}

func inspectExecution(ctx context.Context, getenv func(string) string, destination bool) selfinfo.Info {
	current, present, incomplete := currentForInspection(getenv)
	broker, hasBroker, brokerErr := jobcontext.ChildJobBrokerFromEnv(getenv)
	if !present && !hasBroker {
		return selfinfo.Standalone(destination)
	}
	reason := "helper_unavailable"
	if incomplete || brokerErr != nil || !current.HasJob() {
		reason = "incomplete_execution_context"
	}
	info := selfinfo.Unknown(reason)
	if current.HasJob() {
		info.SetCurrent(current, "environment_unverified")
	}
	fail := func(code, message string) selfinfo.Info {
		info.Lease.ReasonCode = code
		info.Permissions.ReasonCode = code
		for action, c := range info.Capabilities {
			c.ReasonCode = selfinfo.String(code)
			info.Capabilities[action] = c
		}
		info.Diagnostics = append(info.Diagnostics, selfinfo.Diagnostic{Code: code, Message: message})
		return info
	}
	if reason == "incomplete_execution_context" {
		return fail(reason, "Execution context is incomplete or unsupported; authority could not be verified")
	}
	if !hasBroker {
		return fail("helper_unavailable", "Current job metadata is present but no runtime helper is available")
	}
	remote, err := childbroker.Inspect(ctx, broker)
	if err != nil {
		code := "helper_unavailable"
		var inspection *childbroker.InspectionError
		if errors.As(err, &inspection) {
			code = inspection.Code
		}
		return fail(code, "Runtime inspection unavailable; current authority is unknown")
	}
	for _, pair := range [][2]string{
		{current.TenantID, selfinfo.Value(remote.Execution.TenantID)},
		{current.JobID, selfinfo.Value(remote.Execution.JobID)},
		{current.GoalID, selfinfo.Value(remote.Execution.GoalID)},
		{current.InvocationHash, selfinfo.Value(remote.Execution.InvocationHash)},
		{current.InvocationPath, selfinfo.Value(remote.Execution.InvocationPath)},
	} {
		if pair[0] != "" && pair[0] != pair[1] {
			return fail("execution_identity_mismatch", "Runtime helper identity does not match the advertised invocation")
		}
	}
	return remote
}

// Inspection tolerates missing optional goal metadata and preserves partial job
// identity. Submission's stricter environment parser remains unchanged.
func currentForInspection(getenv func(string) string) (jobcontext.Current, bool, bool) {
	var c jobcontext.Current
	present := false
	for _, f := range []struct {
		name string
		dst  *string
	}{
		{jobcontext.CurrentTenantIDEnv, &c.TenantID}, {jobcontext.CurrentJobIDEnv, &c.JobID}, {jobcontext.CurrentGoalIDEnv, &c.GoalID},
		{jobcontext.CurrentJobTypeEnv, &c.JobType}, {jobcontext.CurrentOpTypeEnv, &c.OpType}, {jobcontext.CurrentOpStepEnv, &c.OpStep},
		{jobcontext.CurrentOpTaskTypeEnv, &c.OpTaskType}, {jobcontext.CurrentCellNameEnv, &c.CellName},
		{jobcontext.CurrentRepositorySourceEnv, &c.RepositorySource}, {jobcontext.CurrentGitRefEnv, &c.GitRef},
		{jobcontext.CurrentInvocationPathEnv, &c.InvocationPath}, {jobcontext.CurrentInvocationHashEnv, &c.InvocationHash},
	} {
		*f.dst = strings.TrimSpace(getenv(f.name))
		present = present || *f.dst != ""
	}
	version := strings.TrimSpace(getenv(jobcontext.CurrentContextVersionEnv))
	present = present || version != ""
	incomplete := version != "" && version != jobcontext.CurrentContextVersion
	if raw := getenv(jobcontext.CurrentInvocationSequenceEnv); raw != "" {
		present = true
		seq, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			incomplete = true
		} else {
			c.InvocationSequence = seq
		}
	}
	return c, present, incomplete || present && !c.HasJob()
}

func writeSelf(out io.Writer, r SelfReport) error {
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	var writeErr error
	row := func(label, value string) {
		if writeErr != nil {
			return
		}
		_, writeErr = fmt.Fprintf(w, "%s\t%s\n", label, displayDescription(value))
	}
	optional := func(s *string) string {
		if s == nil {
			return "unknown"
		}
		return *s
	}
	timestamp := func(t *time.Time) string {
		if t == nil {
			return "unknown"
		}
		return t.UTC().Format(time.RFC3339)
	}
	if r.Repo != "" {
		row("FIELD", "VALUE")
		for _, f := range [][2]string{{"short_name", r.ShortName}, {"repo", r.Repo}, {"ref", r.Ref}, {"jobdb", r.JobDB}, {"root_repo", r.RootRepo}, {"root_ref", r.RootRef}, {"pattern", r.Pattern}, {"packages", strings.Join(r.Packages, ", ")}, {"config_path", r.ConfigPath}} {
			row(f[0], displayValue(f[1]))
		}
	} else {
		row("Cell:", "unavailable — no resolved cell configuration")
		if r.JobDB != "" {
			row("JobDB:", r.JobDB)
		}
	}
	if writeErr == nil {
		_, writeErr = fmt.Fprintln(w)
	}
	if r.Execution.Status == "absent" {
		row("Execution:", "standalone — no current job or task detected")
		row("Lease:", "none detected")
		row("Task workspace:", "none; the current checkout is not a task workspace")
		row("Permissions:", "no current-task grant; standalone JobDB permissions are unknown")
	} else {
		e := r.Execution
		row("Execution:", e.Status+" ("+e.Source+")")
		row("Job:", optional(e.JobID))
		row("Tenant:", optional(e.TenantID))
		row("Goal:", optional(e.GoalID))
		row("Task ID:", optional(e.TaskID))
		row("Task type:", optional(e.TaskType))
		attempt, ordinal := "unknown", "unknown"
		if e.AttemptNumber != nil {
			attempt = strconv.Itoa(*e.AttemptNumber)
		}
		if e.ResultOrdinal != nil {
			ordinal = strconv.FormatInt(*e.ResultOrdinal, 10)
		}
		row("Task attempt:", attempt)
		row("Result ordinal:", ordinal)
		row("Invocation:", optional(e.InvocationPath))
		row("Operation:", optional(e.OpType))
		row("Owning cell:", optional(e.CellName))
		row("Workspace cell:", optional(r.Workspace.CellName))
		row("Workspace scope:", optional(r.Workspace.ScopeID))
		if p := r.Workspace.Paths; p != nil {
			row("Workdir:", p.Workdir)
			row("Worktree:", p.Worktree)
			row("Inbox:", p.Inbox)
			row("Outbox:", p.Outbox)
		}
		row("Authority:", r.Lease.AuthorityMode)
		row("Lease:", optional(r.Lease.LeaseID)+" — "+r.Lease.Status+" ("+r.Lease.Validation+")")
		row("Observed:", r.ObservedAt.UTC().Format(time.RFC3339))
		row("Lease expiry:", timestamp(r.Lease.ExpiresAt)+" (renewable; not the task deadline)")
		row("Task deadline:", timestamp(r.Lease.OperationDeadline))
		row("Renewal owner:", optional(r.Lease.RenewalOwner))
		row("Permissions:", r.Permissions.Status)
	}
	if writeErr != nil {
		return writeErr
	}
	if err := w.Flush(); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(w, "\nACTION\tPERMISSION\tAVAILABILITY\tROUTE / SCOPE"); err != nil {
		return err
	}
	actions := []struct{ key, label string }{{"jobs.submit", "Submit child job"}, {"jobs.submit_from_workspace", "Submit incoming workspace"}, {"jobs.submit_from_workspace_now", "Submit current workspace"}, {"secrets.resolve", "Resolve named secrets"}}
	if r.Execution.Status == "absent" {
		actions[0].label = "Submit a new job"
		actions[2].label = "Submit current checkout"
	}
	for _, a := range actions {
		c, ok := r.Capabilities[a.key]
		if !ok {
			continue
		}
		route := selfinfo.Value(c.Command)
		if c.Route != nil {
			route += " via " + *c.Route
		}
		if c.ReasonCode != nil {
			route += "; " + *c.ReasonCode
		}
		if len(c.Restrictions) > 0 {
			route += "; " + strings.Join(c.Restrictions, ", ")
		}
		if len(c.Names) > 0 {
			names := c.Names
			if len(names) > 6 {
				names = names[:6]
			}
			route += "; names: " + strings.Join(names, ", ")
			if len(names) < len(c.Names) {
				route += fmt.Sprintf(" (%d total; see --json)", len(c.Names))
			}
		}
		if _, err := fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", a.label, displayDescription(c.Permission), displayDescription(c.Availability), displayDescription(strings.TrimSpace(route))); err != nil {
			return err
		}
	}
	if err := w.Flush(); err != nil {
		return err
	}
	for _, d := range r.Diagnostics {
		if _, err := fmt.Fprintf(out, "\n%s: %s\n", displayDescription(d.Code), displayDescription(d.Message)); err != nil {
			return err
		}
	}
	if r.Execution.Status == "absent" {
		_, err := fmt.Fprintln(out, "\nStandalone submission may work using a configured connection; no authority probe was performed.")
		if err != nil {
			return err
		}
	}
	_, err := fmt.Fprintln(out, "\nUse c2j self --json for structured details. Unknown means unverified, not allowed or denied.")
	return err
}
