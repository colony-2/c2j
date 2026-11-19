package executiondiag

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/colony-2/c2j/internal/celltest"
	"github.com/colony-2/c2j/pkg/gitprofile"
	"github.com/colony-2/c2j/pkg/ops/process"
	"github.com/colony-2/c2j/pkg/secrets"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
)

func TestCompactSummary(t *testing.T) {
	for _, tc := range []struct {
		name  string
		facts SummaryFacts
		want  string
	}{
		{"stderr", SummaryFacts{Stderr: " Required tool unavailable: go\n", Stdout: "noise", Launched: true, NormalExit: true, ExitCode: 1}, "Required tool unavailable: go [exit 1]"},
		{"stdout", SummaryFacts{Stderr: " \t\n", Stdout: "Validation failed: missing package", Launched: true, NormalExit: true, ExitCode: 1}, "Validation failed: missing package [exit 1]"},
		{"silent", SummaryFacts{Stderr: " \t\n", Launched: true, NormalExit: true, ExitCode: 7}, "Process exited [exit 7]"},
		{"tool quotes", SummaryFacts{Stderr: `Error: "bad"`, Launched: true, NormalExit: true, ExitCode: 9}, `Error: "bad" [exit 9]`},
		{"controls", SummaryFacts{Stderr: " x\n\r\t\x1b[31m\x00\xff€\u2028z ", Launched: true, NormalExit: true, ExitCode: 1}, `x\n\r\t\x1b[31m\x00\xff€\u2028z [exit 1]`},
		{"timeout", SummaryFacts{Stderr: "partial", TimedOut: true, Cancelled: true}, "partial [timed out]"},
		{"silent truncated", SummaryFacts{Stdout: "   ", StdoutTruncated: true, Launched: true, NormalExit: true, ExitCode: 7}, "Process exited [exit 7]"},
		{"unknown exit", SummaryFacts{Launched: true, NormalExit: true, ExitCode: -1, Reason: "signal: killed"}, "signal: killed"},
		{"silent timeout", SummaryFacts{TimedOut: true}, "Timed out"},
		{"cancel", SummaryFacts{Stdout: "partial", Cancelled: true}, "partial [cancelled]"},
		{"silent cancel", SummaryFacts{Cancelled: true}, "Cancelled"},
		{"launch", SummaryFacts{Reason: "fork: no such file"}, "fork: no such file"},
		{"signal", SummaryFacts{Launched: true, Stderr: "partial", Reason: "signal: terminated"}, "partial; signal: terminated"},
		{"result", SummaryFacts{Launched: true, NormalExit: true, ExitCode: 0, Stderr: "value is invalid", IncludeReason: true, Reason: "output failed schema validation: expected string"}, "value is invalid; output failed schema validation: expected string [exit 0]"},
		{"silent result", SummaryFacts{Launched: true, NormalExit: true, IncludeReason: true, Reason: "invalid JSON"}, "invalid JSON [exit 0]"},
		{"capture cutoff", SummaryFacts{Stdout: "partial", StdoutTruncated: true, Launched: true, NormalExit: true, ExitCode: 7}, "partial… [exit 7]"},
	} {
		t.Run(tc.name, func(t *testing.T) { require.Equal(t, tc.want, Summary(tc.facts)) })
	}
}

func TestCompactSummaryBudgets(t *testing.T) {
	for _, text := range []string{strings.Repeat("€", 2000), strings.Repeat("\x1b", 2000), strings.Repeat("\xff", 2000), strings.Repeat("x", 1024)} {
		excerpt := boundedText(text, ExcerptLimit, false)
		require.True(t, utf8.ValidString(excerpt))
		require.LessOrEqual(t, len(excerpt), ExcerptLimit)
		if strings.Contains(text, "\x1b") {
			require.Equal(t, strings.Repeat(`\x1b`, 255)+"…", excerpt)
		}
		summary := Summary(SummaryFacts{Stderr: text, Reason: strings.Repeat("\n€", 2000), Launched: true, NormalExit: true, IncludeReason: true})
		require.LessOrEqual(t, len(summary), 2048)
		require.True(t, utf8.ValidString(summary))
		require.Contains(t, summary, "; ")
		require.True(t, strings.HasSuffix(summary, "… [exit 0]"), summary)
	}
}

func TestCompactSummaryRedactedCapture(t *testing.T) {
	redactor := secrets.NewRedactor()
	require.NoError(t, redactor.Register("private-credential"))
	ctx := secrets.WithRedactor(context.Background(), redactor)
	c := process.NewCapture(ctx)
	_, _, err := process.ExecuteProcess(ctx, process.RunRequest{Capture: c, Shell: "sh", Run: `printf private-; sleep .02; printf credential; exit 1`})
	require.Error(t, err)
	require.Contains(t, string(c.RawBytes(0)), "private-credential")
	require.Equal(t, "[REDACTED] [exit 1]", Summary(SummaryFacts{Stdout: string(c.Bytes(0)), Launched: true, NormalExit: true, ExitCode: 1}))
}

func TestCompactSummaryGitMasksAndCaptureCutoff(t *testing.T) {
	profile := gitprofile.Profile{Version: 1, Entries: []gitprofile.Entry{{URL: "https://git.example/", Username: "bot", SecretRef: "token"}}}
	raw, err := json.Marshal(profile)
	require.NoError(t, err)
	scope, err := gitprofile.NewScope(celltest.Context(context.Background()), gitprofile.Selection{Explicit: true, Value: "test"}, jobdb.ConfigurationSnapshot{Values: map[string]json.RawMessage{gitprofile.ProfilePrefix + "test": raw}}, func(context.Context, []string) (jobdb.SecretResolveResult, error) {
		return jobdb.SecretResolveResult{Secrets: map[string]jobdb.SecretValue{"token": {Value: "private-git-token"}}}, nil
	}, "/usr/bin/c2j")
	require.NoError(t, err)
	defer scope.Close()
	ctx := gitprofile.WithScope(celltest.Context(context.Background()), scope)
	capture := process.NewCapture(ctx)
	var socket string
	for _, v := range gitprofile.Env(ctx, nil) {
		if s, ok := strings.CutPrefix(v, gitprofile.SocketEnv+"="); ok {
			socket = s
		}
	}
	_, err = gitprofile.Call(ctx, socket, gitprofile.Request{Operation: "secret", Name: "token"})
	require.NoError(t, err)
	script := fmt.Sprintf(`printf private-git-token; head -c %d /dev/zero | tr '\000' x; printf private-git-token; exit 1`, process.CaptureLimit-len("private-git-token")-len("private-"))
	_, _, err = process.ExecuteProcess(ctx, process.RunRequest{Capture: capture, Shell: "sh", Run: script})
	require.Error(t, err)
	require.True(t, capture.Truncated(0))
	safe := string(capture.Bytes(0))
	require.NotContains(t, safe, "private-")
	require.True(t, strings.HasSuffix(safe, "[REDACTED]"))
	report := &Report{ctx: ctx, Capture: capture}
	p := report.Presentation(errors.New("specific private-git-token"), false, true)
	require.True(t, strings.HasPrefix(p.Message, "[REDACTED]"))
	require.True(t, strings.HasSuffix(p.Message, "; specific [REDACTED] [exit 1]"))
	require.NotContains(t, p.Message, "private-")
	require.Contains(t, p.Message, "…")
	require.LessOrEqual(t, len(p.Message), 2048)
}
