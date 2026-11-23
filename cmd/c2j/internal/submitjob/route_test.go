package submitjob

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/colony-2/c2j/cmd/c2j/internal/swfruntime"
	"github.com/colony-2/c2j/pkg/jobcontext"
	"github.com/stretchr/testify/require"
)

func clearSubmissionEnv(t *testing.T) {
	t.Helper()
	for _, e := range os.Environ() {
		k, _, _ := strings.Cut(e, "=")
		if strings.HasPrefix(k, "C2J_CURRENT_") || strings.HasPrefix(k, "C2J_CHILD_JOB_") || k == "C2J_JOBDB" || k == "C2J_TENANT_ID" {
			t.Setenv(k, "")
		}
	}
}
func setRouteContext(t *testing.T) {
	t.Helper()
	for k, v := range jobcontext.EnvForCurrent(jobcontext.Current{TenantID: "tenant", JobID: "parent", GoalID: "goal", InvocationSequence: 7, InvocationHash: "hash"}) {
		t.Setenv(k, v)
	}
	for k, v := range jobcontext.EnvForChildJobBroker(jobcontext.ChildJobBroker{Endpoint: "https://worker.test:443/v1/child-jobs", Token: "token", SessionID: "session"}) {
		t.Setenv(k, v)
	}
}
func TestSubmissionRoute(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*testing.T, *Options)
		want string
	}{
		{"broker", func(t *testing.T, o *Options) {}, ""},
		{"legacy", func(t *testing.T, o *Options) { t.Setenv(jobcontext.CurrentContextVersionEnv, "") }, ""},
		{"optional-direct", func(t *testing.T, o *Options) { o.JobDBURI = "http://direct.test/tenant" }, ""},
		{"configured-run", func(t *testing.T, o *Options) { o.JobDBURI = "http://direct.test/tenant"; o.RunAfterSubmit = true }, ""},
		{"no-backend", func(t *testing.T, o *Options) { clearSubmissionEnv(t) }, "--jobdb is required"},
		{"parent-only", func(t *testing.T, o *Options) {
			t.Setenv(jobcontext.ChildJobEndpointEnv, "")
			t.Setenv(jobcontext.ChildJobTokenEnv, "")
			t.Setenv(jobcontext.ChildJobSessionIDEnv, "")
		}, "current job context requires a child job broker"},
		{"tenant-convenience", func(t *testing.T, o *Options) { clearSubmissionEnv(t); t.Setenv(jobcontext.TenantIDEnv, "tenant") }, "--jobdb is required"},
		{"partial-parent", func(t *testing.T, o *Options) { t.Setenv(jobcontext.CurrentJobIDEnv, "") }, "required"},
		{"bad-sequence", func(t *testing.T, o *Options) { t.Setenv(jobcontext.CurrentInvocationSequenceEnv, "seven") }, "integer"},
		{"partial-broker", func(t *testing.T, o *Options) { t.Setenv(jobcontext.ChildJobSessionIDEnv, "") }, "required"},
		{"bad-version", func(t *testing.T, o *Options) { t.Setenv(jobcontext.CurrentContextVersionEnv, "2") }, "version"},
		{"version-only", func(t *testing.T, o *Options) {
			clearSubmissionEnv(t)
			t.Setenv(jobcontext.CurrentContextVersionEnv, "1")
			o.JobDBURI = "embed:///"
		}, "identity"},
		{"broker-no-parent", func(t *testing.T, o *Options) {
			clearSubmissionEnv(t)
			t.Setenv(jobcontext.ChildJobEndpointEnv, "http://worker.test/child")
			t.Setenv(jobcontext.ChildJobTokenEnv, "token")
			t.Setenv(jobcontext.ChildJobSessionIDEnv, "session")
			o.JobDBURI = "embed:///"
		}, "requires current job"},
		{"invalid-direct", func(t *testing.T, o *Options) { o.JobDBURI = "ftp://direct.test/tenant" }, "scheme"},
		{"tenant-mismatch", func(t *testing.T, o *Options) { o.JobDBURI = "https://direct.test/other" }, "current tenant"},
		{"embedded-mismatch", func(t *testing.T, o *Options) { o.JobDBURI = "embed:///" }, "current tenant"},
		{"broker-only-run", func(t *testing.T, o *Options) { o.RunAfterSubmit = true }, "--run requires direct"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clearSubmissionEnv(t)
			setRouteContext(t)
			o := Options{WorkingDir: t.TempDir()}
			tc.edit(t, &o)
			err := o.Complete(context.Background())
			if err == nil {
				err = o.Validate()
			}
			if tc.want != "" {
				require.ErrorContains(t, err, tc.want)
				return
			}
			require.NoError(t, err)
			require.Equal(t, "tenant", o.TenantID)
			require.True(t, o.route.hasBroker)
			require.EqualValues(t, 7, o.route.parent.InvocationSequence)
			called := false
			handle, err := o.route.openRuntime(context.Background(), o.SWFURL, func(context.Context, string) (*swfruntime.Handle, error) { called = true; return nil, nil })
			require.NoError(t, err)
			require.Nil(t, handle)
			require.False(t, called)
			if o.JobDBURI == "" {
				require.Empty(t, o.SWFURL)
			}
		})
	}
}
func TestSubmissionBrokerEndpoint(t *testing.T) {
	for _, endpoint := range []string{"http://worker.test/path", "https://[::1]:443/v1/child-jobs"} {
		require.NoError(t, validateBrokerEndpoint(endpoint))
	}
	for _, endpoint := range []string{"", "/path", "ftp://worker.test/path", "http:///path", "http://user:secret@worker.test/path", "http://worker.test/path?q=1", "http://worker.test/path?", "http://worker.test/path#fragment", "http://worker.test/path#", "http://worker.test:bad/path", "http://worker.test:65536/path", "http://worker.test:0/path", "http://worker.test:/path"} {
		t.Run(endpoint, func(t *testing.T) { require.Error(t, validateBrokerEndpoint(endpoint)) })
	}
}
func TestSubmissionDirectRuntime(t *testing.T) {
	r := &submissionRoute{}
	called := false
	_, err := r.openRuntime(context.Background(), "embed:///", func(_ context.Context, u string) (*swfruntime.Handle, error) {
		called = true
		require.Equal(t, "embed:///", u)
		return nil, nil
	})
	require.NoError(t, err)
	require.True(t, called)
}

func TestSubmissionRouteDirectPrecedence(t *testing.T) {
	for _, tc := range []struct{ name, flag, env, config, want string }{
		{"flag-shields-invalid-environment-and-project", "http://flag.test/tenant", "invalid", "[invalid", "http://flag.test"},
		{"environment-shields-invalid-project", "", "http://environment.test/tenant", "[invalid", "http://environment.test"},
		{"project", "", "", "jobdb: https://project.test/tenant\n", "https://project.test"},
		{"invalid-environment", "", "invalid", "jobdb: https://project.test/tenant\n", ""},
		{"invalid-project", "", "", "[invalid", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clearSubmissionEnv(t)
			setRouteContext(t)
			root := t.TempDir()
			require.NoError(t, os.MkdirAll(filepath.Join(root, ".c2"), 0700))
			require.NoError(t, os.WriteFile(filepath.Join(root, ".c2/config.yaml"), []byte(tc.config), 0600))
			t.Setenv("C2J_JOBDB", tc.env)
			o := Options{WorkingDir: root, JobDBURI: tc.flag}
			err := o.Complete(context.Background())
			if tc.want == "" {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				require.NoError(t, o.Validate())
				require.Equal(t, tc.want, o.SWFURL)
				require.Equal(t, "tenant", o.TenantID)
				require.True(t, o.route.hasBroker)
			}
		})
	}
}
