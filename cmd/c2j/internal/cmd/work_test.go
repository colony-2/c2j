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
