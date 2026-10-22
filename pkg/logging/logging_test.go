package logging_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"testing"

	"github.com/colony-2/c2j/pkg/logging"
	"github.com/stretchr/testify/require"
)

func records(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range bytes.Split(bytes.TrimSpace(buf.Bytes()), []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		var rec map[string]any
		require.NoError(t, json.Unmarshal(line, &rec))
		out = append(out, rec)
	}
	return out
}

func TestSelectiveLoggingAndDerivedFields(t *testing.T) {
	var buf bytes.Buffer
	logger, err := logging.New(&buf, logging.Options{Format: "json", Debug: []logging.Selector{{"component", "worker"}, {"job_id", "J"}, {"purpose", "story"}}})
	require.NoError(t, err)
	worker := logger.With("component", "worker", "job_id", "J").WithGroup("payload").With("saved", 1)
	worker.Debug("selected once", "value", 2)
	logger.With("component", "input").Debug("hidden")
	logger.Debug("missing field")
	logger.WithGroup("payload").Debug("nested field is not selector", "component", "worker")
	logger.With("purpose", "story").Debug("story")
	logger.Debug("call attribute", "job_id", "J")
	logger.Warn("unrelated warning", "job_id", "other")
	logger.Info("normal info")
	out := records(t, &buf)
	require.Len(t, out, 5)
	require.Equal(t, "selected once", out[0]["msg"])
	require.Equal(t, "worker", out[0]["component"])
	require.Equal(t, map[string]any{"saved": float64(1), "value": float64(2)}, out[0]["payload"])
	require.Equal(t, "story", out[1]["msg"])
	require.Equal(t, "call attribute", out[2]["msg"])
	require.Equal(t, "WARN", out[3]["level"])
	require.Equal(t, "INFO", out[4]["level"])
}

func TestDynamicBaselineWithAndWithoutSelection(t *testing.T) {
	for _, selected := range []bool{false, true} {
		t.Run(fmt.Sprint(selected), func(t *testing.T) {
			var buf bytes.Buffer
			level := new(slog.LevelVar)
			opts := logging.Options{Level: level, Format: "json"}
			if selected {
				opts.Debug = []logging.Selector{{"component", "worker"}}
			}
			logger, err := logging.New(&buf, opts)
			require.NoError(t, err)
			logger.Debug("hidden")
			level.Set(slog.LevelDebug)
			logger.Debug("now visible")
			level.Set(slog.LevelError)
			logger.Info("hidden again")
			logger.With("component", "worker").Debug("selected")
			logger.Error("always visible")
			out := records(t, &buf)
			want := 2
			if selected {
				want = 3
			}
			require.Len(t, out, want)
			require.Equal(t, "now visible", out[0]["msg"])
			require.Equal(t, "always visible", out[want-1]["msg"])
		})
	}
}

func TestConcurrentSelectionsDoNotChangeOtherLoggers(t *testing.T) {
	var buf bytes.Buffer // the shared output handler synchronizes writes
	level := new(slog.LevelVar)
	logger, err := logging.New(&buf, logging.Options{Level: level, Format: "json", Debug: []logging.Selector{{"job_id", "selected"}}})
	require.NoError(t, err)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for range 100 {
			level.Set(slog.LevelError)
			level.Set(slog.LevelInfo)
		}
	}()
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			job := "other"
			if i%2 == 0 {
				job = "selected"
			}
			l := logger.With("job_id", job)
			for range 20 {
				l.DebugContext(context.Background(), "detail")
			}
		}()
	}
	wg.Wait()
	require.Len(t, records(t, &buf), 80)
	require.Equal(t, slog.LevelInfo, level.Level())
}

func TestConfigurationValidationAndText(t *testing.T) {
	for _, value := range []string{"", "trace", "INFO+1", "nope"} {
		_, err := logging.ParseLevel(value)
		require.Error(t, err)
	}
	for _, value := range []string{"component", "component=", "unknown=x", "=x"} {
		_, err := logging.ParseSelector(value)
		require.Error(t, err)
	}
	sel, err := logging.ParseSelector("job_id=a=b")
	require.NoError(t, err)
	require.Equal(t, "a=b", sel.Value)
	_, err = logging.New(nil, logging.Options{Debug: []logging.Selector{{"unknown", "x"}}})
	require.Error(t, err)
	_, err = logging.New(nil, logging.Options{Format: "bad"})
	require.Error(t, err)
	var buf bytes.Buffer
	logger, err := logging.New(&buf, logging.Options{})
	require.NoError(t, err)
	logger.Debug("hidden")
	logger.Info("visible")
	require.Contains(t, buf.String(), "level=INFO")
	require.NotContains(t, buf.String(), "hidden")
}
