// Package executioncatalog adapts typed execution offerings to fresh JobDB reads.
package executioncatalog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/colony-2/c2j/pkg/execution"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/colony-2/jobdb/pkg/jobdb/clientpayload"
)

const RequireEnv = "C2J_REQUIRE_EXECUTION_BASE_CATALOG"
const Unsupported = "UNSUPPORTED_EXECUTION_BASE"
const UnsupportedRequirements = "UNSUPPORTED_EXECUTION_REQUIREMENTS"
const Unavailable = "EXECUTION_BASE_CATALOG_UNAVAILABLE"

type Reader func(context.Context) (jobdb.ConfigurationSnapshot, error)
type Policy struct {
	Required bool
	Scope    execution.CatalogScope
	Read     Reader
	Now      func() time.Time
}
type View struct {
	Status   string                 `json:"status"`
	Reason   string                 `json:"reason,omitempty"`
	Scope    execution.CatalogScope `json:"scope"`
	Revision string                 `json:"revision,omitempty"`
	Catalog  *execution.BaseCatalog `json:"catalog,omitempty"`
	Filtered bool                   `json:"filtered,omitempty"`
}

func RequiredFromEnv() (bool, error) {
	raw := os.Getenv(RequireEnv)
	if raw == "" {
		return false, nil
	}
	v, err := strconv.ParseBool(raw)
	if err != nil {
		return false, fmt.Errorf("%s must be a boolean", RequireEnv)
	}
	return v, nil
}
func (p Policy) Discover(ctx context.Context) (View, error) { return p.discover(ctx, false) }

// DiscoverForAdministration exposes expired structural records for repair only.
func (p Policy) DiscoverForAdministration(ctx context.Context) (View, error) {
	return p.discover(ctx, true)
}
func (p Policy) discover(ctx context.Context, allowExpired bool) (View, error) {
	v := View{Scope: p.Scope, Status: "unavailable"}
	if err := ctx.Err(); err != nil {
		return v, err
	}
	if p.Read == nil {
		v.Reason = "fresh catalog reader unavailable"
		return v, catalogUnavailable(v)
	}
	call, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	snapshot, err := p.Read(call)
	v.Revision = snapshot.Revision
	if ctx.Err() != nil {
		return v, ctx.Err()
	}
	if errors.Is(err, jobdb.ErrExecutionLeaseLost) || errors.Is(err, jobdb.ErrJobCancelled) {
		return v, err
	}
	if err != nil || call.Err() != nil {
		v.Reason = "fresh configuration read failed"
		return v, catalogUnavailable(v)
	}
	raw := snapshot.Values[execution.BaseCatalogKey]
	if len(raw) == 0 && !p.Required {
		v.Status = "not_configured"
		return v, nil
	}
	now := time.Now().UTC()
	if p.Now != nil {
		now = p.Now()
	}
	c, err := execution.DecodeBaseCatalog(raw, p.Scope, now)
	if err != nil {
		v.Reason = err.Error()
		if allowExpired {
			if c, e := execution.DecodeBaseCatalogStructure(raw, p.Scope); e == nil && !c.ExpiresAt.After(now) {
				v.Status = "expired"
				v.Catalog = &c
			}
		}
		return v, catalogUnavailable(v)
	}
	v.Status, v.Catalog = "authoritative", &c
	return v, nil
}
func catalogUnavailable(v View) error {
	return jobdb.NewSystemError(jobdb.SystemErrorPayload{Code: Unavailable, Component: "execution_base_catalog", Retryable: true, Message: fmt.Sprintf("%s: %s (scope %s/%s, revision %s)", Unavailable, v.Reason, v.Scope.Runtime, v.Scope.Tenant, v.Revision)})
}
func (p Policy) Check(ctx context.Context, needs execution.Requirements, source string) error {
	// Unsupported selectors must fail even when catalog admission is disabled.
	if needs.Base != nil {
		if err := execution.ValidateCatalogSelector(*needs.Base); err != nil {
			return &jobdb.AppError{Payload: jobdb.AppErrorPayload{Message: err.Error(), Attrs: map[string]any{"code": Unsupported, "source": source, "selector": *needs.Base}}}
		}
	}
	if !p.Required {
		return nil
	}
	needs, err := needs.Normalize()
	if err != nil {
		return err
	}
	v, err := p.Discover(ctx)
	if err != nil {
		return err
	}
	result := v.Catalog.Evaluate(needs)
	if result.Decision == "allowed" {
		return nil
	}
	selector := ""
	if needs.Base != nil {
		selector = *needs.Base
	}
	code := UnsupportedRequirements
	for _, r := range result.Reasons {
		if r.Field == "base" {
			code = Unsupported
		}
	}
	attrs := map[string]any{"code": code, "selector": selector, "source": source, "named_bases": v.Catalog.Names(), "reasons": result.Reasons, "scope": v.Scope, "revision": v.Revision, "generation": v.Catalog.Generation}
	reasons, _ := json.Marshal(result.Reasons)
	return &jobdb.AppError{Payload: jobdb.AppErrorPayload{Message: fmt.Sprintf("%s: %q at %s: %s (scope %s/%s revision %s generation %s). Inspect c2j execution bases check --json", code, selector, source, reasons, v.Scope.Runtime, v.Scope.Tenant, v.Revision, v.Catalog.Generation), Attrs: attrs}}
}

type policyKey struct{}

// WithPolicy attaches driver-owned authority, which recipe inputs cannot replace.
func WithPolicy(ctx context.Context, p Policy) context.Context {
	return context.WithValue(ctx, policyKey{}, p)
}
func FromContext(ctx context.Context) Policy { p, _ := ctx.Value(policyKey{}).(Policy); return p }
func CheckSubmission(ctx context.Context, job jobdb.SubmitJob) error {
	p := FromContext(ctx)
	if p.Required && job.TenantId != p.Scope.Tenant {
		return jobdb.ErrLeasePermissionDenied
	}
	payload, _, err := clientpayload.Initial(job.ClientPayloadUpdate)
	if err != nil {
		return err
	}
	v := execution.Inspect(job.Metadata, payload)
	if v.Diagnostic != "" {
		return fmt.Errorf("initial execution requirements: %s", v.Diagnostic)
	}
	needs := execution.Requirements{}
	if v.Initial != nil {
		needs = v.Initial.Effective
	}
	if v.Demand != nil {
		needs = v.Demand.Effective
	}
	return p.Check(ctx, needs, "submission.execution.base")
}
func CheckRestart(ctx context.Context, req jobdb.SubmitRestartJobRequest) error {
	p := FromContext(ctx)
	if p.Required && req.Job.PriorJobKey.TenantId != p.Scope.Tenant {
		return jobdb.ErrLeasePermissionDenied
	}
	if req.Job.ClientPayloadUpdate == nil {
		if !p.Required {
			return nil
		}
		return jobdb.NewSystemError(jobdb.SystemErrorPayload{Code: Unavailable, Message: "restart initial execution demand must be reconstructed before publication"})
	}
	raw, _, err := clientpayload.Initial(req.Job.ClientPayloadUpdate)
	if err != nil {
		return err
	}
	d, err := execution.PayloadDemand(raw)
	if err != nil {
		return err
	}
	needs := execution.Requirements{}
	if d != nil {
		needs = d.Effective
	}
	return p.Check(ctx, needs, "restart.initial.execution.base")
}
