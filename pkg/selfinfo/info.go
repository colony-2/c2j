// Package selfinfo defines the public, noncredential execution discovery report.
package selfinfo

import (
	"context"
	"encoding/json"
	"net/url"
	"path"
	"regexp"
	"strings"
	"time"

	"github.com/colony-2/c2j/pkg/jobcontext"
	"github.com/colony-2/c2j/pkg/permissions"
	"github.com/colony-2/jobdb/pkg/jobdb"
)

const Version = 1
const MaxResponseBytes = 512 * 1024

type Info struct {
	SchemaVersion int                   `json:"schema_version"`
	ObservedAt    time.Time             `json:"observed_at"`
	Execution     Execution             `json:"execution"`
	Workspace     Workspace             `json:"workspace"`
	Lease         Lease                 `json:"lease"`
	Permissions   Permissions           `json:"permissions"`
	Capabilities  map[string]Capability `json:"capabilities"`
	Diagnostics   []Diagnostic          `json:"diagnostics"`
}
type Execution struct {
	Status             string             `json:"status"`
	Source             string             `json:"source"`
	ReasonCode         string             `json:"reason_code,omitempty"`
	TenantID           *string            `json:"tenant_id"`
	GoalID             *string            `json:"goal_id"`
	JobID              *string            `json:"job_id"`
	JobType            *string            `json:"job_type"`
	TaskID             *string            `json:"task_id"`
	TaskType           *string            `json:"task_type"`
	AttemptNumber      *int               `json:"attempt_number"`
	ResultOrdinal      *int64             `json:"result_ordinal"`
	NodeID             *string            `json:"node_id"`
	InvocationPath     *string            `json:"invocation_path"`
	InvocationSequence *int64             `json:"invocation_sequence"`
	InvocationHash     *string            `json:"invocation_hash"`
	OpType             *string            `json:"op_type"`
	OpStep             *string            `json:"op_step"`
	CellID             *string            `json:"cell_id"`
	CellRepo           *string            `json:"cell_repo"`
	CellName           *string            `json:"cell_name"`
	RecipeSource       *string            `json:"recipe_source"`
	RecipeRef          *string            `json:"recipe_ref"`
	Parent             *jobcontext.Parent `json:"parent"`
}
type Workspace struct {
	Status          string  `json:"status"`
	ScopeID         *string `json:"scope_id"`
	CellName        *string `json:"cell_name"`
	Repo            *string `json:"repo"`
	BaseRef         *string `json:"base_ref"`
	BaseRevision    *string `json:"base_revision"`
	ContentIdentity string  `json:"content_identity"`
	Paths           *Paths  `json:"paths"`
	ReasonCode      string  `json:"reason_code,omitempty"`
}
type Paths struct {
	Workdir  string `json:"workdir"`
	Worktree string `json:"worktree"`
	Inbox    string `json:"inbox"`
	Outbox   string `json:"outbox"`
}
type Lease struct {
	Status            string                `json:"status"`
	AuthorityMode     string                `json:"authority_mode"`
	LeaseID           *string               `json:"lease_id"`
	Route             *jobdb.Route          `json:"route"`
	Validation        string                `json:"validation"`
	ValidatedAt       *time.Time            `json:"validated_at"`
	ExpiresAt         *time.Time            `json:"expires_at"`
	OperationDeadline *time.Time            `json:"operation_deadline"`
	RenewalOwner      *string               `json:"renewal_owner"`
	Delegation        *jobdb.DelegationInfo `json:"delegation"`
	DelegationStatus  string                `json:"delegation_status,omitempty"`
	ReasonCode        string                `json:"reason_code,omitempty"`
}
type Permissions struct {
	ParentBounds *jobdb.DelegationInfo `json:"parent_bounds,omitempty"`
	Status       string                `json:"status"`
	Scope        string                `json:"scope"`
	Source       string                `json:"source,omitempty"`
	JobDB        *permissions.JobDB    `json:"jobdb"`
	ChildCeiling Ceiling               `json:"child_ceiling"`
	ReasonCode   string                `json:"reason_code,omitempty"`
}
type Ceiling struct {
	Status   string             `json:"status"`
	JobDB    *permissions.JobDB `json:"jobdb,omitempty"`
	JobTypes []string           `json:"job_types,omitempty"`
}
type Capability struct {
	SubmitScope    *jobdb.DelegationSubmitPermission `json:"submit_scope,omitempty"`
	Permission     string                            `json:"permission"`
	Availability   string                            `json:"availability"`
	Route          *string                           `json:"route"`
	Command        *string                           `json:"command"`
	ReasonCode     *string                           `json:"reason_code"`
	SubmissionKind string                            `json:"submission_kind,omitempty"`
	Names          []string                          `json:"names,omitempty"`
	Restrictions   []string                          `json:"restrictions,omitempty"`
}
type Diagnostic struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func String(s string) *string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return &s
}
func Value(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
func Time(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	t = t.UTC()
	return &t
}

type Provider func() Info
type providerKey struct{}

func WithProvider(ctx context.Context, p Provider) context.Context {
	return context.WithValue(ctx, providerKey{}, p)
}
func FromContext(ctx context.Context) Provider { p, _ := ctx.Value(providerKey{}).(Provider); return p }

func Unknown(reason string) Info {
	i := Info{SchemaVersion: Version, ObservedAt: time.Now().UTC(),
		Execution:    Execution{Status: "unknown", Source: "local_discovery", ReasonCode: reason},
		Workspace:    Workspace{Status: "unknown", ContentIdentity: "unknown", ReasonCode: reason},
		Lease:        Lease{Status: "unknown", AuthorityMode: "unknown", Validation: "unavailable", ReasonCode: reason},
		Permissions:  Permissions{Status: "unknown", Scope: "current_execution", ChildCeiling: Ceiling{Status: "unknown"}, ReasonCode: reason},
		Capabilities: map[string]Capability{}, Diagnostics: []Diagnostic{}}
	for action, command := range Commands {
		i.Capabilities[action] = Capability{Permission: "unknown", Availability: "unknown", Command: String(command), ReasonCode: String(reason)}
	}
	return i
}

var Commands = map[string]string{
	"jobs.submit":                    "c2j submit",
	"jobs.submit_from_workspace":     "c2j submit --from-workspace",
	"jobs.submit_from_workspace_now": "c2j submit --from-workspace-now",
	"secrets.resolve":                "c2j secret get NAME",
}

func Standalone(destination bool) Info {
	i := Unknown("no_current_execution")
	i.Execution.Status = "absent"
	i.Workspace.Status = "absent"
	i.Workspace.ReasonCode = "no_task_workspace"
	i.Lease.Status = "absent"
	i.Lease.AuthorityMode = "none_detected"
	i.Lease.Validation = "not_applicable"
	i.Permissions.Status = "not_applicable"
	for action, c := range i.Capabilities {
		switch action {
		case "jobs.submit", "jobs.submit_from_workspace_now":
			c.SubmissionKind = "standalone"
			c.ReasonCode = String("standalone_authority_not_inspected")
			if destination {
				c.Route = String("configured_jobdb")
			} else {
				c.Availability = "unavailable"
				c.ReasonCode = String("no_submission_destination")
			}
		case "jobs.submit_from_workspace":
			c.Availability = "unavailable"
			c.ReasonCode = String("no_current_task")
		case "secrets.resolve":
			c.Availability = "unavailable"
			c.ReasonCode = String("no_task_helper")
		}
		i.Capabilities[action] = c
	}
	return i
}
func (i *Info) SetCurrent(c jobcontext.Current, source string) {
	e := &i.Execution
	e.Status = "present"
	e.Source = source
	e.ReasonCode = ""
	e.TenantID = String(c.TenantID)
	e.GoalID = String(c.GoalID)
	e.JobID = String(c.JobID)
	e.JobType = String(c.JobType)
	e.TaskType = String(c.OpTaskType)
	e.InvocationPath = String(c.InvocationPath)
	e.InvocationHash = String(c.InvocationHash)
	if c.InvocationPath != "" {
		e.NodeID = String(path.Base(c.InvocationPath))
		n := c.InvocationSequence
		e.InvocationSequence = &n
	}
	e.OpType = String(c.OpType)
	e.OpStep = String(c.OpStep)
	e.CellName = String(c.CellName)
	e.CellID = String(c.CellID)
	e.CellRepo = String(c.CellRepo)
	e.RecipeSource = String(c.RepositorySource)
	e.RecipeRef = String(c.GitRef)
}

// SetCapabilities evaluates the supported routes, separately from policy grants.
// It does not contact a provider, submit a probe, or mint a credential.
func (i *Info) SetCapabilities(submit, workspace, secretHelper bool) {
	for action, c := range i.Capabilities {
		c.Permission = "unknown"
		c.Availability = "unknown"
		c.ReasonCode = String("permission_unknown")
		if i.Permissions.JobDB != nil {
			allowed := i.Permissions.JobDB.Jobs.Submit
			if action == "secrets.resolve" {
				allowed = len(i.Permissions.JobDB.Secrets.Resolve) > 0
				c.Names = append([]string(nil), i.Permissions.JobDB.Secrets.Resolve...)
			}
			c.Permission = "denied"
			c.Availability = "unavailable"
			c.ReasonCode = String("permission_denied")
			if allowed {
				c.Permission = "granted"
				c.Availability = "ready"
				c.ReasonCode = nil
			}
		}
		routeReady := submit
		c.Route = String("child_job_broker")
		c.SubmissionKind = "child"
		if action == "secrets.resolve" {
			routeReady = secretHelper
			c.Route = String("runtime_secret_helper")
			c.SubmissionKind = ""
		}
		if action == "jobs.submit_from_workspace" || action == "jobs.submit_from_workspace_now" {
			routeReady = submit && workspace
		}
		if !routeReady {
			c.Availability = "unavailable"
			c.Route = nil
			if c.Permission != "denied" {
				c.ReasonCode = String("route_unavailable")
			}
		}
		switch i.Lease.Status {
		case "lost", "expired", "cancelled":
			c.Availability = "unavailable"
			c.ReasonCode = String("execution_" + i.Lease.Status)
		case "unknown":
			if c.Availability == "ready" {
				c.Availability = "unknown"
				c.ReasonCode = String("lease_state_unknown")
			}
		}
		if action != "secrets.resolve" && i.Permissions.ParentBounds != nil {
			c.SubmitScope = i.Permissions.ParentBounds.Permissions.Submit
		}
		if action != "secrets.resolve" && i.Permissions.ChildCeiling.Status == "known" {
			c.Restrictions = []string{"inherited_child_ceiling"}
		}
		i.Capabilities[action] = c
	}
}

// SafeText removes URL credentials wherever URLs occur in a metadata string.
// It is also applied to old cell fields, not just new execution fields.
var urlPattern = regexp.MustCompile(`[A-Za-z][A-Za-z0-9+.-]*://[^\s<>"']+`)

func SafeText(s string) string {
	return urlPattern.ReplaceAllStringFunc(s, func(raw string) string {
		u, err := url.Parse(raw)
		if err != nil {
			return "[invalid URL]"
		}
		u.User = nil
		u.RawQuery = ""
		u.ForceQuery = false
		u.Fragment = ""
		u.RawFragment = ""
		return u.String()
	})
}

// MarshalSafe traverses JSON string values so URL credentials and registered
// secrets cannot escape through either text or JSON rendering. The input type is
// an allowlisted DTO, never a lease, broker options, or an environment map.
func MarshalSafe(v any, redact func(string) string) ([]byte, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var tree any
	d := json.NewDecoder(strings.NewReader(string(b)))
	d.UseNumber()
	if err = d.Decode(&tree); err != nil {
		return nil, err
	}
	var walk func(any) any
	walk = func(v any) any {
		switch x := v.(type) {
		case string:
			s := SafeText(x)
			if redact != nil {
				s = redact(s)
			}
			return s
		case map[string]any:
			for k, v := range x {
				x[k] = walk(v)
			}
		case []any:
			for n, v := range x {
				x[n] = walk(v)
			}
		}
		return v
	}
	return json.Marshal(walk(tree))
}

// Known empty grants must be explicit: clients should not need to guess whether
// an omitted false/empty value means denied or unavailable.
func (p Permissions) MarshalJSON() ([]byte, error) {
	type plain Permissions
	var grant any
	if p.JobDB != nil {
		names := p.JobDB.Secrets.Resolve
		if names == nil {
			names = []string{}
		}
		jobs := map[string]any{"submit": p.JobDB.Jobs.Submit}
		if p.JobDB.Jobs.CellNames != nil {
			jobs["cellNames"] = p.JobDB.Jobs.CellNames
		}
		grant = map[string]any{"jobs": jobs, "secrets": map[string]any{"resolve": names}}
	}
	return json.Marshal(struct {
		plain
		JobDB any `json:"jobdb"`
	}{plain(p), grant})
}
