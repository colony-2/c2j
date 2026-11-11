// Package jsonexact decodes persisted JSON without rounding generic numbers.
package jsonexact

import (
	"bytes"
	"encoding/json"
)

// Unmarshal retains typed destination fields and uses json.Number for generic values.
func Unmarshal(data []byte, dst any) error {
	// Match json.Unmarshal's full-document validation before mutating the destination.
	if !json.Valid(data) {
		var raw json.RawMessage
		return json.Unmarshal(data, &raw)
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	return d.Decode(dst)
}
