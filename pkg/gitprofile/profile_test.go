package gitprofile

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
)

func fixtureProfile() Profile {
	return Profile{Version: 1, Entries: []Entry{{URL: "https://git.example/team/", RewriteFrom: []string{"https://upstream.example/team/"}, Username: "bot", SecretRef: "ro-token"}, {URL: "https://git.example/team/special/", Username: "other", SecretRef: "other-token"}}}
}
func fixtureSnapshot() jobdb.ConfigurationSnapshot {
	raw, _ := json.Marshal(fixtureProfile())
	return jobdb.ConfigurationSnapshot{Values: map[string]json.RawMessage{ReadOnlyKey: json.RawMessage(`"work"`), ProfilePrefix + "work": raw}}
}
func TestProfileValidationAndMatching(t *testing.T) {
	p := fixtureProfile()
	require.NoError(t, p.Validate(false))
	for _, tc := range []struct{ protocol, host, path, user, want string }{
		{"https", "git.example", "team/repo.git", "", "ro-token"},
		{"https", "GIT.EXAMPLE:443", "team/special/repo.git", "other", "other-token"},
		{"http", "git.example", "team/repo.git", "", ""},
		{"https", "git.example:444", "team/repo.git", "", ""},
		{"https", "git.example.evil", "team/repo.git", "", ""},
		{"https", "git.example", "teammate/repo.git", "", ""},
		{"https", "git.example", "team/../outside", "", ""},
		{"https", "git.example", "team/%2e%2e/outside", "", ""},
		{"https", "git.example", "team/special/repo.git", "bot", ""},
	} {
		t.Run(tc.protocol+tc.host+tc.path+tc.user, func(t *testing.T) {
			entry, err := p.Match(tc.protocol, tc.host, tc.path, tc.user)
			if tc.want == "" {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				require.Equal(t, tc.want, entry.SecretRef)
			}
		})
	}
	for _, bad := range []string{
		`{"version":1,"entries":[{"url":"https://x/","username":"u","secret_ref":"x","typo":true}]}`,
		`{"version":1,"entries":[{"url":"https://user:password@x/"}]}`,
		`{"version":1,"entries":[{"url":"https://x/","rewrite_from":["https://x/a/"]}]}`,
		`{"version":1,"entries":[{"url":"https://x/"},{"url":"https://X:443/"}]}`,
		`{"version":1,"entries":[{"url":"https://x/","username":"u\npassword=oops","secret_ref":"x"}]}`,
	} {
		_, err := Decode([]byte(bad), false)
		require.Error(t, err)
	}
}
func TestDefaultSelection(t *testing.T) {
	snapshot := fixtureSnapshot()
	name, err := (Selection{}).Resolve(snapshot)
	require.NoError(t, err)
	require.Equal(t, "work", name)
	name, err = (Selection{Publish: true}).Resolve(snapshot)
	require.NoError(t, err)
	require.Empty(t, name)
	name, err = (Selection{Explicit: true}).Resolve(snapshot)
	require.NoError(t, err)
	require.Empty(t, name)
	_, err = (Selection{Explicit: true, Value: "unknown"}).Resolve(snapshot)
	require.NoError(t, err)
	_, err = Load(snapshot, "unknown")
	require.Error(t, err)
}
func TestScopeFreshnessClearAndIsolation(t *testing.T) {
	var calls atomic.Int32
	resolver := func(ctx context.Context, names []string) (jobdb.SecretResolveResult, error) {
		return jobdb.SecretResolveResult{Secrets: map[string]jobdb.SecretValue{names[0]: {Value: fmt.Sprintf("token-%d", calls.Add(1))}}}, nil
	}
	s, err := NewScope(context.Background(), Selection{}, fixtureSnapshot(), resolver, "/usr/bin/c2j")
	require.NoError(t, err)
	defer s.Close()
	ctx := WithScope(context.Background(), s)
	socket := filepath.Join(s.dir, "resolver.sock")
	req := Request{Operation: "credential", Protocol: "https", Host: "git.example", Path: "team/repo.git"}
	a, err := Call(ctx, socket, req)
	require.NoError(t, err)
	b, err := Call(ctx, socket, req)
	require.NoError(t, err)
	require.NotEqual(t, a.Secret.Value, b.Secret.Value)
	require.Error(t, s.CheckOutput(map[string]string{"leak": b.Secret.Value}))
	raw, err := json.Marshal(map[string]string{"leak": b.Secret.Value})
	require.NoError(t, err)
	require.Error(t, s.CheckOutput(raw), "serialized task data must not hide secrets as base64")
	_, err = Call(ctx, socket, Request{Operation: "secret", Name: "rw-token"})
	require.Error(t, err)
	require.EqualValues(t, 2, calls.Load())
	_, err = Call(ctx, socket, Request{Operation: "activate", Name: "rw"})
	require.Error(t, err)
	_, err = Call(ctx, socket, Request{Operation: "clear"})
	require.NoError(t, err)
	_, err = Call(ctx, socket, Request{Operation: "clear"})
	require.NoError(t, err)
	_, err = Call(ctx, socket, req)
	require.Error(t, err)
	active, err := os.ReadFile(filepath.Join(s.dir, "active.gitconfig"))
	require.NoError(t, err)
	require.NotContains(t, string(active), "rewrites.gitconfig")
	_, err = Call(ctx, socket, Request{Operation: "activate", Name: "work"})
	require.NoError(t, err)
	_, err = Call(ctx, socket, req)
	require.NoError(t, err)
	env := Env(ctx, []string{"GIT_CONFIG_COUNT=9", "GIT_CONFIG_VALUE_8=bad", "GIT_TRACE=1", "GIT_ASKPASS=bad", "UNRELATED=keep"})
	require.NotContains(t, strings.Join(env, "\n"), "bad")
	require.Contains(t, env, "UNRELATED=keep")
	s.Close()
	_, err = Call(ctx, socket, req)
	require.Error(t, err)
	_, err = os.Stat(s.dir)
	require.True(t, os.IsNotExist(err))
}

func TestRedactionSurvivesCancellationUntilTaskReturns(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s, err := NewScope(ctx, Selection{}, fixtureSnapshot(), nil, "/usr/bin/c2j")
	require.NoError(t, err)
	release := s.RetainRedaction()
	defer release()
	require.NoError(t, s.redactor.Register("cancelled-task-secret"))
	cancel()
	s.Close()
	require.Error(t, s.CheckOutput([]byte(`{"leak":"cancelled-task-secret"}`)))
	require.NotContains(t, s.Error(fmt.Errorf("cancelled-task-secret")).Error(), "cancelled-task-secret")
}
func TestClearSuppressesInFlightReply(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	s, err := NewScope(context.Background(), Selection{}, fixtureSnapshot(), func(ctx context.Context, n []string) (jobdb.SecretResolveResult, error) {
		close(entered)
		<-release
		return jobdb.SecretResolveResult{Secrets: map[string]jobdb.SecretValue{n[0]: {Value: "late-secret"}}}, nil
	}, "/usr/bin/c2j")
	require.NoError(t, err)
	defer s.Close()
	socket := filepath.Join(s.dir, "resolver.sock")
	done := make(chan error, 1)
	go func() {
		_, err := Call(context.Background(), socket, Request{Operation: "secret", Name: "ro-token"})
		done <- err
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("resolver not entered")
	}
	_, err = Call(context.Background(), socket, Request{Operation: "clear"})
	require.NoError(t, err)
	close(release)
	select {
	case err := <-done:
		require.Error(t, err)
	case <-time.After(time.Second):
		t.Fatal("resolution stuck")
	}
}
