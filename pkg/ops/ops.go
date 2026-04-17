package ops

import (
	"sync"
)

// opsRegistry is our singleton instance
type opsRegistry struct {
	mu  sync.RWMutex
	ops map[string]RegisterableOp
}

// singleton instance
var (
	instance *opsRegistry
	once     sync.Once
)

// getInstance returns the singleton instance of opsRegistry
func getInstance() *opsRegistry {
	once.Do(func() {
		instance = &opsRegistry{ops: map[string]RegisterableOp{}}
	})
	return instance
}

// Register adds a new operatio(s)) to the registry
func Register(ops ...RegisterableOp) {
	registry := getInstance()
	registry.mu.Lock()
	defer registry.mu.Unlock()
	registry.registerLocked(ops...)
}

// Replace atomically swaps the registry contents with the provided operations.
func Replace(ops ...RegisterableOp) {
	registry := getInstance()
	registry.mu.Lock()
	defer registry.mu.Unlock()
	registry.ops = make(map[string]RegisterableOp, len(ops)*2)
	registry.registerLocked(ops...)
}

func (r *opsRegistry) registerLocked(ops ...RegisterableOp) {
	for _, op := range ops {
		r.ops[op.GetName()] = op
		r.ops[op.GetMetadata().Type] = op
	}
}

// Get retrieves an operation by name with existence check
func Get(name string) (RegisterableOp, bool) {
	registry := getInstance()
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	value, exists := registry.ops[name]
	return value, exists
}

// List returns all registered operation names
func List() []RegisterableOp {
	registry := getInstance()
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	ops := make([]RegisterableOp, 0, 10)
	for _, op := range registry.ops {
		ops = append(ops, op)
	}
	return ops
}

// Clear removes all operations from the registry
func Clear() {
	registry := getInstance()
	registry.mu.Lock()
	defer registry.mu.Unlock()
	registry.ops = map[string]RegisterableOp{}
}

// Size returns the number of registered operations
func Size() int {
	registry := getInstance()
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	return len(registry.ops)
}
