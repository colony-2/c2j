package extensioneagain

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"

	"github.com/colony-2/c2j/internal/jsonexact"
)

const (
	ExitCode      = 77
	CapabilityEnv = "C2J_EXTENSION_EAGAIN_VERSION"
	ResumeEnv     = "C2J_EXTENSION_EAGAIN_RESUME"
	Version       = 1
	Persist       = "persist"
	Abandon       = "abandon"
)

type Request struct {
	Version    int             `json:"version"`
	Mode       string          `json:"mode"`
	NextInputs *map[string]any `json:"next_inputs,omitempty"`
}

type Resume struct {
	Version int    `json:"version"`
	Mode    string `json:"mode"`
	Count   uint64 `json:"count"`
}

type Control struct{ Request Request }

func (*Control) Error() string { return "extension requested EAGAIN" }

func (request Request) Valid() bool {
	return request.Version == Version && ((request.Mode == Abandon && request.NextInputs == nil) || (request.Mode == Persist && request.NextInputs != nil && *request.NextInputs != nil))
}

func (resume Resume) Valid() bool {
	return resume.Version == Version && resume.Count > 0 && (resume.Mode == Persist || resume.Mode == Abandon)
}

type supportKey struct{}
type support struct{ resume *Resume }

func WithSupport(ctx context.Context, resume *Resume) context.Context {
	return context.WithValue(ctx, supportKey{}, support{resume})
}

func Supported(ctx context.Context) (*Resume, bool) {
	value, ok := ctx.Value(supportKey{}).(support)
	return value.resume, ok
}

func Decode(raw []byte, limit int) (Request, error) {
	bad := errors.New("invalid extension EAGAIN response")
	var request Request
	if len(raw) > limit || !json.Valid(raw) {
		return request, bad
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if checkValue(decoder) != nil {
		return request, bad
	}
	if _, err := decoder.Token(); err != io.EOF {
		return request, bad
	}
	var envelope, fields map[string]json.RawMessage
	if json.Unmarshal(raw, &envelope) != nil || len(envelope) != 1 || json.Unmarshal(envelope["eagain"], &fields) != nil {
		return request, bad
	}
	for key, value := range fields {
		if (key != "version" && key != "mode" && key != "next_inputs") || bytes.Equal(value, []byte("null")) {
			return Request{}, bad
		}
	}
	if jsonexact.Unmarshal(envelope["eagain"], &request) != nil || !request.Valid() {
		return Request{}, bad
	}
	return request, nil
}

func checkValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delim, nested := token.(json.Delim)
	if !nested {
		return nil
	}
	seen := map[string]bool{}
	for decoder.More() {
		if delim == '{' {
			key, err := decoder.Token()
			if err != nil {
				return err
			}
			name, ok := key.(string)
			if !ok || seen[name] {
				return errors.New("duplicate or invalid object key")
			}
			seen[name] = true
		}
		if err := checkValue(decoder); err != nil {
			return err
		}
	}
	_, err = decoder.Token()
	return err
}
