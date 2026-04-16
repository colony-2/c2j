package ops

import (
	"gorm.io/gorm"

	"github.com/colony-2/c2j/recipe-core/pkg/workflowctl"
)

// ServiceDependencies2 defines the typed dependencies exposed to management services
// and ops that require access to shared runtime collaborators.
type ServiceDependencies2 interface {
	// WorkflowControl returns a typed workflow controller if available.
	WorkflowControl() workflowctl.WorkflowControl

	// SSEManager returns the server-sent-event manager when supported.
	SSEManager() SSEManager

	// Database returns the shared GORM handle for the current runtime when available.
	Database() *gorm.DB

	// serviceDependenciesMarker seals the interface to recipe-core implementations.
	serviceDependenciesMarker()
}

// ServiceDepsBuilder constructs ServiceDependencies2 instances via a fluent API.
type ServiceDepsBuilder struct {
	workflowCtl workflowctl.WorkflowControl
	sseManager  SSEManager
	database    *gorm.DB
	result      ServiceDependencies2
}

// NewServiceDepsBuilder returns a new builder for ServiceDependencies2 implementations.
func NewServiceDepsBuilder() *ServiceDepsBuilder { return &ServiceDepsBuilder{} }

// WithWorkflowControl configures the workflow controller dependency.
func (b *ServiceDepsBuilder) WithWorkflowControl(ctl workflowctl.WorkflowControl) *ServiceDepsBuilder {
	b.workflowCtl = ctl
	return b
}

// WithSSEManager configures the SSE manager dependency.
func (b *ServiceDepsBuilder) WithSSEManager(mgr SSEManager) *ServiceDepsBuilder {
	b.sseManager = mgr
	return b
}

// WithDatabase configures the shared database handle. Passing nil leaves the dependency unset.
func (b *ServiceDepsBuilder) WithDatabase(db *gorm.DB) *ServiceDepsBuilder {
	b.database = db
	return b
}

// Build materializes the immutable ServiceDependencies2 instance.
func (b *ServiceDepsBuilder) Build() ServiceDependencies2 {
	b.result = &serviceDependencies{
		workflowCtl: b.workflowCtl,
		sseManager:  b.sseManager,
		database:    b.database,
	}
	return b.result
}

type serviceDependencies struct {
	workflowCtl workflowctl.WorkflowControl
	sseManager  SSEManager
	database    *gorm.DB
}

// WorkflowControl returns the controller or nil if not set.
func (d *serviceDependencies) WorkflowControl() workflowctl.WorkflowControl {
	return d.workflowCtl
}

// SSEManager returns the SSE manager or nil if not set.
func (d *serviceDependencies) SSEManager() SSEManager {
	return d.sseManager
}

// Database returns the GORM handle or nil if not set.
func (d *serviceDependencies) Database() *gorm.DB {
	return d.database
}

func (d *serviceDependencies) serviceDependenciesMarker() {
}
