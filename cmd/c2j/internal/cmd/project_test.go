package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/colony-2/c2j/internal/buildinfo"
	"github.com/colony-2/c2j/pkg/jobcontext"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/colony-2/jobdb/pkg/jobdb/celltest"
	"github.com/colony-2/jobdb/pkg/jobdb/projectcatalog"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/remote"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/toy"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func runProjectCLI(args ...string) (string, string, error) {
	root := newRootCmd(buildinfo.Info{})
	var stdout, stderr bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetArgs(args)
	err := root.Execute()
	return stdout.String(), stderr.String(), err
}

func isolateProjectCLI(t *testing.T) {
	t.Helper()
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(name, "C2J_CURRENT_") || strings.HasPrefix(name, "C2J_CHILD_JOB_") {
			t.Setenv(name, "")
		}
	}
	t.Setenv("C2J_JOBDB", "")
	t.Setenv("JOBDB_BEARER_TOKEN", "")
	t.Setenv("HOME", t.TempDir())
	t.Chdir(t.TempDir())
}

func TestProjectCommandRouting(t *testing.T) {
	isolateProjectCLI(t)
	help, _, err := runProjectCLI("project", "--help")
	require.NoError(t, err)
	require.Contains(t, help, "  create ")
	require.Contains(t, help, "  list ")
	root := newRootCmd(buildinfo.Info{})
	group, _, err := root.Find([]string{"project"})
	require.NoError(t, err)
	require.Len(t, group.Commands(), 2)
	legacyList, remaining, err := root.Find([]string{"list", "project"})
	require.NoError(t, err)
	require.Equal(t, "c2j list", legacyList.CommandPath())
	require.Equal(t, []string{"project"}, remaining)
	for _, verb := range []string{"create", "list"} {
		help, _, err := runProjectCLI("project", verb, "--help")
		require.NoError(t, err)
		require.Contains(t, help, "c2j project "+verb)
	}
	for _, args := range [][]string{{"list", "--job-type", "example", "--help"}, {"list", "children", "--jobdb", "embed:///", "--help"}} {
		help, _, err := runProjectCLI(args...)
		require.NoError(t, err)
		require.Contains(t, help, "c2j "+strings.Join(args[:len(args)-3], " "))
	}
	for _, example := range []struct {
		args []string
		want string
	}{
		{[]string{"create", "project"}, "unknown command"},
		{[]string{"project", "create", "--embed"}, "required flag(s)"},
		{[]string{"project", "create", "--name", "sample", "--embed", "--metadata", "n=1", "--metadata", "n=2"}, "duplicate --metadata"},
		{[]string{"project", "create", "--name", "sample", "--embed", "--metadata", "n=invalid"}, "KEY=JSON_VALUE"},
		{[]string{"project", "list", "--embed", "--jobdb", "https://jobdb.example"}, "none of the others"},
	} {
		_, _, err := runProjectCLI(example.args...)
		require.ErrorContains(t, err, example.want, example.args)
	}
	_, err = os.Stat(filepath.Join(os.Getenv("HOME"), ".c2"))
	require.ErrorIs(t, err, os.ErrNotExist)
}

func testProjectHandler(t *testing.T) (http.Handler, *projectcatalog.Service) {
	t.Helper()
	catalog := &projectcatalog.Service{Store: &projectcatalog.MemoryStore{}}
	require.NoError(t, catalog.Install(projectTestAdmin(context.Background()), projectcatalog.Installation{
		Manifest: projectcatalog.InstallationDigest([]byte("disposable project CLI test; no external participants")),
	}))
	return remote.NewConsumerServer(toy.New(toy.WithProjects(catalog))), catalog
}

func projectTestAdmin(ctx context.Context) context.Context {
	return jobdb.WithProjectAdministration(celltest.Context(ctx), func(context.Context) error { return nil })
}

func TestProjectRemoteCatalog(t *testing.T) {
	isolateProjectCLI(t)
	t.Setenv("JOBDB_BEARER_TOKEN", "test-operator")
	handler, catalog := testProjectHandler(t)
	var creates atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer test-operator" {
			t.Error("existing bearer credential was not forwarded")
			writer.WriteHeader(http.StatusUnauthorized)
			return
		}
		request = request.WithContext(projectTestAdmin(request.Context()))
		if request.URL.Path == "/v1/projects" && request.Method == http.MethodPost && creates.Add(1) == 1 {
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, request)
			if recorder.Code != http.StatusCreated {
				t.Errorf("fixture failed to commit: %s", recorder.Body.String())
			}
			conn, _, err := writer.(http.Hijacker).Hijack()
			if err != nil {
				t.Error(err)
				return
			}
			_ = conn.Close()
			return
		}
		handler.ServeHTTP(writer, request)
	}))
	defer server.Close()
	createArgs := []string{"project", "create", "--name", "reports", "--description", "Reporting", "--metadata", `n=9007199254740993123456789`, "--metadata", `options=[true,"a,b=c"]`, "--json", "--jobdb", server.URL}
	stdout, stderr, err := runProjectCLI(createArgs...)
	require.Error(t, err)
	require.Empty(t, stdout)
	require.True(t, strings.HasPrefix(stderr, "Project request ID: "), stderr)
	id := strings.TrimSpace(strings.TrimPrefix(stderr, "Project request ID: "))
	_, parseErr := uuid.Parse(id)
	require.NoError(t, parseErr)
	require.ErrorContains(t, err, `--request-id "`+id+`" with identical input`)
	require.EqualValues(t, 1, creates.Load())
	stored, err := catalog.ListProjects(projectTestAdmin(context.Background()), jobdb.ListProjectsRequest{})
	require.NoError(t, err)
	require.Len(t, stored.Projects, 1)

	createArgs = append(createArgs, "--request-id", id)
	stdout, stderr, err = runProjectCLI(createArgs...)
	require.NoError(t, err)
	require.Empty(t, stderr)
	var receipt jobdb.ProjectCreation
	require.NoError(t, json.Unmarshal([]byte(stdout), &receipt))
	require.Equal(t, stored.Projects[0].ID, receipt.Project.ID)
	require.Equal(t, http.StatusCreated, receipt.Status)
	require.Contains(t, string(receipt.Project.Metadata), "9007199254740993123456789")
	require.Contains(t, string(receipt.Project.Metadata), `[true,"a,b=c"]`)
	replay, _, err := runProjectCLI(createArgs...)
	require.NoError(t, err)
	require.Equal(t, stdout, replay)
	_, _, err = runProjectCLI(append(createArgs, "--description", "different")...)
	require.ErrorIs(t, err, jobdb.ErrProjectConflict)

	var second jobdb.ProjectCreation
	stdout, _, err = runProjectCLI("project", "create", "--name", "reports-other", "--request-id", "other", "--jobdb", server.URL+"/old-scope", "--json")
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal([]byte(stdout), &second))
	t.Setenv("C2J_JOBDB", server.URL+"/old-scope")
	list := func(extra ...string) jobdb.ListProjectsResponse {
		stdout, stderr, err := runProjectCLI(append([]string{"project", "list", "--json"}, extra...)...)
		require.NoError(t, err)
		require.Empty(t, stderr)
		var page jobdb.ListProjectsResponse
		require.NoError(t, json.Unmarshal([]byte(stdout), &page))
		return page
	}
	firstPage := list("--page-size", "1")
	require.Len(t, firstPage.Projects, 1)
	require.NotEmpty(t, firstPage.NextPageToken)
	lastPage := list("--page-size", "1", "--page-token", firstPage.NextPageToken)
	require.Len(t, lastPage.Projects, 1)
	require.Empty(t, lastPage.NextPageToken)
	require.ElementsMatch(t, []string{receipt.Project.ID, second.Project.ID}, []string{firstPage.Projects[0].ID, lastPage.Projects[0].ID})
	filtered := list("--name", "reports")
	require.Len(t, filtered.Projects, 1)
	require.Equal(t, receipt.Project.ID, filtered.Projects[0].ID)
	_, _, err = runProjectCLI("project", "list", "--page-size", "1", "--name", "reports", "--page-token", firstPage.NextPageToken)
	require.ErrorIs(t, err, jobdb.ErrInvalidProject)
	stdout, _, err = runProjectCLI("cell", "create", "review", "--repo", "https://example.com/review.git", "--jobdb", server.URL+"/"+receipt.Project.ID, "--json")
	require.NoError(t, err)
	var cell jobdb.Cell
	require.NoError(t, json.Unmarshal([]byte(stdout), &cell))
	require.Equal(t, receipt.Project.ID, cell.TenantID)
}

func TestProjectCatalogAuthorization(t *testing.T) {
	isolateProjectCLI(t)
	handler, _ := testProjectHandler(t)
	for _, test := range []struct {
		name, bearer string
		admin, lease bool
	}{
		{name: "missing administrator", bearer: "test-operator"},
		{name: "delegation", bearer: "jdb-m2.test-token", admin: true},
		{name: "lease header", bearer: "test-operator", admin: true, lease: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("JOBDB_BEARER_TOKEN", test.bearer)
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				if test.admin {
					request = request.WithContext(projectTestAdmin(request.Context()))
				}
				if test.lease {
					request.Header.Set("X-JobDB-Lease-Token", "test-lease")
				}
				handler.ServeHTTP(writer, request)
			}))
			defer server.Close()
			for _, args := range [][]string{{"project", "list"}, {"project", "create", "--name", "reports", "--request-id", "denied"}} {
				stdout, _, err := runProjectCLI(append(args, "--jobdb", server.URL, "--json")...)
				require.ErrorIs(t, err, jobdb.ErrProjectAccessDenied)
				require.Empty(t, stdout)
			}
		})
	}
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
	defer server.Close()
	t.Setenv("JOBDB_BEARER_TOKEN", "test-operator")
	for _, origin := range []struct {
		name string
		env  map[string]string
	}{
		{"running task", jobcontext.EnvForCurrent(jobcontext.Current{TenantID: "0", JobID: "job"})},
		{"task broker", map[string]string{jobcontext.ChildJobEndpointEnv: server.URL, jobcontext.ChildJobTokenEnv: "token", jobcontext.ChildJobSessionIDEnv: "session"}},
	} {
		t.Run(origin.name, func(t *testing.T) {
			for key, value := range origin.env {
				t.Setenv(key, value)
			}
			for _, args := range [][]string{{"project", "list"}, {"project", "create", "--name", "reports"}} {
				for _, target := range [][]string{{"--embed"}, {"--jobdb", server.URL}} {
					stdout, stderr, err := runProjectCLI(append(args, target...)...)
					require.ErrorContains(t, err, origin.name)
					require.Empty(t, stdout)
					require.Empty(t, stderr)
				}
			}
		})
	}
	require.Zero(t, calls.Load())
	_, err := os.Stat(filepath.Join(os.Getenv("HOME"), ".c2"))
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestProjectCatalogDoesNotFollowRedirects(t *testing.T) {
	isolateProjectCLI(t)
	var catalogCalls, fallbackCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/projects" {
			fallbackCalls.Add(1)
			writer.WriteHeader(http.StatusNotFound)
			return
		}
		catalogCalls.Add(1)
		http.Redirect(writer, request, "/other", http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	for _, args := range [][]string{{"project", "list"}, {"project", "create", "--name", "reports", "--request-id", "redirected"}} {
		stdout, _, err := runProjectCLI(append(args, "--jobdb", server.URL, "--json")...)
		require.ErrorIs(t, err, jobdb.ErrProjectUnavailable)
		require.Empty(t, stdout)
	}
	require.EqualValues(t, 2, catalogCalls.Load())
	require.Zero(t, fallbackCalls.Load())
}

func TestProjectMissingRemoteCatalog(t *testing.T) {
	isolateProjectCLI(t)
	for _, test := range []struct {
		verb, method string
		flags        []string
	}{
		{verb: "create", method: http.MethodPost, flags: []string{"--name", "reports", "--request-id", "missing-catalog"}},
		{verb: "list", method: http.MethodGet},
	} {
		t.Run(test.verb, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				calls.Add(1)
				if request.Method != test.method || request.URL.Path != "/v1/projects" {
					t.Errorf("unexpected request: %s %s", request.Method, request.URL.Path)
				}
				http.NotFound(writer, request)
			}))
			defer server.Close()
			stdout, _, err := runProjectCLI(append([]string{"project", test.verb, "--jobdb", server.URL, "--json"}, test.flags...)...)
			require.ErrorIs(t, err, jobdb.ErrProjectUnavailable)
			require.NotContains(t, err.Error(), jobdb.ErrProjectNotFound.Error())
			require.Empty(t, stdout)
			require.EqualValues(t, 1, calls.Load())
		})
	}
}

func TestProjectEmbeddedCLI(t *testing.T) {
	isolateProjectCLI(t)
	t.Setenv("C2J_JOBDB", "http://localhost:1")
	_, _, err := runProjectCLI("project", "list", "--embed")
	require.ErrorIs(t, err, jobdb.ErrProjectUnavailable)
	_, err = os.Stat(filepath.Join(os.Getenv("HOME"), ".c2"))
	require.ErrorIs(t, err, os.ErrNotExist)
	stdout, _, err := runProjectCLI("project", "create", "--embed", "--name", "reports", "--json", "--request-id", "stable")
	require.NoError(t, err)
	var creation jobdb.ProjectCreation
	require.NoError(t, json.Unmarshal([]byte(stdout), &creation))
	id := creation.Project.ID
	root := filepath.Join(os.Getenv("HOME"), ".c2", "embed", "default")
	entries, err := os.ReadDir(root)
	require.NoError(t, err)
	for _, entry := range entries {
		require.NotContains(t, entry.Name(), ".blobs")
		require.NotContains(t, entry.Name(), ".secrets")
	}
	stdout, _, err = runProjectCLI("project", "list", "--jobdb", "embed:///"+id, "--json")
	require.NoError(t, err)
	var listed jobdb.ListProjectsResponse
	require.NoError(t, json.Unmarshal([]byte(stdout), &listed))
	var ids []string
	for _, project := range listed.Projects {
		ids = append(ids, project.ID)
	}
	require.ElementsMatch(t, []string{"0", "c2", id}, ids)
	for _, scope := range []struct{ uri, id string }{{"embed:///" + id, id}, {"embed:///", "0"}} {
		stdout, _, err := runProjectCLI("cell", "create", "review", "--repo", "https://example.com/review.git", "--jobdb", scope.uri, "--json")
		require.NoError(t, err)
		var cell jobdb.Cell
		require.NoError(t, json.Unmarshal([]byte(stdout), &cell))
		require.Equal(t, scope.id, cell.TenantID)
	}
	_, _, err = runProjectCLI("cell", "create", "missing", "--repo", "https://example.com/missing.git", "--jobdb", "embed:///not-registered")
	require.ErrorIs(t, err, jobdb.ErrProjectNotFound)
}
