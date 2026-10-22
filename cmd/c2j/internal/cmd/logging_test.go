package cmd

import (
	"bytes"
	"log"
	"log/slog"
	"testing"

	"github.com/colony-2/c2j/cmd/c2j/internal/buildinfo"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

func TestLoggingFlagsAndEnvironment(t *testing.T) {
	for _, tt := range []struct {
		name, envLevel, envFormat string
		args                      []string
		want, absent              string
		fail                      bool
	}{
		{name: "environment", envLevel: "debug", envFormat: "json", want: `"msg":"detail"`},
		{name: "flags override", envLevel: "invalid", envFormat: "invalid", args: []string{"--log-level=warn", "--log-format=text"}, want: "level=WARN", absent: "detail"},
		{name: "selection", envLevel: "info", envFormat: "json", args: []string{"--log-debug=component=worker", "--log-debug=job_id=J"}, want: `"msg":"detail"`, absent: "unrelated detail"},
		{name: "invalid level", envLevel: "invalid", envFormat: "text", fail: true},
		{name: "invalid format", envLevel: "info", envFormat: "invalid", fail: true},
		{name: "empty format", envLevel: "info", envFormat: "", fail: true},
		{name: "invalid selector", envLevel: "info", envFormat: "text", args: []string{"--log-debug=typo=worker"}, fail: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			previous, writer, flags := slog.Default(), log.Writer(), log.Flags()
			t.Cleanup(func() { slog.SetDefault(previous); log.SetOutput(writer); log.SetFlags(flags) })
			t.Setenv("C2J_LOG_LEVEL", tt.envLevel)
			t.Setenv("C2J_LOG_FORMAT", tt.envFormat)
			root := newRootCmd(buildinfo.Info{})
			called := false
			root.AddCommand(&cobra.Command{Use: "probe", RunE: func(cmd *cobra.Command, _ []string) error {
				called = true
				slog.Default().With("component", "worker", "job_id", "J").Debug("detail")
				slog.Debug("unrelated detail")
				slog.Warn("warning")
				log.Print("legacy")
				_, err := cmd.OutOrStdout().Write([]byte(`{"result":true}`))
				return err
			}})
			var stdout, stderr bytes.Buffer
			root.SetOut(&stdout)
			root.SetErr(&stderr)
			root.SetArgs(append([]string{"probe"}, tt.args...))
			err := root.Execute()
			if tt.fail {
				require.Error(t, err)
				require.False(t, called)
				require.Empty(t, stdout.String())
				return
			}
			require.NoError(t, err)
			require.Equal(t, `{"result":true}`, stdout.String())
			require.Contains(t, stderr.String(), tt.want)
			if tt.absent != "" {
				require.NotContains(t, stderr.String(), tt.absent)
			}
			if tt.name == "selection" {
				require.Equal(t, 1, bytes.Count(stderr.Bytes(), []byte(`"msg":"detail"`)))
			}
		})
	}
}
