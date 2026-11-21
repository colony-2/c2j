package executioncatalog

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/colony-2/c2j/pkg/execution"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestExecutionBaseCatalogFreshPolicy(t *testing.T) {
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	c := execution.BaseCatalog{Version: 1, Scope: execution.CatalogScope{Runtime: "https://jobdb", Tenant: "t"}, Complete: true, NamedBases: []string{"macos-15-arm64"}, Dynamic: []execution.DynamicBaseRule{{Kind: "oci"}}, Generation: "one", PublishedAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Hour)}
	reads := 0
	p := Policy{Required: true, Scope: c.Scope, Now: func() time.Time { return now }, Read: func(ctx context.Context) (jobdb.ConfigurationSnapshot, error) {
		reads++
		deadline, ok := ctx.Deadline()
		require.True(t, ok)
		require.LessOrEqual(t, time.Until(deadline), 10*time.Second)
		raw, _ := json.Marshal(c)
		return jobdb.ConfigurationSnapshot{Revision: "revision", Values: map[string]json.RawMessage{execution.BaseCatalogKey: raw}}, nil
	}}
	base := "macos-15-arm64"
	require.NoError(t, p.Check(context.Background(), execution.Requirements{Base: &base}, "node"))
	c.NamedBases = []string{}
	err := p.Check(context.Background(), execution.Requirements{Base: &base}, "node")
	var app *jobdb.AppError
	require.ErrorAs(t, err, &app)
	require.Equal(t, Unsupported, app.Payload.Attrs["code"])
	require.Equal(t, "revision", app.Payload.Attrs["revision"])
	require.Equal(t, "node", app.Payload.Attrs["source"])
	require.Equal(t, 2, reads)
	base = "o*"
	err = p.Check(context.Background(), execution.Requirements{Base: &base}, "node")
	var system *jobdb.SystemError
	require.ErrorAs(t, err, &system)
	require.Equal(t, CannotVerify, system.Payload.Code)
	require.False(t, system.Payload.Retryable)
	c.ExpiresAt = now
	require.Error(t, p.Check(context.Background(), execution.Requirements{}, "unconstrained"))
	p.Read = nil
	require.Error(t, p.Check(context.Background(), execution.Requirements{}, "missing"))
	p.Read = func(context.Context) (jobdb.ConfigurationSnapshot, error) {
		return jobdb.ConfigurationSnapshot{}, jobdb.ErrExecutionLeaseLost
	}
	require.ErrorIs(t, p.Check(context.Background(), execution.Requirements{}, "lost"), jobdb.ErrExecutionLeaseLost)
	p.Read = func(context.Context) (jobdb.ConfigurationSnapshot, error) {
		return jobdb.ConfigurationSnapshot{}, errors.New("credentials must not appear")
	}
	err = p.Check(context.Background(), execution.Requirements{}, "failed")
	require.NotContains(t, err.Error(), "credentials must not appear")
	p.Read = func(context.Context) (jobdb.ConfigurationSnapshot, error) { return jobdb.ConfigurationSnapshot{}, nil }
	p.Required = false
	v, err := p.Discover(context.Background())
	require.NoError(t, err)
	require.Equal(t, "not_configured", v.Status)
	require.Nil(t, v.Catalog)
	p.Required = true
	_, err = p.Discover(context.Background())
	require.Error(t, err)
	p.Required = false
	p.Read = nil
	require.NoError(t, p.Check(context.Background(), execution.Requirements{}, "zero"))
}
func TestExecutionBaseCatalogEnvironment(t *testing.T) {
	for _, v := range []string{"", "false", "true", "0", "1", "invalid"} {
		t.Run(v, func(t *testing.T) {
			t.Setenv(RequireEnv, v)
			required, err := RequiredFromEnv()
			if v == "invalid" {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				require.Equal(t, v == "true" || v == "1", required)
			}
		})
	}
}
