package template

import (
	"encoding/json"
	"fmt"
	"strings"
)

// RenderMode defines how templates should be resolved (interpolation vs pure CEL).
type RenderMode int

const (
	// ModeInterpolation supports string interpolation with embedded expressions
	ModeInterpolation RenderMode = iota
	// ModePureCEL requires pure CEL expression only (for when conditions)
	ModePureCEL
)

// Segment represents a parsed segment of a template
type Segment interface {
	isSegment()
	Position() int // For error reporting
}

// TextSegment represents plain text in a template
type TextSegment struct {
	Text string
	Pos  int
}

// ExpressionSegment represents a CEL expression in a template
type ExpressionSegment struct {
	Expression string // Raw CEL expression
	Pos        int    // Start position in original input
}

func (TextSegment) isSegment()            {}
func (ExpressionSegment) isSegment()      {}
func (t TextSegment) Position() int       { return t.Pos }
func (e ExpressionSegment) Position() int { return e.Pos }

// parseTemplate parses a template string into CEL expression segments.
// CEL expressions use the `${{ ... }}` delimiter.
func parseTemplate(input string) ([]Segment, error) {
	var segments []Segment
	pos := 0

	for pos < len(input) {
		// Look for next `${{`
		idx := strings.Index(input[pos:], "${{")

		if idx == -1 {
			// No more expressions, rest is text
			if pos < len(input) {
				segments = append(segments, TextSegment{
					Text: input[pos:],
					Pos:  pos,
				})
			}
			break
		}

		// Add text before `${{`
		if idx > 0 {
			segments = append(segments, TextSegment{
				Text: input[pos : pos+idx],
				Pos:  pos,
			})
		}

		// Find matching }} respecting quotes
		exprStart := pos + idx + 3
		exprEnd, err := findExpressionEnd(input, exprStart)
		if err != nil {
			return nil, fmt.Errorf("at position %d: %w", exprStart, err)
		}

		// Add expression segment
		// Calculate the position after trimming spaces
		exprContent := strings.TrimSpace(input[exprStart:exprEnd])
		// Find where the actual content starts after trimming
		actualStart := exprStart
		for actualStart < exprEnd && (input[actualStart] == ' ' || input[actualStart] == '\t') {
			actualStart++
		}
		if strings.Contains(exprContent, "{{") {
			return nil, fmt.Errorf("at position %d: CEL expressions cannot contain Go template delimiters", actualStart)
		}
		segments = append(segments, ExpressionSegment{
			Expression: exprContent,
			Pos:        actualStart,
		})

		pos = exprEnd + 2 // Skip past }}
	}

	return segments, nil
}

// findExpressionEnd finds the end of a CEL expression respecting quotes
func findExpressionEnd(input string, start int) (int, error) {
	pos := start
	var inString bool
	var stringDelim rune

	for pos < len(input) {
		// Check for }} only when not in string
		if !inString && pos+1 < len(input) &&
			input[pos] == '}' && input[pos+1] == '}' {
			return pos, nil
		}

		// Prevent going past the end of the string
		if pos >= len(input) {
			break
		}

		ch := rune(input[pos])

		switch {
		case !inString && (ch == '"' || ch == '\''):
			// Starting a string
			inString = true
			stringDelim = ch
			pos++

		case inString && stringDelim == '"' && ch == '\\':
			// In double-quoted string, backslash escapes next char
			pos += 2 // Skip escape sequence
			if pos > len(input) {
				return 0, fmt.Errorf("unexpected end in escape sequence")
			}

		case inString && stringDelim == '\'' && ch == '\'' &&
			pos+1 < len(input) && input[pos+1] == '\'':
			// In single-quoted string, '' is escaped single quote
			pos += 2

		case inString && ch == stringDelim:
			// End of string (unless it's '' in single-quoted)
			if !(stringDelim == '\'' && pos+1 < len(input) && input[pos+1] == '\'') {
				inString = false
			}
			pos++

		default:
			pos++
		}
	}

	return 0, fmt.Errorf("unclosed CEL expression (missing }})")
}

// isInterpolationTemplate checks if a template needs interpolation
// Returns false if it's a single complete expression, true if it contains
// multiple expressions or mixed text and expressions
func isInterpolationTemplate(segments []Segment) bool {
	// If we have multiple segments or text mixed with expressions, it's interpolation
	if len(segments) > 1 {
		return true
	}

	// If the single segment is text, it's not a template at all
	if len(segments) == 1 {
		if _, ok := segments[0].(TextSegment); ok {
			return false // Plain text
		}
	}

	// Single expression segment = not interpolation (backward compatible)
	return false
}

// convertToString converts a value to string for interpolation
func convertToString(value interface{}) string {
	if value == nil {
		return ""
	}

	switch v := value.(type) {
	case string:
		return v
	case bool:
		if v {
			return "true"
		}
		return "false"
	case float64:
		// Check if it's actually an integer
		if v == float64(int64(v)) {
			return fmt.Sprintf("%d", int64(v))
		}
		return fmt.Sprintf("%g", v)
	case int64:
		return fmt.Sprintf("%d", v)
	case int:
		return fmt.Sprintf("%d", v)
	default:
		if marshaled, err := json.Marshal(value); err == nil {
			return string(marshaled)
		}
		return fmt.Sprintf("%v", value)
	}
}
