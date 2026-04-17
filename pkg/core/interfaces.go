package core

import "context"

// Storage defines the interface for persistent storage operations.
type Storage interface {
	// Position operations
	SavePosition(ctx context.Context, pos Position) error
	GetPositions(ctx context.Context) ([]Position, error)
	DeletePosition(ctx context.Context, cellID string) error

	// Container ID operations
	SaveContainerID(ctx context.Context, cellID, containerID string) error
	GetContainerID(ctx context.Context, cellID string) (string, error)
	DeleteContainerID(ctx context.Context, cellID string) error

	// Lifecycle
	Close() error
}

// GraphBuilder defines the interface for building the dependency graph.
type GraphBuilder interface {
	// BuildGraph constructs the dependency graph from the file system.
	BuildGraph(ctx context.Context) (*Graph, error)

	// GetCell retrieves a single cell by ID.
	GetCell(ctx context.Context, cellID string) (*Cell, error)
}
