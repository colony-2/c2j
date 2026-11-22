package cmd

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/colony-2/c2j/pkg/execution"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/colony-2/jobdb/pkg/jobdb/clientpayload"
	"github.com/spf13/cobra"
)

type catalogEdit func(*cobra.Command, []string, *execution.BaseCatalog) error
type catalogReceipt struct {
	Changed    bool                   `json:"changed"`
	Scope      execution.CatalogScope `json:"scope"`
	Revision   string                 `json:"revision"`
	Generation string                 `json:"generation"`
	ExpiresAt  time.Time              `json:"expires_at"`
	Changes    []string               `json:"changes"`
}

func validityFlag(c *cobra.Command) {
	c.Flags().Duration("valid-for", 0, "Required positive validity duration, e.g. 24h")
}
func offerFlag(c *cobra.Command) { c.Flags().String("offer", "default", "Offering name") }
func rangeFlags(c *cobra.Command) {
	for _, resource := range []string{"cpu", "memory"} {
		c.Flags().String(resource+"-min", "", "Inclusive minimum (none clears a bound)")
		c.Flags().String(resource+"-max", "", "Inclusive maximum (none clears a bound)")
		c.Flags().Bool(resource+"-unbounded", false, "Allow any positive explicit request")
		c.Flags().Bool(resource+"-not-allowed", false, "Disallow explicit resource requests")
	}
}
func uniqueStrings(values []string) []string {
	out := slices.Clone(values)
	slices.Sort(out)
	return slices.Compact(out)
}
func selectedOffering(cmd *cobra.Command, args []string, c *execution.BaseCatalog) (*execution.BaseOffering, error) {
	if err := execution.ValidateNamedBase(args[0]); err != nil {
		return nil, err
	}
	name, _ := cmd.Flags().GetString("offer")
	for i := range c.Offerings {
		if c.Offerings[i].Base == args[0] && c.Offerings[i].Name == name {
			return &c.Offerings[i], nil
		}
	}
	return nil, errors.New("offering not found")
}
func editResourceRules(cmd *cobra.Command, old *execution.OfferingResources) (*execution.OfferingResources, error) {
	out := &execution.OfferingResources{}
	changes := 0
	for _, resource := range []string{"cpu", "memory"} {
		var r execution.ResourceRule
		if old != nil {
			if resource == "cpu" {
				r = *old.CPU
			} else {
				r = *old.Memory
			}
		}
		minSet, maxSet := cmd.Flags().Changed(resource+"-min"), cmd.Flags().Changed(resource+"-max")
		unbounded, denied := cmd.Flags().Changed(resource+"-unbounded"), cmd.Flags().Changed(resource+"-not-allowed")
		if unbounded && denied || (unbounded || denied) && (minSet || maxSet) {
			return nil, fmt.Errorf("conflicting %s state/bound options", resource)
		}
		if unbounded || denied {
			flag := resource + "-unbounded"
			state := "unbounded"
			if denied {
				flag = resource + "-not-allowed"
				state = "not_allowed"
			}
			yes, _ := cmd.Flags().GetBool(flag)
			if !yes {
				return nil, fmt.Errorf("%s must be true when supplied", flag)
			}
			r = execution.ResourceRule{State: state}
			changes++
		} else if minSet || maxSet {
			if r.State != "range" {
				r = execution.ResourceRule{State: "range"}
			}
			for _, bound := range []string{"min", "max"} {
				if cmd.Flags().Changed(resource + "-" + bound) {
					value, _ := cmd.Flags().GetString(resource + "-" + bound)
					var p *string
					if value != "none" {
						p = &value
					}
					if bound == "min" {
						r.Min = p
					} else {
						r.Max = p
					}
				}
			}
			if r.Min == nil && r.Max == nil {
				r.State = "unbounded"
			}
			changes++
		} else if old == nil {
			return nil, fmt.Errorf("declare %s bounds, --%s-unbounded or --%s-not-allowed", resource, resource, resource)
		}
		if resource == "cpu" {
			out.CPU = &r
		} else {
			out.Memory = &r
		}
	}
	if changes == 0 {
		return nil, errors.New("at least one resource change option is required")
	}
	return out, nil
}
func editBaseCatalog(ctx context.Context, admin jobdb.ConfigurationAdmin, scope execution.CatalogScope, cmd *cobra.Command, args []string, raw []byte, apply catalogEdit, now time.Time) (catalogReceipt, error) {
	receipt := catalogReceipt{Scope: scope, Changes: []string{}}
	call, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	snapshot, err := admin.GetTenantConfiguration(call, scope.Tenant)
	if err != nil {
		return receipt, errors.New("configuration administration read failed")
	}
	old := snapshot.Values[execution.BaseCatalogKey]
	var candidate execution.BaseCatalog
	switch {
	case apply == nil && cmd.Name() == "init":
		if len(old) > 0 {
			return receipt, errors.New("catalog already exists")
		}
		candidate = execution.BaseCatalog{Version: 1, Scope: scope, Complete: true, Offerings: []execution.BaseOffering{}}
	case apply == nil && cmd.Name() == "set":
		candidate, err = execution.DecodeBaseCatalog(raw, scope, now)
		if err != nil {
			return receipt, err
		}
	default:
		candidate, err = execution.DecodeBaseCatalogStructure(old, scope)
		if err != nil {
			return receipt, err
		}
		if cmd.Name() != "renew" {
			if err = candidate.Validate(scope, now); err != nil {
				return receipt, err
			}
		}
	}
	if cmd.Name() == "init" || cmd.Name() == "renew" {
		validity, _ := cmd.Flags().GetDuration("valid-for")
		if validity <= 0 {
			return receipt, errors.New("--valid-for requires a positive duration")
		}
		candidate.ExpiresAt = now.Add(validity)
	}
	if apply != nil {
		if err = apply(cmd, args, &candidate); err != nil {
			return receipt, err
		}
	}
	previous, e := execution.DecodeBaseCatalogStructure(old, scope)
	candidate.Generation = rand.Text()
	candidate.PublishedAt = now
	candidate, err = candidate.Canonicalize(scope)
	if err != nil {
		return receipt, err
	}
	if err = candidate.Validate(scope, now); err != nil {
		return receipt, err
	}
	if e == nil && sameCatalogSemantics(previous, candidate) {
		receipt.Revision = snapshot.Revision
		receipt.Generation = previous.Generation
		receipt.ExpiresAt = previous.ExpiresAt
		return receipt, nil
	}
	encoded, err := json.Marshal(candidate)
	if err != nil {
		return receipt, err
	}
	revision, err := publishBaseCatalogSnapshot(ctx, admin, scope, encoded, snapshot.Revision)
	if err != nil {
		return receipt, err
	}
	receipt.Changed = true
	receipt.Revision = revision
	receipt.Generation = candidate.Generation
	receipt.ExpiresAt = candidate.ExpiresAt
	receipt.Changes = []string{cmd.CommandPath()}
	if cmd.Name() == "init" {
		receipt.Changes = append(receipt.Changes, "Empty catalog: no requests allowed until offerings are added.")
	}
	return receipt, nil
}
func sameCatalogSemantics(a, b execution.BaseCatalog) bool {
	a.Generation = ""
	b.Generation = ""
	a.PublishedAt = time.Time{}
	b.PublishedAt = time.Time{}
	ar, _ := json.Marshal(a)
	br, _ := json.Marshal(b)
	return sameCatalog(ar, br)
}
func sameCatalog(a, b []byte) bool {
	da, ea := clientpayload.DigestWithLimit(a, execution.BaseCatalogMaxBytes)
	db, eb := clientpayload.DigestWithLimit(b, execution.BaseCatalogMaxBytes)
	return ea == nil && eb == nil && da == db
}

var errCatalogConflict = jobdb.ErrConfigurationRevisionMismatch

type catalogPublicationUncertain struct{ Candidate []byte }

func (e *catalogPublicationUncertain) Error() string {
	return "catalog publication uncertain; compare the retained exact candidate before retrying"
}

// The revision is the snapshot used to construct the candidate. Never refresh it
// before writing: that would permit a stale candidate to overwrite a winner.
func publishBaseCatalogSnapshot(ctx context.Context, admin jobdb.ConfigurationAdmin, scope execution.CatalogScope, encoded []byte, revision string) (string, error) {
	if len(encoded) > execution.BaseCatalogMaxBytes {
		return "", jobdb.ErrConfigurationTooLarge
	}
	call, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	next, err := admin.SetTenantConfigurationValue(call, jobdb.SetTenantConfigurationValueRequest{TenantID: scope.Tenant, Name: execution.BaseCatalogKey, Value: encoded, ExpectedRevision: &revision})
	if err == nil {
		return next, nil
	}
	if errors.Is(err, jobdb.ErrConfigurationRevisionMismatch) {
		return "", fmt.Errorf("catalog publication conflict; reread before publishing: %w", err)
	}
	check, stop := context.WithTimeout(ctx, 10*time.Second)
	defer stop()
	after, readErr := admin.GetTenantConfiguration(check, scope.Tenant)
	if readErr == nil && sameCatalog(after.Values[execution.BaseCatalogKey], encoded) {
		return after.Revision, nil
	}
	return "", &catalogPublicationUncertain{Candidate: slices.Clone(encoded)}
}
