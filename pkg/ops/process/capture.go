package process

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/colony-2/c2j/pkg/gitprofile"
	"github.com/colony-2/c2j/pkg/secrets"
)

const CaptureLimit = 4 * 1024 * 1024
const observationLimit = 65536

// Capture records observed pipe reads, not child write order. Its writers
// always drain the pipes, even after the memory/observation budget is exhausted.
// Use one Capture per process. Read results only after ExecuteProcess returns.
type Capture struct {
	LaunchAttempted bool
	raw             [2]bytes.Buffer
	rawOmitted      [2]bool
	safeRaw         [2][]byte
	mu              sync.Mutex
	events          []observation
	size            int
	omitted         [2]bool
	redactParts     func([]string, bool) []string
	Request         RunRequest
	Started         time.Time
	Duration        time.Duration
	Launched        bool
	Err             error
	safe            [2][]string
	sealed          bool
}
type observation struct {
	stream int
	at     time.Time
	data   string
}
type captureWriter struct {
	capture *Capture
	stream  int
}

func NewCapture(ctx context.Context) *Capture {
	var gitRedactor *secrets.Redactor
	if scope := gitprofile.FromContext(ctx); scope != nil {
		gitRedactor = scope.Redactor()
	}
	redactor := secrets.RedactorFromContext(ctx)
	return &Capture{redactParts: func(parts []string, incomplete bool) []string {
		return redactor.RedactParts(parts, incomplete, gitRedactor)
	}}
}
func (c *Capture) writer(stream int) io.Writer { return captureWriter{c, stream} }
func (w captureWriter) Write(p []byte) (int, error) {
	c := w.capture
	c.mu.Lock()
	defer c.mu.Unlock()
	rawN := min(len(p), CaptureLimit-c.raw[w.stream].Len())
	_, _ = c.raw[w.stream].Write(p[:rawN])
	if rawN < len(p) {
		c.rawOmitted[w.stream] = true
	}
	n := min(len(p), CaptureLimit-c.size)
	if len(c.events) >= observationLimit {
		n = 0
	}
	if n > 0 {
		c.events = append(c.events, observation{w.stream, time.Now().UTC(), string(p[:n])})
		c.size += n
	}
	if n < len(p) {
		c.omitted[w.stream] = true
	}
	return len(p), nil
}
func (c *Capture) seal() {
	if c.sealed {
		return
	}
	for _, event := range c.events {
		c.safe[event.stream] = append(c.safe[event.stream], event.data)
	}
	for stream := range 2 {
		c.safeRaw[stream] = []byte(strings.Join(c.redactParts([]string{c.raw[stream].String()}, c.rawOmitted[stream]), ""))
		c.safe[stream] = c.redactParts(c.safe[stream], c.omitted[stream])
	}
	// Only safe observations are used for diagnostic rendering.
	offsets := [2]int{}
	for i, event := range c.events {
		c.events[i].data = c.safe[event.stream][offsets[event.stream]]
		offsets[event.stream]++
	}
	c.sealed = true
}
func (c *Capture) Bytes(stream int) []byte   { c.seal(); return c.safeRaw[stream] }
func (c *Capture) Truncated(stream int) bool { return c.rawOmitted[stream] }

// RawBytes is the protocol result, independent of diagnostic observation limits.
// Callers must not persist it as diagnostic text without redaction.
func (c *Capture) RawBytes(stream int) []byte { return c.raw[stream].Bytes() }

// WriteLog renders partial observations immediately in their observed position.
// A split UTF-8 rune is completed at its last observation; invalid/control bytes
// are escaped. Output text is never interpreted as terminal/workflow commands.
func (c *Capture) WriteLog(w io.Writer) {
	c.seal()
	if len(c.events) == 0 {
		fmt.Fprintln(w, "[No output observed.]")
	}
	pending := [2]string{}
	continued := [2]bool{}
	for _, event := range c.events {
		s := event.stream
		data := pending[s] + event.data
		pending[s] = ""
		var line strings.Builder
		emit := func(partial bool) {
			label := []string{"stdout", "stderr"}[s]
			marker := ""
			if continued[s] {
				marker += " [continued]"
			}
			if partial {
				marker += " [partial]"
			}
			fmt.Fprintf(w, "%s [%s]%s %s\n", event.at.Format("2006-01-02T15:04:05.000Z"), label, marker, line.String())
			line.Reset()
			continued[s] = partial
		}
		for len(data) > 0 {
			if !utf8.FullRuneInString(data) {
				pending[s] = data
				break
			}
			r, n := utf8.DecodeRuneInString(data)
			switch {
			case r == '\n':
				emit(false)
			case r == utf8.RuneError && n == 1:
				fmt.Fprintf(&line, "\\x%02x", data[0])
			case r == '\r':
				line.WriteString(`\r`)
			case r == '\t':
				line.WriteString(`\t`)
			case r < 32 || r == 127:
				fmt.Fprintf(&line, "\\x%02x", r)
			default:
				line.WriteRune(r)
			}
			data = data[n:]
		}
		if line.Len() > 0 || pending[s] != "" {
			emit(true)
		}
	}
	for s, data := range pending {
		if data != "" {
			fmt.Fprintf(w, "[%s] [incomplete UTF-8 bytes] % x\n", []string{"stdout", "stderr"}[s], []byte(data))
		}
	}
	if c.omitted[0] || c.omitted[1] {
		fmt.Fprintln(w, "[Capture truncated: byte or observation limit exceeded; remaining output was drained and omitted.]")
	}
}
