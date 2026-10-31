package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/colony-2/c2j/cmd/c2j/internal/defaults"
	"github.com/colony-2/c2j/cmd/c2j/internal/swfruntime"
	"github.com/colony-2/c2j/pkg/cellref"
	"github.com/colony-2/c2j/pkg/config"
	"github.com/colony-2/c2j/pkg/gitprofile"
	"github.com/colony-2/c2j/pkg/hookpolicy"
	"github.com/colony-2/jobdb/pkg/jobdb"
	jobconfiguration "github.com/colony-2/jobdb/pkg/jobdb/configuration"
	"github.com/spf13/cobra"
)

func newHooksCmd() *cobra.Command {
	root := &cobra.Command{Use: "hooks", Short: "Manage repository-local recipe hooks"}
	group := &cobra.Command{Use: "perms", Short: "Administer per-hook permissions in JobDB configuration"}
	root.AddCommand(group)
	var uri string
	var embed bool
	var jsonOutput bool
	group.PersistentFlags().StringVar(&uri, "jobdb", "", "JobDB URI selecting the tenant")
	group.PersistentFlags().BoolVar(&embed, "embed", false, "Use embedded JobDB")
	group.PersistentFlags().BoolVar(&jsonOutput, "json", false, "Emit hook permissions as JSON")
	for _, operation := range []string{"add", "remove", "clear", "inherit", "set", "get", "list"} {
		var cell, commit, policy string
		var tenant, all, submit, candidate, replace bool
		var names []string
		args := cobra.ExactArgs(1)
		use := operation + " HOOK"
		if operation == "list" {
			args = cobra.NoArgs
			use = operation
		}
		c := &cobra.Command{Use: use, Args: args, RunE: func(cmd *cobra.Command, args []string) error {
			if os.Getenv(gitprofile.SocketEnv) != "" {
				return fmt.Errorf("hook permission management requires a provisioning context")
			}
			if all && (operation != "list" || tenant || cell != "") {
				return fmt.Errorf("--all-cells is exclusive and only supported by list")
			}
			if !all && (tenant == (cell != "")) {
				return fmt.Errorf("select exactly one of --tenant-wide or --cell")
			}
			if operation == "inherit" && tenant {
				return fmt.Errorf("inherit requires --cell")
			}
			hook := ""
			var err error
			if len(args) > 0 {
				hook, err = hookpolicy.Path(args[0])
				if err != nil {
					return err
				}
			}
			trust := hookpolicy.Trust{Mode: "candidate"}
			if operation == "add" {
				if candidate == (commit != "") {
					return fmt.Errorf("select exactly one of --trust-candidate-code or --candidate-commit")
				}
				if commit != "" {
					trust = hookpolicy.Trust{Mode: "commit", Commit: commit}
				}
			}
			var entry *hookpolicy.Entry
			if operation == "set" {
				if policy == "" {
					return fmt.Errorf("--policy-json is required")
				}
				raw := []byte(policy)
				if policy == "-" {
					raw, err = io.ReadAll(io.LimitReader(cmd.InOrStdin(), 65537))
					if err != nil {
						return err
					}
				}
				if len(raw) > 65536 {
					return fmt.Errorf("policy entry exceeds size limit")
				}
				entry = &hookpolicy.Entry{}
				if err = hookpolicy.Decode(raw, entry); err != nil {
					return err
				}
				if err = entry.Validate(); err != nil {
					return err
				}
			}
			repository := ""
			if cell != "" {
				repository, err = hookCellIdentity(cmd.Context(), cell)
				if err != nil {
					return err
				}
			}
			targetURI := uri
			if embed {
				targetURI = defaults.EmbedURL
			}
			target, err := defaults.ResolveJobDBTarget(cmd.Context(), "", targetURI)
			if err != nil {
				return err
			}
			if target.RuntimeURL == "" {
				return fmt.Errorf("--jobdb or configured JobDB endpoint is required")
			}
			h, err := swfruntime.Open(cmd.Context(), target.RuntimeURL)
			if err != nil {
				return err
			}
			defer h.Cleanup()
			ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
			defer cancel()
			snapshot, err := h.Configuration.GetTenantConfiguration(ctx, target.TenantID)
			if err != nil {
				return fmt.Errorf("configuration administration read failed: %w", err)
			}
			if all {
				out := map[string]hookpolicy.Document{}
				for k, raw := range snapshot.Values {
					if k != hookpolicy.TenantKey && !strings.HasPrefix(k, hookpolicy.CellPrefix) {
						continue
					}
					var d hookpolicy.Document
					if err = hookpolicy.Decode(raw, &d); err != nil {
						return err
					}
					if err = d.Validate(d.Repository); err != nil {
						return err
					}
					if (k == hookpolicy.TenantKey && d.Repository != "") || (k != hookpolicy.TenantKey && (d.Repository == "" || k != hookpolicy.CellKey(d.Repository))) {
						return fmt.Errorf("invalid repository binding in %s", k)
					}
					out[k] = d
				}
				return writeAllHookPolicies(cmd.OutOrStdout(), target.TenantID, snapshot.Revision, out, jsonOutput)
			}
			s, err := hookpolicy.Capture(snapshot, repository)
			if err != nil {
				return err
			}
			key := hookpolicy.TenantKey
			if cell != "" {
				key = hookpolicy.CellKey(repository)
			}
			if operation != "get" && operation != "list" {
				d, err := hookpolicy.Edit(s, cell != "", hook, operation, names, submit, trust, replace, entry)
				if err != nil {
					return err
				}
				raw, err := json.Marshal(d)
				if err != nil {
					return err
				}
				if err = jobconfiguration.ValidateValue(raw, jobdb.ConfigurationLimits{}); err != nil {
					return err
				}
				if operation == "inherit" && len(d.Hooks) == 0 {
					snapshot.Revision, err = h.Configuration.RemoveTenantConfigurationValue(ctx, jobdb.RemoveTenantConfigurationValueRequest{TenantID: target.TenantID, Name: key, ExpectedRevision: &snapshot.Revision})
				} else {
					snapshot.Revision, err = h.Configuration.SetTenantConfigurationValue(ctx, jobdb.SetTenantConfigurationValueRequest{TenantID: target.TenantID, Name: key, Value: raw, ExpectedRevision: &snapshot.Revision})
				}
				if err != nil {
					return fmt.Errorf("policy update failed; reread before retrying: %w", err)
				}
				if cell != "" {
					s.Cell = &d
				} else {
					s.Tenant = &d
				}
			}
			paths := []string{hook}
			if operation == "list" {
				seen := map[string]bool{}
				for _, d := range []*hookpolicy.Document{s.Tenant, s.Cell} {
					if d != nil {
						for p := range d.Hooks {
							seen[p] = true
						}
					}
				}
				paths = nil
				for p := range seen {
					paths = append(paths, p)
				}
				sort.Strings(paths)
			}
			entries := map[string]hookPolicyView{}
			for _, p := range paths {
				e, source := s.Select(p)
				entries[p] = hookPolicyView{Effective: e, Source: source}
			}
			scope := "tenant-defaults"
			if cell != "" {
				scope = "cell"
			}
			return writeHookPolicies(cmd.OutOrStdout(), operation, target.TenantID, scope, repository, snapshot.Revision, entries, jsonOutput)
		}}
		c.Flags().StringVar(&cell, "cell", "", "Configured cell name or explicit repository")
		c.Flags().BoolVar(&tenant, "tenant-wide", false, "Edit this hook's tenant default across cells")
		if operation == "list" {
			c.Flags().BoolVar(&all, "all-cells", false, "List tenant defaults and all cell overrides")
		}
		if operation == "add" || operation == "remove" {
			c.Flags().StringArrayVar(&names, "secret", nil, "Exact secret name (repeatable)")
			c.Flags().BoolVar(&submit, "submit-jobs", false, "Select job submission permission")
		}
		if operation == "add" {
			c.Flags().BoolVar(&candidate, "trust-candidate-code", false, "Trust candidate code with selected capabilities")
			c.Flags().StringVar(&commit, "candidate-commit", "", "Require this full candidate commit SHA")
			c.Flags().BoolVar(&replace, "replace", false, "Replace an existing grant's trust condition")
		}
		if operation == "set" {
			c.Flags().StringVar(&policy, "policy-json", "", "Complete hook entry JSON, or - for stdin")
		}
		group.AddCommand(c)
	}
	return root
}

func hookCellIdentity(ctx context.Context, cell string) (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	resolution := &cellref.Context{BaseDir: cwd}
	// Explicit repository references need no project configuration or commands.
	if !strings.ContainsAny(cell, "/:\\") {
		cfg, e := config.LoadProjectConfig(cwd)
		if e != nil && !errors.Is(e, config.ErrConfigNotFound) {
			return "", e
		}
		if e == nil {
			resolution, e = cfg.CellResolution(ctx)
			if e != nil {
				return "", e
			}
		}
	}
	target, err := resolution.Resolve(cell, "")
	if err != nil {
		return "", err
	}
	return hookpolicy.Identity(target.Repository)
}
