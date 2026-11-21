package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/colony-2/c2j/cmd/c2j/internal/defaults"
	"github.com/colony-2/c2j/cmd/c2j/internal/swfruntime"
	"github.com/colony-2/c2j/pkg/execution"
	"github.com/colony-2/c2j/pkg/executioncatalog"
	"github.com/colony-2/c2j/pkg/gitprofile"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/colony-2/jobdb/pkg/jobdb/clientpayload"
	"github.com/spf13/cobra"
)

func newExecutionCmd() *cobra.Command {
	c := &cobra.Command{Use: "execution", Short: "Inspect execution support"}
	var uri string
	var embed, jsonOutput bool
	bases := &cobra.Command{Use: "bases", Short: "List offered bases (support, not free capacity)", Args: cobra.NoArgs}
	bases.PersistentFlags().StringVar(&uri, "jobdb", "", "JobDB URI")
	bases.PersistentFlags().BoolVar(&embed, "embed", false, "Use embedded JobDB")
	bases.Flags().BoolVar(&jsonOutput, "json", false, "Emit typed JSON")
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
			return nil, executioncatalog.Policy{}, err
		}
		p, err := defaults.ExecutionBasePolicy(target.RuntimeURL, target.TenantID, h.Configuration)
		if err != nil {
			h.Cleanup()
			return nil, p, err
		}
		return h, p, nil
	}
	bases.RunE = func(cmd *cobra.Command, args []string) error {
		var view executioncatalog.View
		var err error
		if socket := os.Getenv(gitprofile.SocketEnv); socket != "" {
			var res gitprofile.Response
			res, err = gitprofile.Call(cmd.Context(), socket, gitprofile.Request{Operation: "execution_bases"})
			if len(res.Value) > 0 {
				if decodeErr := json.Unmarshal(res.Value, &view); decodeErr != nil {
					return decodeErr
				}
			}
		} else {
			var h *swfruntime.Handle
			var p executioncatalog.Policy
			h, p, err = open(cmd)
			if err != nil {
				return err
			}
			defer h.Cleanup()
			view, err = p.Discover(cmd.Context())
		}
		if jsonOutput {
			if e := json.NewEncoder(cmd.OutOrStdout()).Encode(view); e != nil {
				return e
			}
		} else {
			fmt.Fprintf(cmd.OutOrStdout(), "Base offering: %s (support, not capacity)\nScope: %s / %s\nRevision: %s\n", view.Status, view.Scope.Runtime, view.Scope.Tenant, view.Revision)
			if view.Catalog != nil {
				fmt.Fprintf(cmd.OutOrStdout(), "Offered named bases: %v\nDynamic support: %v\nGeneration: %s\nPublished: %s\nExpires: %s\n", view.Catalog.NamedBases, view.Catalog.Dynamic, view.Catalog.Generation, view.Catalog.PublishedAt.Format(time.RFC3339), view.Catalog.ExpiresAt.Format(time.RFC3339))
			}
			if view.Reason != "" {
				fmt.Fprintln(cmd.OutOrStdout(), "Reason:", view.Reason)
			}
		}
		return err
	}
	for _, op := range []string{"get", "set"} {
		var file string
		command := &cobra.Command{Use: op, Short: op + " the structured offering (provisioning context)", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
			if inBaseOperation() {
				return errors.New("base catalog management requires a provisioning context")
			}
			var raw []byte
			if op == "set" {
				if file == "" {
					return errors.New("--file is required")
				}
				var err error
				raw, err = readProfileFile(file, cmd.InOrStdin(), execution.BaseCatalogMaxBytes)
				if err != nil {
					return err
				}
			}
			h, p, err := open(cmd)
			if err != nil {
				return err
			}
			defer h.Cleanup()
			if op == "get" {
				p.Required = true
				v, err := p.Discover(cmd.Context())
				if err != nil {
					return err
				}
				return json.NewEncoder(cmd.OutOrStdout()).Encode(v.Catalog)
			}
			revision, err := publishBaseCatalog(cmd.Context(), h.Configuration, p.Scope, raw, time.Now().UTC())
			if err != nil {
				return err
			}
			return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]string{"revision": revision})
		}}
		if op == "set" {
			command.Flags().StringVar(&file, "file", "", "Complete catalog JSON file, or - for stdin")
		}
		bases.AddCommand(command)
	}
	c.AddCommand(bases)
	return c
}

func sameCatalog(a, b []byte) bool {
	da, ea := clientpayload.DigestWithLimit(a, execution.BaseCatalogMaxBytes)
	db, eb := clientpayload.DigestWithLimit(b, execution.BaseCatalogMaxBytes)
	return ea == nil && eb == nil && da == db
}
func publishBaseCatalog(ctx context.Context, admin jobdb.ConfigurationAdmin, scope execution.CatalogScope, raw []byte, now time.Time) (string, error) {
	catalog, err := execution.DecodeBaseCatalog(raw, scope, now)
	if err != nil {
		return "", err
	}
	encoded, err := json.Marshal(catalog)
	if err != nil {
		return "", err
	}
	if len(encoded) > execution.BaseCatalogMaxBytes {
		return "", jobdb.ErrConfigurationTooLarge
	}
	call, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	snapshot, err := admin.GetTenantConfiguration(call, scope.Tenant)
	if err != nil {
		return "", errors.New("configuration administration read failed")
	}
	old := snapshot.Values[execution.BaseCatalogKey]
	if sameCatalog(old, encoded) {
		return snapshot.Revision, nil
	}
	var previous execution.BaseCatalog
	if json.Unmarshal(old, &previous) == nil && previous.Generation == catalog.Generation {
		return "", errors.New("changed catalog requires a new generation")
	}
	revision, err := admin.SetTenantConfigurationValue(call, jobdb.SetTenantConfigurationValueRequest{TenantID: scope.Tenant, Name: execution.BaseCatalogKey, Value: encoded, ExpectedRevision: &snapshot.Revision})
	if err == nil {
		return revision, nil
	}
	if errors.Is(err, jobdb.ErrConfigurationRevisionMismatch) {
		return "", fmt.Errorf("catalog publication conflict; reread before publishing: %w", err)
	}
	// A lost response never licenses a second write. Resolve only exact content.
	check, stop := context.WithTimeout(ctx, 10*time.Second)
	defer stop()
	after, readErr := admin.GetTenantConfiguration(check, scope.Tenant)
	if readErr == nil && sameCatalog(after.Values[execution.BaseCatalogKey], encoded) {
		return after.Revision, nil
	}
	return "", errors.New("catalog publication uncertain; read back the exact generation and content before retrying")
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
