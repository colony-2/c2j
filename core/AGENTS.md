# server/core

## Overview

The core module provides foundational domain types and interfaces for the colony2 dependency graph system. It defines shared contracts for graph representation, storage operations, and data structures used across all server modules.

## Architecture

### Core Components
- **Domain Types**: `Cell`, `Edge`, `Graph`, `Position` - fundamental data structures for dependency graph representation
- **Storage Interface**: Contract for persistent storage operations (positions, container mappings)
- **GraphBuilder Interface**: Contract for constructing dependency graphs from filesystem analysis

### Module Relationships
- **Pure Interface Layer**: No implementations, only type definitions and contracts
- **Dependency Root**: All other server modules depend on core, core depends on nothing
- **Consumer Modules**: storage (implements Storage), graph (implements GraphBuilder), api (uses types), container (uses cell IDs)

## Key Interfaces

### Storage Interface
```go
type Storage interface {
    SavePosition(ctx context.Context, pos Position) error
    GetPositions(ctx context.Context) ([]Position, error)
    DeletePosition(ctx context.Context, cellID string) error
    SaveContainerID(ctx context.Context, cellID, containerID string) error
    GetContainerID(ctx context.Context, cellID string) (string, error)
    DeleteContainerID(ctx context.Context, cellID string) error
    Close() error
}
```

### GraphBuilder Interface
```go
type GraphBuilder interface {
    BuildGraph(ctx context.Context) (*Graph, error)
    GetCell(ctx context.Context, cellID string) (*Cell, error)
}
```

### Core Types
```go
type Cell struct {
    ID           string   `json:"id"`
    Name         string   `json:"name"`
    Path         string   `json:"path"`
    Type         string   `json:"type"`
    Dependencies []string `json:"dependencies"`
}

type Graph struct {
    Cells []Cell `json:"cells"`
    Edges []Edge `json:"edges"`
}

type Position struct {
    CellID string  `json:"cellId"`
    X      float64 `json:"x"`
    Y      float64 `json:"y"`
}
```

## Usage Examples

### GraphBuilder Implementation
```go
// Implementing GraphBuilder interface
type Builder struct {
    rootPath string
}

func (b *Builder) BuildGraph(ctx context.Context) (*core.Graph, error) {
    // Scan filesystem and build dependency graph
    cells := []core.Cell{}
    // ... build logic
    return &core.Graph{Cells: cells, Edges: edges}, nil
}

func (b *Builder) GetCell(ctx context.Context, cellID string) (*core.Cell, error) {
    graph, err := b.BuildGraph(ctx)
    if err != nil {
        return nil, err
    }
    // Find cell by ID in graph
    for _, cell := range graph.Cells {
        if cell.ID == cellID {
            return &cell, nil
        }
    }
    return nil, ErrCellNotFound
}
```

### Storage Implementation
```go
// Creating storage instances
func NewBoltStorage(config Config) (core.Storage, error) {
    return bolt.New(config.DatabasePath, config.ReadOnly)
}

func NewMemoryStorage() core.Storage {
    return memory.New()
}

// Using storage interface
func savePosition(storage core.Storage, cellID string, x, y float64) error {
    pos := core.Position{CellID: cellID, X: x, Y: y}
    return storage.SavePosition(context.Background(), pos)
}
```

### API Integration
```go
// Using core types in HTTP handlers
func graphHandler(graphBuilder core.GraphBuilder) http.HandlerFunc {
    return func(w http.ResponseWriter, r *http.Request) {
        graph, err := graphBuilder.BuildGraph(r.Context())
        if err != nil {
            http.Error(w, err.Error(), http.StatusInternalServerError)
            return
        }
        json.NewEncoder(w).Encode(graph)
    }
}
```

## Configuration

Module configuration in go.mod:
```
module github.com/colony-2/colony2/server/core
go 1.24
```

Moon.yml module configuration:
```yaml
type: library
language: go
```

Consumer modules use local replace directives:
```
replace github.com/colony-2/colony2/server/core => ../core
```