// Package inbox reads retained invocation descriptors without executing recipes.
package inbox

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"

	"github.com/colony-2/c2j/pkg/artifacts"
	"github.com/colony-2/c2j/pkg/story/internal/model"
	"github.com/colony-2/jobdb/pkg/jobdb"
)

// Error contains only a stable reason, never retained payloads or URLs.
type Error struct{ Reason string }

func (e *Error) Error() string    { return "inbox: " + e.Reason }
func Failure(reason string) error { return &Error{Reason: reason} }
func Reason(err error) string {
	var e *Error
	if errors.As(err, &e) {
		return e.Reason
	}
	return "history_unreadable"
}

type descriptor struct {
	Input         json.RawMessage            `json:"input"`
	InputArtifact string                     `json:"input_artifact"`
	Artifacts     map[string]json.RawMessage `json:"artifacts"`
}

// Decode recognizes activity envelopes by their input/context fields. Attachment
// decoding stays raw to preserve large numbers and distinguish malformed history.
func Decode(ctx context.Context, td jobdb.TaskData) (map[string]artifacts.Ref, error) {
	if td == nil {
		return nil, Failure("history_missing")
	}
	raw, err := td.GetData()
	if err != nil {
		return nil, ReadFailure("history", err)
	}
	d, err := decode(raw, true)
	if err != nil {
		return nil, err
	}
	if d.InputArtifact != "" {
		arts, err := td.GetArtifacts()
		if err != nil {
			return nil, ReadFailure("attachment", err)
		}
		found := false
		for _, a := range arts {
			if a != nil && a.Name() == d.InputArtifact {
				found = true
				payload, err := a.Bytes(ctx)
				if err != nil {
					return nil, ReadFailure("attachment", err)
				}
				private, err := decode(payload, false)
				if err != nil {
					return nil, Failure("attachment_malformed")
				}
				d.Input, d.Artifacts = private.Input, private.Artifacts
				break
			}
		}
		if !found {
			return nil, Failure("attachment_missing")
		}
	}
	refs := make(map[string]artifacts.Ref)
	for name, raw := range d.Artifacts {
		var ref artifacts.Ref
		if json.Unmarshal(raw, &ref) != nil {
			refs[name] = artifacts.Ref{}
			continue
		}
		refs[name] = ref
	}
	// Artifacts is the recorded selection boundary. The compiler merges typed
	// normalized value bindings into this map before recording the invocation.
	// Input JSON alone carries no type/selection proof: an ordinary document can
	// have exactly the same shape as a value descriptor.
	return refs, nil
}
func decode(raw []byte, envelope bool) (descriptor, error) {
	var fields map[string]json.RawMessage
	var d descriptor
	if len(raw) == 0 {
		return d, Failure("history_missing")
	}
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		return d, Failure("history_malformed")
	}
	if _, ok := fields["input"]; !ok {
		return d, Failure("history_unsupported")
	}
	if envelope {
		if context, ok := fields["context"]; !ok || len(bytes.TrimSpace(context)) == 0 || bytes.TrimSpace(context)[0] != '{' {
			return d, Failure("history_unsupported")
		}
	}
	if json.Unmarshal(raw, &d) != nil {
		return d, Failure("history_malformed")
	}
	if string(d.Input) != "null" {
		var input map[string]json.RawMessage
		if json.Unmarshal(d.Input, &input) != nil {
			return d, Failure("history_malformed")
		}
	}
	return d, nil
}
func Unavailable(reason string) *model.Inbox {
	return &model.Inbox{Status: "unavailable", Reason: reason, Entries: []model.InboxEntry{}}
}
func Project(ctx context.Context, td jobdb.TaskData) *model.Inbox {
	refs, err := Decode(ctx, td)
	if err != nil {
		return Unavailable(Reason(err))
	}
	result := &model.Inbox{Status: "available", Entries: make([]model.InboxEntry, 0, len(refs))}
	for binding, ref := range refs {
		e := model.InboxEntry{Binding: binding, SourceKind: string(ref.Kind), Content: "unsupported"}
		e.Path = Destination(binding, ref.NameValue())
		if e.Path == "" {
			e.Reason = "path_unavailable"
		}
		switch ref.Kind {
		case artifacts.RefKindStored:
			if key, ok := ref.StoredKey(); ok && key.Validate() == nil {
				e.SourceKey = &key
				e.Content = "stored"
			} else {
				e.Reason = "descriptor_malformed"
			}
		case artifacts.RefKindValue:
			// No ValueBytes call here: content is serialized only on demand.
			if ref.Stored != nil || ref.External != nil || len(ref.Value) == 0 || !json.Valid(ref.Value) || (ref.Format == "text" && bytes.TrimSpace(ref.Value)[0] != '"') {
				e.Reason = "descriptor_malformed"
			} else if ref.Format != "text" && ref.Format != "json" {
				e.Reason = "format_unsupported"
			} else {
				e.Content = "value"
			}
		case artifacts.RefKindExternal:
			e.Reason = "external_content_unsupported"
			// file URLs can denote directories even without Expand. No stat or fetch.
			if ref.External == nil || ref.External.Expand || strings.HasPrefix(ref.External.URL, "file:") {
				e.Path = ""
				e.Reason = "member_inventory_unavailable"
			}
		default:
			e.Reason = "source_unsupported"
		}
		result.Entries = append(result.Entries, e)
	}
	sort.Slice(result.Entries, func(i, j int) bool {
		a, b := result.Entries[i], result.Entries[j]
		if a.Binding != b.Binding {
			return a.Binding < b.Binding
		}
		return a.Path < b.Path
	})
	return result
}
func Destination(binding, name string) string {
	if binding == "" || filepath.IsAbs(binding) {
		return ""
	}
	for _, s := range strings.FieldsFunc(binding, func(r rune) bool { return r == '/' || r == '\\' }) {
		if s == ".." {
			return ""
		}
	}
	if strings.HasSuffix(binding, "/") || strings.HasSuffix(binding, "\\") {
		if name == "" {
			return ""
		}
		binding = filepath.Join(strings.TrimRight(binding, "/\\"), name)
	}
	clean := filepath.Clean(binding)
	if clean == "." || filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, "../") {
		return ""
	}
	return filepath.ToSlash(clean)
}
func Clone(in *model.Inbox) *model.Inbox {
	if in == nil {
		return nil
	}
	out := *in
	out.Entries = append([]model.InboxEntry{}, in.Entries...)
	for i := range out.Entries {
		if k := out.Entries[i].SourceKey; k != nil {
			copy := *k
			out.Entries[i].SourceKey = &copy
		}
	}
	return &out
}

// ReadFailure preserves recognizable missing/denied states without exposing error payloads.
func ReadFailure(prefix string, err error) error {
	if errors.Is(err, fs.ErrPermission) || errors.Is(err, jobdb.ErrLeasePermissionDenied) || errors.Is(err, jobdb.ErrCellForbidden) {
		return Failure(prefix + "_denied")
	}
	if errors.Is(err, fs.ErrNotExist) || errors.Is(err, jobdb.ErrJobNotFound) || errors.Is(err, jobdb.ErrChapterNotFound) {
		return Failure(prefix + "_missing")
	}
	return Failure(prefix + "_unreadable")
}
