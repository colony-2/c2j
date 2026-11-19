package executiondiag

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/colony-2/c2j/pkg/recipe"
	"github.com/colony-2/jobdb/pkg/jobdb"
)

const ExcerptLimit = 1024

// SummaryFacts separates observed process status from the operation's result.
// All text must already be redacted. ExitCode is meaningful only with NormalExit.
type SummaryFacts struct {
	Stdout, Stderr                   string
	StdoutTruncated, StderrTruncated bool
	Launched, NormalExit             bool
	ExitCode                         int
	TimedOut, Cancelled              bool
	Reason                           string
	IncludeReason                    bool
}

func Summary(f SummaryFacts) string {
	text, truncated := strings.TrimSpace(f.Stderr), f.StderrTruncated
	if text == "" {
		text, truncated = strings.TrimSpace(f.Stdout), f.StdoutTruncated
	}
	if text == "" {
		truncated = false
	}
	excerpt := boundedText(text, ExcerptLimit, truncated)
	suffix, fallback := "", ""
	switch {
	case f.TimedOut:
		suffix, fallback = " [timed out]", "Timed out"
	case f.Cancelled:
		suffix, fallback = " [cancelled]", "Cancelled"
	case f.Launched && f.NormalExit && f.ExitCode >= 0:
		suffix, fallback = fmt.Sprintf(" [exit %d]", f.ExitCode), "Process exited"
	}
	if (f.TimedOut || f.Cancelled) && excerpt == "" {
		return fallback
	}
	reason := ""
	if !f.TimedOut && !f.Cancelled && (f.IncludeReason || !f.Launched || !f.NormalExit || f.ExitCode < 0) {
		separator := 0
		if excerpt != "" {
			separator = 2
		}
		reason = boundedText(strings.TrimSpace(f.Reason), recipe.FailureMessageLimit-len(suffix)-len(excerpt)-separator, false)
	}
	if reason != "" {
		if excerpt != "" {
			return excerpt + "; " + reason + suffix
		}
		return reason + suffix
	}
	if excerpt == "" {
		excerpt = fallback
		if excerpt == "" {
			excerpt = "Process failed"
		}
	}
	return excerpt + suffix
}

// Escape one rune at a time, so shortening never splits UTF-8 or an escape token.
func boundedText(s string, limit int, incomplete bool) string {
	var b strings.Builder
	ends := []int{0}
	for len(s) > 0 {
		r, n := utf8.DecodeRuneInString(s)
		token := s[:n]
		switch {
		case r == utf8.RuneError && n == 1:
			token = fmt.Sprintf("\\x%02x", s[0])
		case !unicode.IsPrint(r):
			token = strings.Trim(strconv.QuoteRuneToASCII(r), "'")
		}
		if b.Len()+len(token) > limit {
			incomplete = true
			break
		}
		b.WriteString(token)
		ends = append(ends, b.Len())
		s = s[n:]
	}
	if !incomplete {
		return b.String()
	}
	end := len(ends) - 1
	for end > 0 && ends[end] > limit-len("…") {
		end--
	}
	return b.String()[:ends[end]] + "…"
}

// Presentation uses safe capture rather than protocol bytes or output previews.
// IncludeReason is true for result/protocol failures rather than process errors.
func (r *Report) Presentation(opErr error, timedOut, includeReason bool) *recipe.FailurePresentation {
	c := r.Capture
	var ee *exec.ExitError
	normal, exit := c.Launched && c.Err == nil, 0
	if errors.As(c.Err, &ee) && ee.ExitCode() >= 0 {
		normal, exit = true, ee.ExitCode()
	}
	var native *jobdb.TimeoutError
	timedOut = timedOut || errors.As(opErr, &native) || errors.Is(opErr, context.DeadlineExceeded)
	reason := opErr.Error()
	if !includeReason && c.Err != nil {
		reason = c.Err.Error()
	}
	message := Summary(SummaryFacts{
		Stdout: string(c.Bytes(0)), Stderr: string(c.Bytes(1)),
		StdoutTruncated: c.Truncated(0), StderrTruncated: c.Truncated(1),
		Launched: c.Launched, NormalExit: normal, ExitCode: exit,
		TimedOut: timedOut, Cancelled: errors.Is(opErr, context.Canceled) || errors.Is(opErr, jobdb.ErrJobCancelled),
		Reason: r.safe(reason), IncludeReason: includeReason,
	})
	return &recipe.FailurePresentation{Format: recipe.OutputExitFormat, Message: message}
}
