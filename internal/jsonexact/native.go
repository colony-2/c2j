package jsonexact

import (
	"encoding/json"
	"math/big"
	"strconv"
)

// Native prefers native numbers only when JSON round-tripping preserves their
// exact mathematical value. High-precision values remain json.Number.
// Maps and slices are updated in place; callers must own the decoded values.
func Native(v any) any {
	switch x := v.(type) {
	case json.Number:
		if n, err := x.Int64(); err == nil {
			return n
		}
		if n, err := strconv.ParseUint(string(x), 10, 64); err == nil {
			return n
		}
		if n, err := x.Float64(); err == nil {
			raw, err := json.Marshal(n)
			if err == nil && equalNumber(x, json.Number(raw)) {
				return n
			}
		}
		return x
	case map[string]any:
		for k, value := range x {
			x[k] = Native(value)
		}
		return x
	case []any:
		for i, value := range x {
			x[i] = Native(value)
		}
		return x
	default:
		return x
	}
}

func equalNumber(a, b json.Number) bool {
	x, ok := new(big.Rat).SetString(string(a))
	if !ok {
		return false
	}
	y, ok := new(big.Rat).SetString(string(b))
	return ok && x.Cmp(y) == 0
}
