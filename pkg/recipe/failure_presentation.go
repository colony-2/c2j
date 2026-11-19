package recipe

import (
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/colony-2/jobdb/pkg/jobdb"
)

const OutputExitFormat = "output_exit_v1"
const FailureMessageLimit = 2048

type FailurePresentation struct {
	Format  string `json:"format"`
	Message string `json:"message"`
}

// ExecutionPresentation accepts only the bounded, single-line format we own.
// It deliberately does not inspect a failure's Cause or guess from message text.
func ExecutionPresentation(attrs map[string]any) *FailurePresentation {
	for _, key := range []string{"command", "extension"} {
		var p *FailurePresentation
		switch details := attrs[key].(type) {
		case interface{ ExecutionFailurePresentation() *FailurePresentation }:
			p = details.ExecutionFailurePresentation()
		case map[string]any:
			switch value := details["presentation"].(type) {
			case *FailurePresentation:
				p = value
			case FailurePresentation:
				p = &value
			case map[string]any:
				format, formatOK := value["format"].(string)
				message, messageOK := value["message"].(string)
				if formatOK && messageOK {
					p = &FailurePresentation{Format: format, Message: message}
				}
			}
		}
		if p == nil {
			continue
		}
		if p.Format != OutputExitFormat || strings.TrimSpace(p.Message) == "" || len(p.Message) > FailureMessageLimit || !utf8.ValidString(p.Message) {
			continue
		}
		if strings.ContainsFunc(p.Message, func(r rune) bool { return unicode.IsControl(r) || r == '\u2028' || r == '\u2029' }) {
			continue
		}
		copy := *p
		return &copy
	}
	return nil
}

func CompactFailureMessage(f *RuntimeFailure) (string, bool) {
	if f != nil {
		if p := ExecutionPresentation(f.Attrs); p != nil && p.Message == f.Message {
			return f.Message, true
		}
	}
	return "", false
}

// CompactErrorMessage recognizes the effective structured failure, including a
// restored JobDB envelope reached through CLI wrappers. Nested failure causes
// cannot override an explicit replacement message.
func CompactErrorMessage(err error) (string, bool) {
	var app *jobdb.AppError
	if !errors.As(err, &app) {
		return "", false
	}
	switch value := app.Payload.Attrs["c2j_failure"].(type) {
	case *RuntimeFailure:
		return CompactFailureMessage(value)
	case RuntimeFailure:
		return CompactFailureMessage(&value)
	case map[string]any:
		message, messageOK := value["message"].(string)
		attrs, attrsOK := value["attrs"].(map[string]any)
		if messageOK && attrsOK {
			return CompactFailureMessage(&RuntimeFailure{Message: message, Attrs: attrs})
		}
	}
	return "", false
}
