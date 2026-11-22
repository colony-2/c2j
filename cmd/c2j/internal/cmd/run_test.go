package cmd

import (
	"context"
	"errors"
	"github.com/stretchr/testify/require"
	"io"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestExecutionFlagsAreRegisteredOnEveryEntryPoint(t *testing.T) {
	for _, cmd := range []*cobra.Command{newRunCmd(), newRunOneSpecificCmd("one"), newRunWithLeaseCmd(), newSubmitCmd(), newListCmd(), newListChildrenCmd()} {
		t.Run(cmd.Use, func(t *testing.T) {
			for _, field := range []string{"cpu", "memory", "ephemeral-storage", "platform", "image", "image-digest", "image-id"} {
				if cmd.Flags().Lookup("execution-"+field) == nil {
					t.Errorf("missing --execution-%s", field)
				}
			}
		})
	}
	for _, cmd := range []*cobra.Command{newListCmd(), newListChildrenCmd()} {
		for _, name := range []string{"compatible-with-execution", "include-unresolved"} {
			if cmd.Flags().Lookup(name) == nil {
				t.Errorf("%s missing --%s", cmd.Use, name)
			}
		}
	}
	for _, field := range []string{"cpu", "memory", "ephemeral-storage", "platform", "image"} {
		if newSubmitCmd().Flags().Lookup("require-"+field) == nil {
			t.Errorf("submit missing --require-%s", field)
		}
	}
}

func TestRunWithLeaseCommandHasProtectedInputAndNoClaimControls(t *testing.T) {
	cmd, _, err := newRunCmd().Find([]string{"with-lease"})
	if err != nil || cmd.Use != "with-lease" {
		t.Fatalf("missing with-lease command: %v", err)
	}
	for _, name := range []string{"lease-file", "job-id", "jobdb", "await-threshold", "input-mode"} {
		if cmd.Flags().Lookup(name) == nil {
			t.Errorf("missing --%s", name)
		}
	}
	for _, name := range []string{"lease-token", "lease", "worker-id", "lease-duration", "on-not-ready", "wait-timeout", "poll-interval", "embed"} {
		if cmd.Flags().Lookup(name) != nil {
			t.Errorf("with-lease must not expose --%s", name)
		}
	}
	if err := cmd.Args(cmd, []string{"credential"}); err == nil {
		t.Fatal("with-lease must reject positional credentials")
	}
}

func TestRunCommandSupportsTargetedAndSuppliedLeases(t *testing.T) {
	cmd := newRunCmd()
	for _, name := range []string{"one", "with-lease"} {
		if sub, _, err := cmd.Find([]string{name}); err != nil || sub.Use != name {
			t.Fatalf("missing %s: %v", name, err)
		}
	}
	for _, sub := range cmd.Commands() {
		if sub.Name() == "any" || sub.Name() == "loop" {
			t.Fatalf("unexpected polling command %s", sub.Name())
		}
	}
}

func TestReadyCommandExposesTenantAndRuntimeFlags(t *testing.T) {
	cmd := newReadyCmd()
	if flag := cmd.Flags().Lookup("embed"); flag != nil {
		t.Fatal("ready command should not expose --embed")
	}
	if flag := cmd.Flags().Lookup("jobdb"); flag == nil {
		t.Fatal("ready command should expose --jobdb")
	}
	if flag := cmd.Flags().Lookup("tenant-id"); flag != nil {
		t.Fatal("ready command should not expose --tenant-id")
	}
	if flag := cmd.Flags().Lookup("swf-url"); flag != nil {
		t.Fatal("ready command should not expose --swf-url")
	}
}

func TestHealthPortFlags(t *testing.T) {
	for _, cmd := range []*cobra.Command{newRunCmd(), newRunOneSpecificCmd("one"), newRunWithLeaseCmd()} {
		flag := cmd.Flags().Lookup("health-port")
		if flag == nil || flag.DefValue != "8080" {
			t.Fatalf("%s health port default: %v", cmd.Use, flag)
		}
		for _, value := range []string{"0", "9321"} {
			if err := cmd.Flags().Set("health-port", value); err != nil {
				t.Fatal(err)
			}
			port, err := cmd.Flags().GetInt("health-port")
			if err != nil || (value == "0" && port != 0) || (value == "9321" && port != 9321) {
				t.Fatalf("port override: %v %v", port, err)
			}
		}
	}
}

func TestHealthRunCommandCancellation(t *testing.T) {
	for _, cmd := range []*cobra.Command{newRunCmd(), newRunOneSpecificCmd("one"), newRunWithLeaseCmd()} {
		t.Run(cmd.Use, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			cmd.SetContext(ctx)
			cmd.SetOut(io.Discard)
			cmd.SetErr(io.Discard)
			require.NoError(t, cmd.Flags().Set("job-id", "job"))
			require.NoError(t, cmd.Flags().Set("jobdb", "http://127.0.0.1:1/tenant"))
			require.NoError(t, cmd.Flags().Set("health-port", "0"))
			if cmd.Use == "with-lease" {
				require.NoError(t, cmd.Flags().Set("lease-file", "-"))
				cmd.SetIn(strings.NewReader(`{"version":2,"tenantId":"tenant","jobId":"job","leaseId":"lease","leaseToken":"secret"}`))
			}
			err := cmd.RunE(cmd, nil)
			require.True(t, errors.Is(err, context.Canceled), "context cancellation must reach %s: %v", cmd.Use, err)
		})
	}
}
