package cmd

import (
	"fmt"
	"log/slog"
	"os"

	"github.com/colony-2/c2j/pkg/logging"
	"github.com/colony-2/c2j/pkg/secrets"
	"github.com/spf13/cobra"
)

func bindLoggingFlags(root *cobra.Command) {
	var level, format string
	var debug []string
	flags := root.PersistentFlags()
	flags.StringVar(&level, "log-level", "info", "Diagnostic level: debug, info, warn, error (C2J_LOG_LEVEL)")
	flags.StringVar(&format, "log-format", "text", "Diagnostic format: text or json (C2J_LOG_FORMAT); writes to stderr")
	flags.StringArrayVar(&debug, "log-debug", nil, "Enable DEBUG for field=value; repeat for alternative selections")
	root.PersistentPreRunE = func(cmd *cobra.Command, _ []string) error {
		levelValue, formatValue := level, format
		if !flags.Changed("log-level") {
			if v, ok := os.LookupEnv("C2J_LOG_LEVEL"); ok {
				levelValue = v
			}
		}
		if !flags.Changed("log-format") {
			if v, ok := os.LookupEnv("C2J_LOG_FORMAT"); ok {
				formatValue = v
			}
		}
		parsed, err := logging.ParseLevel(levelValue)
		if err != nil {
			return err
		}
		opts := logging.Options{Level: parsed, Format: formatValue}
		// Empty explicit/env values are invalid; the helper's empty format is
		// reserved for its programmatic default.
		if formatValue == "" {
			return fmt.Errorf("invalid log format %q: expected text or json", formatValue)
		}
		for _, value := range debug {
			selection, err := logging.ParseSelector(value)
			if err != nil {
				return err
			}
			opts.Debug = append(opts.Debug, selection)
		}
		logger, err := logging.New(cmd.ErrOrStderr(), opts)
		if err != nil {
			return err
		}
		slog.SetDefault(secrets.MaskLogger(logger))
		return nil
	}
}
