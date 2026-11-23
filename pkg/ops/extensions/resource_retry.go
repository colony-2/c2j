package extensions

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os/exec"

	"github.com/colony-2/c2j/internal/resourceretry"
	"github.com/colony-2/c2j/pkg/ops/process"
)

const resourceRetryControlLimit = 4096
const resourceRetryControlType = "resource_retry"

type resourceRetryControl struct {
	Type    string `json:"type"`
	Version int    `json:"version"`
	Reason  string `json:"reason"`
	Source  string `json:"source"`
	Phase   string `json:"phase"`
	Replay  string `json:"replay"`
}

func decodeResourceRetry(raw []byte) (resourceRetryControl, error) {
	var zero resourceRetryControl
	invalid := errors.New("expected a complete version-1 resource_retry control")
	if len(raw) > resourceRetryControlLimit {
		return zero, invalid
	}
	envelope, err := resourceControlFields(raw)
	if err != nil || len(envelope) != 1 || envelope["control"] == nil {
		return zero, invalid
	}
	fields, err := resourceControlFields(envelope["control"])
	if err != nil || len(fields) != 6 {
		return zero, invalid
	}
	for _, name := range []string{"type", "version", "reason", "source", "phase", "replay"} {
		if fields[name] == nil {
			return zero, invalid
		}
	}
	var control resourceRetryControl
	if json.Unmarshal(envelope["control"], &control) != nil ||
		control.Type != resourceRetryControlType || control.Version != 1 ||
		control.Reason != "resource_temporarily_unavailable" || control.Replay != "restart_from_input" ||
		!resourceretry.Is(resourceretry.Restart(control.Source, control.Phase)) {
		return zero, invalid
	}
	return control, nil
}

// resourceRetryAfterExit returns nil for an ordinary extension failure.
// An explicit retry request cannot discard an attempt without a confirmed stop.
func resourceRetryAfterExit(raw []byte, capture *process.Capture, exitErr error) error {
	control, err := decodeResourceRetry(raw)
	if err != nil {
		return nil
	}
	if !capture.TreeCleanupComplete || capture.TreeCleanupError != nil {
		return errors.Join(exitErr, errors.New("extension resource retry process-tree cleanup was not confirmed"))
	}
	return resourceretry.Restart(control.Source, control.Phase)
}

// Decode exact, case-sensitive fields. A map decoder alone silently overwrites
// duplicates, and a struct decoder accepts case-insensitive protocol names.
func resourceControlFields(raw []byte) (map[string]json.RawMessage, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	first, err := dec.Token()
	if err != nil || first != json.Delim('{') {
		return nil, errors.New("expected object")
	}
	fields := map[string]json.RawMessage{}
	for dec.More() {
		key, err := dec.Token()
		if err != nil {
			return nil, err
		}
		name, ok := key.(string)
		if !ok || fields[name] != nil {
			return nil, errors.New("duplicate field")
		}
		var value json.RawMessage
		if err := dec.Decode(&value); err != nil {
			return nil, err
		}
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return nil, errors.New("null field")
		}
		fields[name] = value
	}
	if _, err := dec.Token(); err != nil {
		return nil, err
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, errors.New("expected one object")
	}
	return fields, nil
}

// A capture/wait/cleanup error joined to an exit status is not an ordinary
// completed exit, even if errors.As could find an ExitError inside it.
func cleanExit(err error, code int) bool {
	for depth := 0; err != nil && depth < 32; depth++ {
		if exit, ok := err.(*exec.ExitError); ok {
			return exit.ExitCode() == code
		}
		if joined, ok := err.(interface{ Unwrap() []error }); ok {
			causes := joined.Unwrap()
			if len(causes) != 1 {
				return false
			}
			err = causes[0]
		} else if wrapped, ok := err.(interface{ Unwrap() error }); ok {
			err = wrapped.Unwrap()
		} else {
			return false
		}
	}
	return false
}
