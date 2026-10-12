// Package configuration implements deferred, non-secret configuration references.
// JobDB owns the execution-scoped configuration cache.
package configuration

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"

	"github.com/colony-2/c2j/pkg/objects"
	"github.com/colony-2/c2j/pkg/secrets"
	"github.com/colony-2/jobdb/pkg/jobdb"
)

type Fetch func(context.Context) (jobdb.ConfigurationSnapshot, error)
type Diagnostics struct {
	Revision string                     `json:"revision"`
	Values   map[string]json.RawMessage `json:"values"`
	Missing  []string                   `json:"missing,omitempty"`
}

func MaterializeInput(ctx context.Context, typ reflect.Type, input any, fetch Fetch) (any, *Diagnostics, error) {
	var snapshot *jobdb.ConfigurationSnapshot
	var diagnostics *Diagnostics
	result, err := transformInput(typ, input, "", func(ref Reference) (any, error) {
		if snapshot == nil {
			s, err := fetch(ctx)
			if err != nil {
				return nil, err
			}
			snapshot = &s
			diagnostics = &Diagnostics{Revision: s.Revision, Values: map[string]json.RawMessage{}}
		}

		raw, ok := snapshot.Values[ref.Name]
		if !ok {
			diagnostics.Missing = append(diagnostics.Missing, ref.Name)
			return nil, fmt.Errorf("configuration %q is missing", ref.Name)
		}
		diagnostics.Values[ref.Name] = append(json.RawMessage(nil), raw...)
		var value any
		if err := json.Unmarshal(raw, &value); err != nil {
			return nil, fmt.Errorf("configuration %q: %w", ref.Name, err)
		}
		if Contains(value) || secrets.Contains(value) {
			return nil, fmt.Errorf("configuration %q contains a reserved reference encoding", ref.Name)
		}
		if refs, e := objects.Collect(value); e != nil || len(refs) > 0 {
			return nil, fmt.Errorf("configuration %q cannot supply object references", ref.Name)
		}
		valid := true
		switch ref.ExpectedType {
		case "string":
			_, valid = value.(string)
		case "number":
			_, valid = value.(float64)
		case "boolean":
			_, valid = value.(bool)
		}
		if !valid {
			return nil, fmt.Errorf("configuration %q must be %s", ref.Name, ref.ExpectedType)
		}
		return value, nil
	})
	return result, diagnostics, err
}
