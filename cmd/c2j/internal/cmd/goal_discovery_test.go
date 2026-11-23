package cmd

import (
	"bytes"
	"context"

	"net/http"
	"net/http/httptest"

	"testing"

	"github.com/colony-2/c2j/internal/buildinfo"

	"github.com/stretchr/testify/require"
)

func TestExactJobGoalCommandsRejectBeforeCalls(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; w.WriteHeader(500) }))
	defer server.Close()
	for _, args := range [][]string{{"get", "G"}, {"list"}, {"fail", "G", "--message", "stop"}, {"candidates", "G"}} {
		command := newGoalsCmd()
		var out bytes.Buffer
		command.SetOut(&out)
		command.SetErr(&out)
		command.SetArgs(append(args, "--jobdb", server.URL+"/tenant"))
		require.ErrorContains(t, command.ExecuteContext(context.Background()), "unsupported")
	}
	require.Zero(t, calls)
}

func TestGoalAttachRejectedThroughRoot(t *testing.T) {
	for _, args := range [][]string{{"goals", "attach", "G", "B"}, {"goals", "attach"}} {
		root := newRootCmd(buildinfo.Info{})
		var out bytes.Buffer
		root.SetOut(&out)
		root.SetErr(&out)
		root.SetArgs(args)
		require.ErrorContains(t, root.ExecuteContext(context.Background()), "unsupported")
	}
	root := newRootCmd(buildinfo.Info{})
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"goals", "--help"})
	require.NoError(t, root.ExecuteContext(context.Background()))
	require.NotContains(t, out.String(), "attach")
}
