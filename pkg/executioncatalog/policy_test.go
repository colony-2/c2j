package executioncatalog

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/colony-2/c2j/internal/executiontest"
	"github.com/colony-2/c2j/pkg/execution"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestExecutionBaseCatalogFreshPolicy(t *testing.T) {
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	c := execution.BaseCatalog{Version: 1, Scope: execution.CatalogScope{Runtime: "https://jobdb", Tenant: "t"}, Complete: true, Offerings: executiontest.Offerings("macos-15-arm64"), Generation: "one", PublishedAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Hour)}
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
	c.Offerings = executiontest.Offerings()
	err := p.Check(context.Background(), execution.Requirements{Base: &base}, "node")
	var app *jobdb.AppError
	require.ErrorAs(t, err, &app)
	require.Equal(t, Unsupported, app.Payload.Attrs["code"])
	require.Equal(t, "revision", app.Payload.Attrs["revision"])
	require.Equal(t, "node", app.Payload.Attrs["source"])
	require.Equal(t, 2, reads)
	base = "o*"
	err = p.Check(context.Background(), execution.Requirements{Base: &base}, "node")
	require.ErrorAs(t, err, &app)
	require.Equal(t, Unsupported, app.Payload.Attrs["code"])
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

func TestExecutionBaseCatalogCompletePolicy(t *testing.T) {
	now := time.Now().UTC()
	c := execution.BaseCatalog{Version: 1, Scope: execution.CatalogScope{Runtime: "https://test", Tenant: "t"}, Complete: true, Offerings: executiontest.Offerings("linux"), Generation: "one", PublishedAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Hour)}
	c.Offerings[0].Resources.CPU = &execution.ResourceRule{State: "range", Min: policyPtr("500m"), Max: policyPtr("8")}
	c.Offerings[0].Resources.Memory = &execution.ResourceRule{State: "not_allowed"}
	reads := 0
	p := Policy{Required: true, Scope: c.Scope, Now: func() time.Time { return now }, Read: func(ctx context.Context) (jobdb.ConfigurationSnapshot, error) {
		reads++
		raw, _ := json.Marshal(c)
		return jobdb.ConfigurationSnapshot{Revision: "rev", Values: map[string]json.RawMessage{execution.BaseCatalogKey: raw}}, nil
	}}
	t.Run("P05_complete_refusal", func(t *testing.T) {
		err := p.Check(context.Background(), execution.Requirements{Base: policyPtr("linux"), Capabilities: []string{"special"}, Resources: execution.Resources{CPU: policyPtr("16"), Memory: policyPtr("1Gi")}}, "test-boundary")
		var app *jobdb.AppError
		require.ErrorAs(t, err, &app)
		require.Equal(t, UnsupportedRequirements, app.Payload.Attrs["code"])
		require.Equal(t, "rev", app.Payload.Attrs["revision"])
		require.Equal(t, "one", app.Payload.Attrs["generation"])
		require.Equal(t, "test-boundary", app.Payload.Attrs["source"])
		reasons := app.Payload.Attrs["reasons"].([]execution.CatalogReason)
		require.Len(t, reasons, 3)
		require.Equal(t, "default", reasons[0].Offering.Name)
	})
	t.Run("P04_disabled_without_reads_rejects_oci", func(t *testing.T) {
		before := reads
		p.Required = false
		require.NoError(t, p.Check(context.Background(), execution.Requirements{Base: policyPtr("linux")}, "test"))
		require.ErrorContains(t, p.Check(context.Background(), execution.Requirements{Base: policyPtr("oci:alpine")}, "test"), "OCI")
		require.Equal(t, before, reads)
		p.Required = true
	})
	t.Run("P02_earlier_deadline", func(t *testing.T) {
		copy := p
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		deadline, _ := ctx.Deadline()
		copy.Read = func(ctx context.Context) (jobdb.ConfigurationSnapshot, error) {
			actual, _ := ctx.Deadline()
			require.Equal(t, deadline, actual)
			return jobdb.ConfigurationSnapshot{}, nil
		}
		_, err := copy.Discover(ctx)
		require.Error(t, err)
	})
	t.Run("P03_cancellation_identity", func(t *testing.T) {
		copy := p
		copy.Read = func(ctx context.Context) (jobdb.ConfigurationSnapshot, error) {
			<-ctx.Done()
			return jobdb.ConfigurationSnapshot{}, ctx.Err()
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := copy.Discover(ctx)
		require.ErrorIs(t, err, context.Canceled)
		for _, identity := range []error{jobdb.ErrExecutionLeaseLost, jobdb.ErrJobCancelled} {
			copy.Read = func(context.Context) (jobdb.ConfigurationSnapshot, error) {
				return jobdb.ConfigurationSnapshot{}, identity
			}
			_, err = copy.Discover(context.Background())
			require.ErrorIs(t, err, identity)
		}
	})
	t.Run("P01_P06_scope_expiry_and_optional_absence", func(t *testing.T) {
		copy := p
		copy.Scope.Tenant = "other"
		_, err := copy.Discover(context.Background())
		require.Error(t, err)
		c.ExpiresAt = now
		v, err := p.Discover(context.Background())
		require.Error(t, err)
		require.Nil(t, v.Catalog)
		v, err = p.DiscoverForAdministration(context.Background())
		require.Error(t, err)
		require.Equal(t, "expired", v.Status)
		require.NotNil(t, v.Catalog)
		copy = p
		copy.Required = false
		copy.Read = func(context.Context) (jobdb.ConfigurationSnapshot, error) { return jobdb.ConfigurationSnapshot{}, nil }
		v, err = copy.Discover(context.Background())
		require.NoError(t, err)
		require.Equal(t, "not_configured", v.Status)
		copy.Required = true
		v, err = copy.Discover(context.Background())
		require.Error(t, err)
		require.Equal(t, "unavailable", v.Status)
	})
}
func policyPtr(s string) *string { return &s }
