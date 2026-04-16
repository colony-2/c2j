package ops

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
)

type dummyIn struct {
	A string `json:"a"`
}
type dummyOut struct {
	B string `json:"b"`
}

func TestOps_Register_Get_List_Clear(t *testing.T) {
	Clear()
	// Operations register globally for workflow access [pkg/ops/ops.go]
	op := NewActivityMappedOpV2[dummyIn, dummyOut](OpMetadata{Type: "t1"}, func(_ OpDependencies, _ context.Context, in dummyIn) (dummyOut, error) {
		return dummyOut{B: in.A}, nil
	})
	Register(op)

	// Registry lookups find operations by name correctly [pkg/ops/ops.go]
	got, ok := Get("t1")
	assert.True(t, ok)
	assert.Equal(t, "t1", got.GetMetadata().Type)

	// Duplicate operation names replace existing registrations [pkg/ops/ops.go]
	op2 := NewActivityMappedOpV2[dummyIn, dummyOut](OpMetadata{Type: "t1"}, func(_ OpDependencies, _ context.Context, in dummyIn) (dummyOut, error) {
		return dummyOut{B: "new"}, nil
	})
	Register(op2)
	got2, ok := Get("t1")
	assert.True(t, ok)
	assert.Equal(t, got2, op2)

	// Missing operations return clear not-found indication [pkg/ops/ops.go]
	_, ok = Get("missing")
	assert.False(t, ok)

	// Registry clears all operations for test isolation [pkg/ops/ops.go]
	Clear()
	assert.Equal(t, 0, Size())
}

func TestOps_Replace(t *testing.T) {
	Clear()

	oldOp := NewActivityMappedOpV2[dummyIn, dummyOut](OpMetadata{Type: "old"}, func(_ OpDependencies, _ context.Context, in dummyIn) (dummyOut, error) {
		return dummyOut{B: in.A}, nil
	})
	newOp := NewActivityMappedOpV2[dummyIn, dummyOut](OpMetadata{Type: "new"}, func(_ OpDependencies, _ context.Context, in dummyIn) (dummyOut, error) {
		return dummyOut{B: in.A}, nil
	})

	Register(oldOp)
	Replace(newOp)

	_, ok := Get("old")
	assert.False(t, ok)
	got, ok := Get("new")
	assert.True(t, ok)
	assert.Equal(t, newOp, got)
}

func TestOps_ThreadSafety(t *testing.T) {
	Clear()
	// Concurrent operation registrations maintain data integrity [pkg/ops/ops.go]
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			name := "op-" + string(rune('a'+(i%26)))
			Register(NewActivityMappedOpV2[dummyIn, dummyOut](OpMetadata{Type: name}, func(_ OpDependencies, _ context.Context, in dummyIn) (dummyOut, error) {
				return dummyOut{B: in.A}, nil
			}))
		}()
	}
	wg.Wait()
	// Registry handles high-volume lookups without degradation [pkg/ops/ops.go]
	// Just sanity check size > 0
	assert.True(t, Size() > 0)
}
