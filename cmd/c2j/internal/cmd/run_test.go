package cmd

import (
	"github.com/spf13/cobra"
	"testing"
)

func TestExecutionFlagsAreRegisteredOnEveryEntryPoint(t *testing.T) {
	for _, cmd := range []*cobra.Command{newRunCmd(), newRunOneSpecificCmd("one"), newRunAnyCmd(), newRunLoopCmd(), newRunWithLeaseCmd(), newSubmitCmd(), newListCmd(), newListChildrenCmd()} {
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

func TestRunCommandHasOneAnyAndLoopForms(t *testing.T) {
	cmd := newRunCmd()
	if flag := cmd.Flags().Lookup("job-id"); flag == nil {
		t.Fatal("run command should default to one-job mode and expose --job-id")
	}
	if flag := cmd.Flags().Lookup("embed"); flag == nil {
		t.Fatal("run command should expose --embed for one-job mode")
	}
	if subcmd, _, err := cmd.Find([]string{"one"}); err != nil || subcmd.Use != "one" {
		t.Fatalf("run command should expose one subcommand, got %v err=%v", subcmd, err)
	}
	if subcmd, _, err := cmd.Find([]string{"any"}); err != nil || subcmd.Use != "any" {
		t.Fatalf("run command should expose any subcommand, got %v err=%v", subcmd, err)
	}
	if subcmd, _, err := cmd.Find([]string{"loop"}); err != nil || subcmd.Use != "loop" {
		t.Fatalf("run command should expose loop subcommand, got %v err=%v", subcmd, err)
	}
}

func TestRunLoopCommandDoesNotExposeEmbedFlag(t *testing.T) {
	cmd := newRunLoopCmd()
	if flag := cmd.Flags().Lookup("embed"); flag != nil {
		t.Fatal("run loop command must not expose --embed")
	}
	if flag := cmd.Flags().Lookup("concurrency"); flag == nil {
		t.Fatal("run loop command should expose --concurrency")
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

func TestRunAnyCommandDoesNotExposeConcurrencyFlag(t *testing.T) {
	cmd := newRunAnyCmd()
	if flag := cmd.Flags().Lookup("embed"); flag != nil {
		t.Fatal("run any command should not expose --embed")
	}
	if flag := cmd.Flags().Lookup("concurrency"); flag != nil {
		t.Fatal("run any command should not expose --concurrency")
	}
	if flag := cmd.Flags().Lookup("lease-duration"); flag == nil {
		t.Fatal("run any command should expose --lease-duration")
	}
}
