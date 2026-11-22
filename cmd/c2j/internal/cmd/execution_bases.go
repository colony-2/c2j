package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/colony-2/c2j/cmd/c2j/internal/defaults"
	"github.com/colony-2/c2j/cmd/c2j/internal/swfruntime"
	"github.com/colony-2/c2j/pkg/execution"
	"github.com/colony-2/c2j/pkg/executioncatalog"
	"github.com/colony-2/c2j/pkg/gitprofile"
	"github.com/spf13/cobra"
)

func newExecutionCmd() *cobra.Command {
	c := &cobra.Command{Use: "execution", Short: "Inspect execution support"}
	var uri string
	var embed, jsonOutput bool
	bases := &cobra.Command{Use: "bases", Short: "Inspect and edit declared execution support", Args: cobra.NoArgs}
	bases.PersistentFlags().StringVar(&uri, "jobdb", "", "JobDB URI")
	bases.PersistentFlags().BoolVar(&embed, "embed", false, "Use embedded JobDB")
	bases.PersistentFlags().BoolVar(&jsonOutput, "json", false, "Emit structured JSON")
	fail := func(cmd *cobra.Command, err error) error {
		if err != nil && jsonOutput {
			code := "catalog_error"
			if errors.Is(err, errCatalogConflict) {
				code = "conflict"
			}
			var uncertain *catalogPublicationUncertain
			if errors.As(err, &uncertain) {
				code = "publication_uncertain"
			}
			v := map[string]any{"error": code, "reason": err.Error()}
			if uncertain != nil {
				v["candidate"] = json.RawMessage(uncertain.Candidate)
			}
			if e := json.NewEncoder(cmd.OutOrStdout()).Encode(v); e != nil {
				return e
			}
		}
		if err != nil && !jsonOutput {
			var uncertain *catalogPublicationUncertain
			if errors.As(err, &uncertain) {
				fmt.Fprintf(cmd.ErrOrStderr(), "Exact publication candidate: %s\n", uncertain.Candidate)
			}
		}
		return err
	}
	open := func(cmd *cobra.Command) (*swfruntime.Handle, executioncatalog.Policy, error) {
		if inBaseOperation() {
			return nil, executioncatalog.Policy{}, errors.New("typed discovery requires the live execution helper; administration requires a provisioning context")
		}
		targetURI := uri
		if embed {
			targetURI = defaults.EmbedURL
		}
		target, err := defaults.ResolveJobDBTarget(cmd.Context(), "", targetURI)
		if err != nil {
			return nil, executioncatalog.Policy{}, err
		}
		if target.RuntimeURL == "" {
			return nil, executioncatalog.Policy{}, errors.New("--jobdb is required")
		}
		h, err := swfruntime.Open(cmd.Context(), target.RuntimeURL)
		if err != nil {
			return nil, executioncatalog.Policy{}, errors.New("configuration administration connection failed")
		}
		p, err := defaults.ExecutionBasePolicy(target.RuntimeURL, target.TenantID, h.Configuration)
		if err != nil {
			h.Cleanup()
			return nil, p, err
		}
		return h, p, nil
	}
	discover := func(cmd *cobra.Command) (executioncatalog.View, error) {
		if inBaseOperation() {
			if cmd.Flags().Changed("jobdb") || cmd.Flags().Changed("embed") {
				return executioncatalog.View{}, errors.New("operation target overrides are not allowed; use the trusted helper scope")
			}
			socket := os.Getenv(gitprofile.SocketEnv)
			if socket == "" {
				return executioncatalog.View{}, errors.New("typed discovery requires the live execution helper; administration requires a provisioning context")
			}
			res, err := gitprofile.Call(cmd.Context(), socket, gitprofile.Request{Operation: "execution_bases"})
			var v executioncatalog.View
			if len(res.Value) > 0 {
				if e := json.Unmarshal(res.Value, &v); e != nil {
					return v, e
				}
			}
			return v, err
		}
		h, p, err := open(cmd)
		if err != nil {
			return executioncatalog.View{}, err
		}
		defer h.Cleanup()
		v, err := p.DiscoverForAdministration(cmd.Context())
		return v, err
	}
	inspect := func(cmd *cobra.Command, args []string) error {
		if len(args) > 0 {
			if err := execution.ValidateNamedBase(args[0]); err != nil {
				return fail(cmd, err)
			}
		}
		v, err := discover(cmd)
		if err != nil && v.Status == "" {
			v.Status = "unavailable"
			v.Reason = err.Error()
		}
		if len(args) > 0 && v.Catalog != nil {
			filtered := []execution.BaseOffering{}
			for _, o := range v.Catalog.Offerings {
				if o.Base == args[0] {
					filtered = append(filtered, o)
				}
			}
			if len(filtered) == 0 {
				v.Status = "not_found"
				v.Reason = "base is not declared"
				v.Catalog = nil
				err = errors.New(v.Reason)
			} else {
				v.Filtered = true
				v.Catalog.Offerings = filtered
				v.Catalog.Complete = false
			}
		}
		if jsonOutput {
			if e := json.NewEncoder(cmd.OutOrStdout()).Encode(v); e != nil {
				return e
			}
		} else {
			renderBaseView(cmd, v)
		}
		return err
	}
	bases.RunE = inspect
	bases.AddCommand(&cobra.Command{Use: "get [BASE]", Short: "Inspect all offerings or one named base", Args: cobra.MaximumNArgs(1), RunE: inspect})
	var caps []string
	var cpu, memory string
	check := &cobra.Command{Use: "check [SELECTOR]", Short: "Explain whether one offering accepts the full request", Args: cobra.MaximumNArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		needs := execution.Requirements{Capabilities: caps}
		if len(args) > 0 {
			needs.Base = &args[0]
		}
		if cmd.Flags().Changed("cpu") {
			needs.Resources.CPU = &cpu
		}
		if cmd.Flags().Changed("memory") {
			needs.Resources.Memory = &memory
		}
		result := (execution.BaseCatalog{}).Evaluate(needs)
		var v executioncatalog.View
		var err error
		if result.Decision != "invalid_request" {
			v, err = discover(cmd)
			result.Reasons = []execution.CatalogReason{}
			if err != nil {
				result.Decision = "unavailable"
			} else if v.Catalog == nil {
				result.Decision = v.Status
			} else {
				result = v.Catalog.Evaluate(needs)
			}
		}
		response := struct {
			execution.CatalogEvaluation
			Scope      execution.CatalogScope `json:"scope"`
			Revision   string                 `json:"revision,omitempty"`
			Generation string                 `json:"generation,omitempty"`
			Reason     string                 `json:"reason,omitempty"`
		}{CatalogEvaluation: result, Scope: v.Scope, Revision: v.Revision, Reason: v.Reason}
		if err != nil && response.Reason == "" {
			response.Reason = err.Error()
		}
		if v.Catalog != nil {
			response.Generation = v.Catalog.Generation
		}
		if jsonOutput {
			if e := json.NewEncoder(cmd.OutOrStdout()).Encode(response); e != nil {
				return e
			}
		} else {
			fmt.Fprintln(cmd.OutOrStdout(), "Decision:", strings.ReplaceAll(result.Decision, "_", " "))
			fmt.Fprintf(cmd.OutOrStdout(), "Scope: %s / %s   Revision: %s\n", v.Scope.Runtime, v.Scope.Tenant, v.Revision)
			for _, id := range result.Matches {
				fmt.Fprintf(cmd.OutOrStdout(), "Allowed by %s/%s\n", id.Base, id.Name)
			}
			for _, r := range result.Reasons {
				offer := ""
				if r.Offering != nil {
					offer = r.Offering.Base + "/" + r.Offering.Name + ": "
				}
				allowed := "none"
				switch value := r.Allowed.(type) {
				case []string:
					if len(value) > 0 {
						allowed = strings.Join(value, ", ")
					}
				case *execution.ResourceRule:
					allowed = renderResourceRule(value)
				}
				field := strings.TrimPrefix(r.Field, "resources.")
				if field == "capabilities" {
					field = "capability"
				}
				fmt.Fprintf(cmd.OutOrStdout(), "%s%s %s: %s (allowed: %s)\n", offer, field, r.Requested, r.Reason, allowed)
			}
			if response.Reason != "" {
				fmt.Fprintln(cmd.OutOrStdout(), response.Reason)
			}
		}
		if result.Decision != "allowed" {
			return fmt.Errorf("execution requirements: %s", result.Decision)
		}
		return nil
	}}
	check.Flags().StringArrayVar(&caps, "capability", nil, "Required capability (repeatable)")
	check.Flags().StringVar(&cpu, "cpu", "", "Explicit CPU request")
	check.Flags().StringVar(&memory, "memory", "", "Explicit memory request")
	bases.AddCommand(check)
	mutation := func(command *cobra.Command, apply catalogEdit) {
		command.RunE = func(cmd *cobra.Command, args []string) error {
			if inBaseOperation() {
				return fail(cmd, errors.New("base catalog management requires a provisioning context"))
			}
			// Input files are opened only after the operation guard.
			var raw []byte
			if cmd.Flags().Lookup("file") != nil {
				file, _ := cmd.Flags().GetString("file")
				if file == "" {
					return fail(cmd, errors.New("--file is required"))
				}
				var err error
				raw, err = readProfileFile(file, cmd.InOrStdin(), execution.BaseCatalogMaxBytes)
				if err != nil {
					return fail(cmd, err)
				}
			}
			h, p, err := open(cmd)
			if err != nil {
				return fail(cmd, err)
			}
			defer h.Cleanup()
			receipt, err := editBaseCatalog(cmd.Context(), h.Configuration, p.Scope, cmd, args, raw, apply, time.Now().UTC())
			if err != nil {
				return fail(cmd, err)
			}
			if jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(receipt)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Changed: %t   Scope: %s / %s\nRevision: %s   Generation: %s   Expires: %s\n", receipt.Changed, receipt.Scope.Runtime, receipt.Scope.Tenant, receipt.Revision, receipt.Generation, receipt.ExpiresAt.Format(time.RFC3339))
			for _, change := range receipt.Changes {
				fmt.Fprintln(cmd.OutOrStdout(), change)
			}
			return nil
		}
		bases.AddCommand(command)
	}
	init := &cobra.Command{Use: "init", Short: "Create an empty catalog (allows no requests until offerings are added)", Args: cobra.NoArgs}
	validityFlag(init)
	mutation(init, nil)
	renew := &cobra.Command{Use: "renew", Short: "Renew catalog validity without changing offerings", Args: cobra.NoArgs}
	validityFlag(renew)
	mutation(renew, nil)
	set := &cobra.Command{Use: "set --file PATH", Short: "Import a complete catalog object", Args: cobra.NoArgs}
	set.Flags().String("file", "", "Complete catalog JSON file, or - for stdin")
	mutation(set, nil)
	add := &cobra.Command{Use: "add BASE", Short: "Declare one simultaneous configuration", Args: cobra.ExactArgs(1)}
	offerFlag(add)
	rangeFlags(add)
	add.Flags().StringArray("capability", nil, "Allowed capability (repeatable; omission means none)")
	mutation(add, func(cmd *cobra.Command, args []string, c *execution.BaseCatalog) error {
		if err := execution.ValidateNamedBase(args[0]); err != nil {
			return err
		}
		name, _ := cmd.Flags().GetString("offer")
		for _, o := range c.Offerings {
			if o.Base == args[0] && o.Name == name {
				return errors.New("offering already exists")
			}
		}
		caps, _ := cmd.Flags().GetStringArray("capability")
		if caps == nil {
			caps = []string{}
		}
		caps = uniqueStrings(caps)
		resources, err := editResourceRules(cmd, nil)
		if err != nil {
			return err
		}
		c.Offerings = append(c.Offerings, execution.BaseOffering{Base: args[0], Name: name, Capabilities: caps, Resources: resources})
		return nil
	})
	remove := &cobra.Command{Use: "remove BASE", Short: "Remove a base group or a selected offering", Args: cobra.ExactArgs(1)}
	offerFlag(remove)
	mutation(remove, func(cmd *cobra.Command, args []string, c *execution.BaseCatalog) error {
		if err := execution.ValidateNamedBase(args[0]); err != nil {
			return err
		}
		name, _ := cmd.Flags().GetString("offer")
		keep := []execution.BaseOffering{}
		found := false
		for _, o := range c.Offerings {
			if o.Base == args[0] && (!cmd.Flags().Changed("offer") || o.Name == name) {
				found = true
			} else {
				keep = append(keep, o)
			}
		}
		if !found {
			return errors.New("base or offering not found")
		}
		c.Offerings = keep
		return nil
	})
	capability := &cobra.Command{Use: "capability", Short: "Edit the allowed capability set"}
	for _, op := range []string{"add", "remove", "clear"} {
		n := 2
		use := op + " BASE CAPABILITY"
		if op == "clear" {
			n = 1
			use = op + " BASE"
		}
		command := &cobra.Command{Use: use, Short: op + " allowed capabilities", Args: cobra.ExactArgs(n)}
		offerFlag(command)
		mutation(command, func(cmd *cobra.Command, args []string, c *execution.BaseCatalog) error {
			o, err := selectedOffering(cmd, args, c)
			if err != nil {
				return err
			}
			if op == "clear" {
				o.Capabilities = []string{}
				return nil
			}
			if _, err := (execution.Requirements{Capabilities: []string{args[1]}}).Normalize(); err != nil {
				return err
			}
			if op == "add" {
				o.Capabilities = uniqueStrings(append(o.Capabilities, args[1]))
			} else {
				keep := []string{}
				for _, cap := range o.Capabilities {
					if cap != args[1] {
						keep = append(keep, cap)
					}
				}
				o.Capabilities = keep
			}
			return nil
		})
		bases.RemoveCommand(command)
		capability.AddCommand(command)
	}
	bases.AddCommand(capability)
	ranges := &cobra.Command{Use: "ranges", Short: "Edit CPU and memory request rules"}
	command := &cobra.Command{Use: "set BASE", Short: "Patch individual bounds or explicit resource states", Args: cobra.ExactArgs(1)}
	offerFlag(command)
	rangeFlags(command)
	mutation(command, func(cmd *cobra.Command, args []string, c *execution.BaseCatalog) error {
		o, err := selectedOffering(cmd, args, c)
		if err != nil {
			return err
		}
		r, err := editResourceRules(cmd, o.Resources)
		if err == nil {
			o.Resources = r
		}
		return err
	})
	bases.RemoveCommand(command)
	ranges.AddCommand(command)
	bases.AddCommand(ranges)
	c.AddCommand(bases)
	return c
}
func inBaseOperation() bool {
	if os.Getenv(gitprofile.SocketEnv) != "" {
		return true
	}
	for _, entry := range os.Environ() {
		name, value, _ := strings.Cut(entry, "=")
		if value != "" && (strings.HasPrefix(name, "C2J_CURRENT_") || strings.HasPrefix(name, "C2J_CHILD_JOB_")) {
			return true
		}
	}
	return false
}
func renderBaseView(cmd *cobra.Command, v executioncatalog.View) {
	w := cmd.OutOrStdout()
	fmt.Fprintf(w, "Offering: %s   Scope: %s / %s\nRevision: %s\n", v.Status, v.Scope.Runtime, v.Scope.Tenant, v.Revision)
	if v.Catalog != nil {
		fmt.Fprintf(w, "Generation: %s   Published: %s   Expires: %s\nDeclared support; current availability is not reported.\n", v.Catalog.Generation, v.Catalog.PublishedAt.Format(time.RFC3339), v.Catalog.ExpiresAt.Format(time.RFC3339))
		table := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
		fmt.Fprintln(table, "BASE\tOFFER\tALLOWED CAPABILITIES\tCPU REQUEST\tMEMORY REQUEST")
		for _, o := range v.Catalog.Offerings {
			caps := strings.Join(o.Capabilities, ", ")
			if caps == "" {
				caps = "none"
			}
			fmt.Fprintf(table, "%s\t%s\t%s\t%s\t%s\n", o.Base, o.Name, caps, renderResourceRule(o.Resources.CPU), renderResourceRule(o.Resources.Memory))
		}
		_ = table.Flush()
		fmt.Fprintln(w, "Unlisted bases/capabilities and out-of-range requests: not allowed.\nResource columns describe explicit requests; omission uses provider defaults.")
		if v.Filtered {
			fmt.Fprintln(w, "Filtered view; incomplete and unsuitable for catalog import.")
		}
	}
	if v.Reason != "" {
		fmt.Fprintln(w, "Reason:", v.Reason)
	}
}
func renderResourceRule(r *execution.ResourceRule) string {
	if r.State == "not_allowed" {
		return "not allowed"
	}
	if r.State == "unbounded" {
		return "unbounded"
	}
	lo, hi := "unbounded", "unbounded"
	if r.Min != nil {
		lo = *r.Min
	}
	if r.Max != nil {
		hi = *r.Max
	}
	return lo + "–" + hi
}
