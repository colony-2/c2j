// Package extensionwait defines the discard-only extension wait protocol.
// A Request is scheduler metadata, never an operation result or resume input.
package extensionwait

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"
)

const ExitCode = 76
const CapabilityEnv = "C2J_EXTENSION_WAIT_VERSION"

type Dependency struct {
	Job       string `json:"job,omitempty"`
	Goal      string `json:"goal,omitempty"`
	Condition string `json:"condition"`
}

type Request struct {
	Version      int          `json:"version"`
	Until        *time.Time   `json:"until,omitempty"`
	Dependencies []Dependency `json:"dependencies,omitempty"`
}

var timestamp = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d{1,9})?(Z|[+-](0\d|1\d|2[0-3]):[0-5]\d)$`)
var earliest = time.Date(1677, 9, 21, 0, 12, 43, 145224192, time.UTC)
var latest = time.Date(2262, 4, 11, 23, 47, 16, 854775807, time.UTC)

// Decode rejects ambiguous controls without echoing producer-supplied bytes.
func Decode(raw []byte, limit int) (Request, error) {
	bad := func() (Request, error) { return Request{}, errors.New("invalid extension wait response") }
	if len(raw) > limit || !json.Valid(raw) {
		return bad()
	}
	allowed := map[string]bool{"wait": true, "version": true, "until": true, "dependencies": true, "job": true, "goal": true, "condition": true}
	d := json.NewDecoder(bytes.NewReader(raw))
	var check func() error
	check = func() error {
		t, err := d.Token()
		if err != nil {
			return err
		}
		if t == nil {
			return errors.New("null")
		}
		if delim, ok := t.(json.Delim); ok {
			switch delim {
			case '{':
				seen := map[string]bool{}
				for d.More() {
					k, e := d.Token()
					if e != nil {
						return e
					}
					key, ok := k.(string)
					if !ok || seen[key] || !allowed[key] {
						return errors.New("duplicate key")
					}
					seen[key] = true
					if e := check(); e != nil {
						return e
					}
				}
			case '[':
				for d.More() {
					if e := check(); e != nil {
						return e
					}
				}
			default:
				return errors.New("delimiter")
			}
			_, err = d.Token()
			return err
		}
		return nil
	}
	if check() != nil {
		return bad()
	}
	if _, err := d.Token(); err != io.EOF {
		return bad()
	}
	var wire struct {
		Wait *struct {
			Version      int     `json:"version"`
			Until        *string `json:"until,omitempty"`
			Dependencies []struct {
				Job       *string `json:"job,omitempty"`
				Goal      *string `json:"goal,omitempty"`
				Condition string  `json:"condition"`
			} `json:"dependencies,omitempty"`
		} `json:"wait"`
	}
	d = json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&wire) != nil || wire.Wait == nil || wire.Wait.Version != 1 {
		return bad()
	}
	w := wire.Wait
	r := Request{Version: 1}
	if w.Until != nil {
		if !timestamp.MatchString(*w.Until) {
			return bad()
		}
		t, err := time.Parse(time.RFC3339Nano, *w.Until)
		if err != nil || t.Before(earliest) || t.After(latest) {
			return bad()
		}
		t = t.UTC()
		r.Until = &t
	}
	seen := map[string]string{}
	for _, wire := range w.Dependencies {
		if (wire.Job == nil) == (wire.Goal == nil) {
			return bad()
		}
		dep := Dependency{Condition: wire.Condition}
		if wire.Job != nil {
			if strings.TrimSpace(*wire.Job) == "" {
				return bad()
			}
			dep.Job = *wire.Job
		}
		if wire.Goal != nil {
			if strings.TrimSpace(*wire.Goal) == "" {
				return bad()
			}
			dep.Goal = *wire.Goal
		}
		if dep.Condition != "success" && !(dep.Job != "" && dep.Condition == "complete") {
			return bad()
		}
		key := "job:" + dep.Job
		if dep.Goal != "" {
			key = "goal:" + dep.Goal
		}
		if condition, ok := seen[key]; ok {
			if condition != dep.Condition {
				return bad()
			}
			continue
		}
		seen[key] = dep.Condition
		r.Dependencies = append(r.Dependencies, dep)
	}
	if r.Until == nil && len(r.Dependencies) == 0 {
		return bad()
	}
	return r, nil
}

// Discard survives wrappers and cleanup errors. Only an isolated, cleaned
// control is eligible for scheduling; recognition alone must suppress capture.
type Discard struct {
	Request Request
	cleaned bool
}

func (*Discard) Error() string { return "extension attempt discarded for wait" }
func IsDiscard(err error) bool { var d *Discard; return errors.As(err, &d) }
func New(r Request) error      { return &Discard{Request: r} }

// Cleaned is called after all invocation resources have closed and the entire
// disposable directory has been removed. Other errors prevent acknowledgment.
func Cleaned(err error, cleanupErr error) error {
	if cleanupErr != nil {
		return errors.Join(err, errors.New("extension wait cleanup failed"))
	}
	if d, ok := eligible(err, false); ok {
		d.cleaned = true
	}
	return err
}
func Eligible(err error) (*Discard, bool) { return eligible(err, true) }
func eligible(err error, requireClean bool) (*Discard, bool) {
	if d, ok := err.(*Discard); ok {
		return d, !requireClean || d.cleaned
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		children := joined.Unwrap()
		if len(children) == 1 {
			return eligible(children[0], requireClean)
		}
		return nil, false
	}
	if wrapped, ok := err.(interface{ Unwrap() error }); ok {
		return eligible(wrapped.Unwrap(), requireClean)
	}
	return nil, false
}

// Redacted preserves discard disposition while removing unsafe error chains.
// It deliberately removes scheduling eligibility when redaction was necessary.
func Redacted(err error, safe string) error {
	if !IsDiscard(err) {
		return errors.New(safe)
	}
	return errors.Join(&Discard{}, fmt.Errorf("%s", safe))
}
