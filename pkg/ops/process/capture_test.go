package process

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/colony-2/c2j/pkg/secrets"
	"github.com/stretchr/testify/require"
)

func TestCaptureObservedOrderRedactionAndSegments(t *testing.T) {
	redactor := secrets.NewRedactor()
	require.NoError(t, redactor.Register("secret-token"))
	c := NewCapture(secrets.WithRedactor(context.Background(), redactor))
	writes := []struct {
		stream int
		text   string
	}{{0, "hello secret-"}, {1, "warning\n\n"}, {0, "token\n"}, {0, "\xe2"}, {1, "interleaved\n"}, {0, "\x82\xac\r\x00\n"}}
	for _, w := range writes {
		_, err := c.writer(w.stream).Write([]byte(w.text))
		require.NoError(t, err)
	}
	require.Equal(t, "hello [REDACTED]\n€\r\x00\n", string(c.Bytes(0)))
	var log bytes.Buffer
	c.WriteLog(&log)
	text := log.String()
	require.NotContains(t, text, "secret-")
	require.NotContains(t, text, "token")
	require.Contains(t, text, "[stdout] [partial] hello [REDACTED]")
	require.Contains(t, text, "[stderr] warning\n")
	require.Contains(t, text, "[stderr] \n")
	require.Less(t, strings.Index(text, "hello"), strings.Index(text, "warning"))
	require.Less(t, strings.Index(text, "warning"), strings.Index(text, "[stdout] [continued]"))
	require.Contains(t, text, `€\r\x00`)
	require.Regexp(t, `\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d\.\d{3}Z \[stdout\]`, text)
}
func TestCaptureLimitsDrainAndMaskCutoff(t *testing.T) {
	r := secrets.NewRedactor()
	require.NoError(t, r.Register("private-token"))
	c := NewCapture(secrets.WithRedactor(context.Background(), r))
	data := strings.Repeat("x", CaptureLimit-len("private-")) + "private-token"
	n, err := c.writer(0).Write([]byte(data))
	require.NoError(t, err)
	require.Equal(t, len(data), n)
	require.True(t, c.Truncated(0))
	require.LessOrEqual(t, c.size, CaptureLimit)
	require.True(t, strings.HasSuffix(string(c.Bytes(0)), "[REDACTED]"))
	var b bytes.Buffer
	c.WriteLog(&b)
	require.Contains(t, b.String(), "Capture truncated")
}
func TestCaptureConcurrentProcessesDrainAfterLimit(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c := NewCapture(ctx)
	_, _, err := ExecuteProcess(ctx, RunRequest{Capture: c, Shell: "sh", Run: `(head -c 5000000 /dev/zero >&2) & head -c 5000000 /dev/zero; wait`})
	require.NoError(t, err)
	require.True(t, c.Launched)
	require.True(t, c.Truncated(0) || c.Truncated(1))
	require.LessOrEqual(t, c.size, CaptureLimit)
}

func TestObservationLimitDoesNotTruncateProtocol(t *testing.T) {
	c := NewCapture(context.Background())
	for range observationLimit + 1 {
		_, err := c.writer(0).Write([]byte("x"))
		require.NoError(t, err)
	}
	require.Len(t, c.RawBytes(0), observationLimit+1)
	require.False(t, c.Truncated(0))
	var b bytes.Buffer
	c.WriteLog(&b)
	require.Contains(t, b.String(), "Capture truncated")
}
