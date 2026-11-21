// Package executioncatalog adapts typed execution offerings to fresh JobDB reads.
package executioncatalog

import (
	"context"
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
const CannotVerify = "CANNOT_VERIFY_EXECUTION_BASE"
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
func (p Policy) Discover(ctx context.Context) (View, error) {
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
		return v, catalogUnavailable(v)
	}
	v.Status, v.Catalog = "authoritative", &c
	return v, nil
}
func catalogUnavailable(v View) error {
	return jobdb.NewSystemError(jobdb.SystemErrorPayload{Code: Unavailable, Component: "execution_base_catalog", Retryable: true, Message: fmt.Sprintf("%s: %s (scope %s/%s, revision %s)", Unavailable, v.Reason, v.Scope.Runtime, v.Scope.Tenant, v.Revision)})
}
func (p Policy) Check(ctx context.Context, needs execution.Requirements, source string) error {
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
	selector := ""
	if needs.Base != nil {
		selector = *needs.Base
	}
	switch v.Catalog.AdmitBase(selector) {
	case execution.BaseAllowed:
		return nil
	case execution.BaseCannotVerify:
		return jobdb.NewSystemError(jobdb.SystemErrorPayload{Code: CannotVerify, Component: "execution_base_catalog", Message: fmt.Sprintf("%s: selector %q at %s intersects dynamic OCI support; choose a concrete base; scope %s/%s revision %s generation %s", CannotVerify, selector, source, p.Scope.Runtime, p.Scope.Tenant, v.Revision, v.Catalog.Generation)})
	default:
		attrs := map[string]any{"code": Unsupported, "selector": selector, "source": source, "named_bases": v.Catalog.NamedBases, "dynamic": v.Catalog.Dynamic, "scope": v.Scope, "revision": v.Revision, "generation": v.Catalog.Generation}
		return &jobdb.AppError{Payload: jobdb.AppErrorPayload{Message: fmt.Sprintf("%s: %q at %s; offered named bases: %v; dynamic support: %v. Correct the request; discover choices with c2j execution bases --json", Unsupported, selector, source, v.Catalog.NamedBases, v.Catalog.Dynamic), Attrs: attrs}}
	}
}

type policyKey struct{}

// WithPolicy attaches driver-owned authority, which recipe inputs cannot replace.
func WithPolicy(ctx context.Context, p Policy) context.Context {
	return context.WithValue(ctx, policyKey{}, p)
}
func FromContext(ctx context.Context) Policy { p, _ := ctx.Value(policyKey{}).(Policy); return p }
func CheckSubmission(ctx context.Context, job jobdb.SubmitJob) error {
	p := FromContext(ctx)
	if !p.Required {
		return nil
	}
	if job.TenantId != p.Scope.Tenant {
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
	if !p.Required {
		return nil
	}
	if req.Job.PriorJobKey.TenantId != p.Scope.Tenant {
		return jobdb.ErrLeasePermissionDenied
	}
	if req.Job.ClientPayloadUpdate == nil {
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
