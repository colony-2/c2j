package cmd

import "testing"

func TestWorkCommandDoesNotExposeEmbedFlag(t *testing.T) {
	cmd := newWorkCmd()
	if flag := cmd.Flags().Lookup("embed"); flag != nil {
		t.Fatal("work command must not expose --embed")
	}
	if flag := cmd.Flags().Lookup("concurrency"); flag == nil {
		t.Fatal("work command should expose --concurrency")
	}
}

func TestReadyCommandExposesTenantAndRuntimeFlags(t *testing.T) {
	cmd := newReadyCmd()
	if flag := cmd.Flags().Lookup("embed"); flag != nil {
		t.Fatal("ready command should not expose --embed")
	}
	if flag := cmd.Flags().Lookup("tenant-id"); flag == nil {
		t.Fatal("ready command should expose --tenant-id")
	}
	if flag := cmd.Flags().Lookup("swf-url"); flag == nil {
		t.Fatal("ready command should expose --swf-url")
	}
}

func TestRunOneCommandDoesNotExposeConcurrencyFlag(t *testing.T) {
	cmd := newRunOneCmd()
	if flag := cmd.Flags().Lookup("embed"); flag != nil {
		t.Fatal("runone command should not expose --embed")
	}
	if flag := cmd.Flags().Lookup("concurrency"); flag != nil {
		t.Fatal("runone command should not expose --concurrency")
	}
	if flag := cmd.Flags().Lookup("lease-duration"); flag == nil {
		t.Fatal("runone command should expose --lease-duration")
	}
}
