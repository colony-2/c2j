package gitprofile

import (
	"context"
	"encoding/json"
	"github.com/colony-2/c2j/internal/executiontest"
	"path/filepath"
	"testing"
	"time"

	"github.com/colony-2/c2j/pkg/execution"
	"github.com/colony-2/c2j/pkg/executioncatalog"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
)

func TestExecutionBaseCatalogScopedDiscoveryAndHiding(t *testing.T) {
	now := time.Now().UTC()
	c := execution.BaseCatalog{Version: 1, Scope: execution.CatalogScope{Runtime: "https://test", Tenant: "t"}, Complete: true, Offerings: executiontest.Offerings("linux"), Generation: "one", PublishedAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Hour)}
	min, max := "500m", "8"
	c.Offerings[0].Capabilities = []string{"docker"}
	c.Offerings[0].Resources.CPU = &execution.ResourceRule{State: "range", Min: &min, Max: &max}
	reads := 0
	p := executioncatalog.Policy{Required: true, Scope: c.Scope, Read: func(context.Context) (jobdb.ConfigurationSnapshot, error) {
		reads++
		raw, _ := json.Marshal(c)
		return jobdb.ConfigurationSnapshot{Revision: c.Generation, Values: map[string]json.RawMessage{execution.BaseCatalogKey: raw}}, nil
	}}
	snapshot := jobdb.ConfigurationSnapshot{Values: map[string]json.RawMessage{execution.BaseCatalogKey: json.RawMessage(`"hidden"`), "ordinary": json.RawMessage(`"visible"`)}}
	s, err := NewScope(executioncatalog.WithPolicy(context.Background(), p), Selection{}, snapshot, nil, "")
	require.NoError(t, err)
	defer s.Close()
	socket := filepath.Join(s.dir, "resolver.sock")
	_, err = Call(context.Background(), socket, Request{Operation: "config", Name: execution.BaseCatalogKey})
	require.ErrorContains(t, err, "reserved")
	res, err := Call(context.Background(), socket, Request{Operation: "config", Name: "ordinary"})
	require.NoError(t, err)
	require.JSONEq(t, `"visible"`, string(res.Value))
	require.Zero(t, reads)
	res, err = Call(context.Background(), socket, Request{Operation: "execution_bases"})
	require.NoError(t, err)
	require.Contains(t, string(res.Value), `"generation":"one"`)
	var view executioncatalog.View
	require.NoError(t, json.Unmarshal(res.Value, &view))
	require.Equal(t, []string{"docker"}, view.Catalog.Offerings[0].Capabilities)
	require.Equal(t, "500m", *view.Catalog.Offerings[0].Resources.CPU.Min)
	require.Equal(t, "8", *view.Catalog.Offerings[0].Resources.CPU.Max)
	c.Generation = "two"
	c.Offerings = executiontest.Offerings("macos-15-arm64")
	res, err = Call(context.Background(), socket, Request{Operation: "execution_bases"})
	require.NoError(t, err)
	require.Contains(t, string(res.Value), "macos-15-arm64")
	require.Equal(t, 2, reads)
	fork, err := s.Fork(context.Background(), Selection{})
	require.NoError(t, err)
	defer fork.Close()
	res, err = Call(context.Background(), filepath.Join(fork.dir, "resolver.sock"), Request{Operation: "execution_bases"})
	require.NoError(t, err)
	require.Contains(t, string(res.Value), `"generation":"two"`)
	s.Close()
	_, err = Call(context.Background(), filepath.Join(fork.dir, "resolver.sock"), Request{Operation: "execution_bases"})
	require.Error(t, err)
}
func TestExecutionBaseCatalogScopeFencesInflightDiscovery(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	p := executioncatalog.Policy{Read: func(context.Context) (jobdb.ConfigurationSnapshot, error) {
		close(started)
		<-release
		return jobdb.ConfigurationSnapshot{}, nil
	}}
	s, err := NewScope(executioncatalog.WithPolicy(context.Background(), p), Selection{}, jobdb.ConfigurationSnapshot{}, nil, "")
	require.NoError(t, err)
	defer s.Close()
	result := make(chan error, 1)
	go func() {
		_, err := Call(context.Background(), filepath.Join(s.dir, "resolver.sock"), Request{Operation: "execution_bases"})
		result <- err
	}()
	<-started
	_, err = Call(context.Background(), filepath.Join(s.dir, "resolver.sock"), Request{Operation: "clear"})
	require.NoError(t, err)
	close(release)
	require.ErrorContains(t, <-result, "execution access ended")
}

func TestExecutionBaseCatalogForkFencesInflightDiscovery(t *testing.T) {
	started, release := make(chan context.Context, 1), make(chan struct{})
	p := executioncatalog.Policy{Read: func(ctx context.Context) (jobdb.ConfigurationSnapshot, error) {
		started <- ctx
		<-release // Model a late reply even after cancellation.
		return jobdb.ConfigurationSnapshot{}, nil
	}}
	parent, err := NewScope(executioncatalog.WithPolicy(context.Background(), p), Selection{}, jobdb.ConfigurationSnapshot{}, nil, "")
	require.NoError(t, err)
	defer parent.Close()
	fork, err := parent.Fork(context.Background(), Selection{})
	require.NoError(t, err)
	defer fork.Close()
	result := make(chan error, 1)
	go func() {
		_, err := Call(context.Background(), filepath.Join(fork.dir, "resolver.sock"), Request{Operation: "execution_bases"})
		result <- err
	}()
	readContext := <-started
	parent.Close()
	canceled := false
	select {
	case <-readContext.Done():
		canceled = true
	case <-time.After(time.Second):
	}
	close(release)
	require.True(t, canceled, "parent exit must cancel the fork's pending authority read")
	require.Error(t, <-result, "a reply arriving after parent exit must not become authoritative discovery")
}
