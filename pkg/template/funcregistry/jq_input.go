package funcregistry

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/big"
	"reflect"

	"github.com/colony-2/c2j/pkg/artifacts"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/google/cel-go/common/types/ref"
	"google.golang.org/protobuf/types/known/structpb"
)

// jqInput builds detached, jq-compatible containers and numbers. Typed artifact
// references become queryable objects only here, not in stored context.
func jqInput(value any) (any, error) {
	switch v := value.(type) {
	case ref.Val:
		return jqInput(v.Value())
	case nil, bool, string, int, float64:
		return v, nil
	case json.Number:
		if n, ok := new(big.Int).SetString(string(v), 10); ok {
			return jqInput(n)
		}
		return v.Float64()
	case *big.Int:
		if v == nil {
			return nil, nil
		}
		if v.IsInt64() {
			return jqInput(v.Int64())
		}
		return new(big.Int).Set(v), nil
	case structpb.NullValue:
		return nil, nil
	case json.RawMessage:
		if len(v) == 0 {
			return nil, nil
		}
		if !json.Valid(v) {
			return nil, fmt.Errorf("invalid JSON in jq input")
		}
		decoder := json.NewDecoder(bytes.NewReader(v))
		decoder.UseNumber()
		var decoded any
		if err := decoder.Decode(&decoded); err != nil {
			return nil, err
		}
		return jqInput(decoded)
	case artifacts.Ref:
		out := map[string]any{"kind": string(v.Kind)}
		if v.Name != "" {
			out["name"] = v.Name
		}
		if v.Stored != nil {
			out["stored"] = v.Stored
		}
		if v.External != nil {
			out["external"] = v.External
		}
		if v.Format != "" {
			out["format"] = v.Format
		}
		if len(v.Value) > 0 {
			out["value"] = v.Value
		}
		return jqInput(out)
	case artifacts.StoredRef:
		return jqInput(map[string]any{"key": v.Key})
	case artifacts.ExternalRef:
		out := map[string]any{"url": v.URL}
		if v.Expand {
			out["expand"] = true
		}
		return out, nil
	case jobdb.ArtifactKey:
		return jqInput(map[string]any{"jobId": v.JobId, "taskOrdinal": v.TaskOrdinal, "name": v.Name, "sizeBytes": v.SizeBytes})
	}
	v := reflect.ValueOf(value)
	switch v.Kind() {
	case reflect.Bool:
		return v.Bool(), nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		n := v.Int()
		if int64(int(n)) == n {
			return int(n), nil
		}
		return big.NewInt(n), nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		n := v.Uint()
		if n <= uint64(^uint(0)>>1) {
			return int(n), nil
		}
		return new(big.Int).SetUint64(n), nil
	case reflect.Float32, reflect.Float64:
		return v.Float(), nil
	case reflect.Pointer, reflect.Interface:
		if v.IsNil() {
			return nil, nil
		}
		return jqInput(v.Elem().Interface())
	case reflect.String:
		return v.String(), nil
	case reflect.Struct:
		// Preserve existing opaque values (for example child-job bookkeeping)
		// when jq carries them through a larger object without inspecting them.
		return value, nil
	case reflect.Map:
		out := make(map[string]any, v.Len())
		iter := v.MapRange()
		for iter.Next() {
			key, err := jqInput(iter.Key().Interface())
			if err != nil {
				return nil, err
			}
			name, ok := key.(string)
			if !ok {
				return nil, fmt.Errorf("jq input requires string map keys, got %T", key)
			}
			item, err := jqInput(iter.Value().Interface())
			if err != nil {
				return nil, err
			}
			out[name] = item
		}
		return out, nil
	case reflect.Slice, reflect.Array:
		out := make([]any, v.Len())
		for i := range out {
			item, err := jqInput(v.Index(i).Interface())
			if err != nil {
				return nil, err
			}
			out[i] = item
		}
		return out, nil
	default:
		return nil, fmt.Errorf("unsupported jq input type %T", value)
	}
}
