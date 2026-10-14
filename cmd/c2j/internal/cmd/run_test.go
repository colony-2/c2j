package cmd

import (
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
