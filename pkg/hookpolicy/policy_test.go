package hookpolicy

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/colony-2/c2j/pkg/permissions"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
)

func TestPolicyInheritanceAndEdits(t *testing.T) {
	hook := ".c2/hooks/check.yaml"
	repo := "https://example.com/service"
	s := Snapshot{Repository: repo}
	d, err := Edit(s, false, hook, "add", []string{"A", "B"}, true, Trust{Mode: "candidate"}, false, nil)
	require.NoError(t, err)
	s.Tenant = &d
	c, err := Edit(s, true, hook, "remove", []string{"A"}, false, Trust{}, false, nil)
	require.NoError(t, err)
	s.Cell = &c
	e, source := s.Select(hook)
	require.Equal(t, "cell", source)
	require.Equal(t, []string{"B"}, e.Perms.JobDB.Secrets.Resolve)
	require.True(t, e.Perms.JobDB.Jobs.Submit)
	d, err = Edit(s, false, hook, "add", []string{"C"}, false, Trust{Mode: "candidate"}, false, nil)
	require.NoError(t, err)
	s.Tenant = &d
	e, _ = s.Select(hook)
	require.Equal(t, []string{"B"}, e.Perms.JobDB.Secrets.Resolve)
	c, err = Edit(s, true, hook, "remove", []string{"B"}, true, Trust{}, false, nil)
	require.NoError(t, err)
	s.Cell = &c
	e, source = s.Select(hook)
	require.Equal(t, "cell", source)
	require.Empty(t, e.Perms.JobDB.Secrets.Resolve)
	require.False(t, e.Perms.JobDB.Jobs.Submit)
	c, err = Edit(s, true, hook, "inherit", nil, false, Trust{}, false, nil)
	require.NoError(t, err)
	s.Cell = &c
	e, source = s.Select(hook)
	require.Equal(t, "tenant", source)
	require.Len(t, e.Perms.JobDB.Secrets.Resolve, 3)
	e, source = s.Select("other.yaml")
	require.Equal(t, "none", source)
	require.Empty(t, e.Perms.JobDB.Secrets.Resolve)
	require.False(t, e.Perms.JobDB.Jobs.Submit)
	_, err = Edit(s, true, hook, "add", []string{"A"}, false, Trust{Mode: "commit", Commit: strings.Repeat("a", 40)}, false, nil)
	require.ErrorContains(t, err, "--replace")
}

func TestPolicyValidationAndAuthority(t *testing.T) {
	for _, raw := range []string{`{"perms":{"jobdb":{"jobs":{"submit":null}}}}`, `{"perms":{"jobdb":{"config":{"read":true}}}}`, `{"perms":{},"perms":{}}`, `{"perms":{"jobdb":{"secrets":{"resolve":["A"]}}}}`, `{"perms":{"jobdb":{"jobs":{"submit":true}}}}`} {
		var e Entry
		err := Decode([]byte(raw), &e)
		if err == nil {
			err = e.Validate()
		}
		require.Error(t, err, raw)
	}
	d, err := Edit(Snapshot{}, false, "hook.yaml", "add", []string{"A"}, true, Trust{Mode: "commit", Commit: strings.Repeat("a", 40)}, false, nil)
	require.NoError(t, err)
	e := d.Hooks["hook.yaml"]
	_, err = e.Authority(strings.Repeat("b", 40), permissions.State{}, nil)
	require.ErrorContains(t, err, "not authorized")
	a, err := e.Authority(strings.Repeat("a", 40), permissions.State{}, nil)
	require.NoError(t, err)
	require.NoError(t, a.CheckInput("A"))
	require.Error(t, a.CheckInput("B"))
	require.True(t, a.Grant.Jobs.Submit)
	a, err = e.Authority(strings.Repeat("a", 40), permissions.State{Limit: &permissions.JobDB{}}, nil)
	require.NoError(t, err)
	require.Error(t, a.CheckInput("A"))
	require.False(t, a.Grant.Jobs.Submit)
	a, err = Empty().Authority(strings.Repeat("a", 40), permissions.State{}, nil)
	require.NoError(t, err)
	g, err := a.Resolve([]string{"git-credential"})
	require.NoError(t, err)
	require.Empty(t, g.Secrets.Resolve)
	require.False(t, g.Jobs.Submit)
}

func TestCaptureRejectsBrokenBindingsAndCopies(t *testing.T) {
	repo := "https://example.com/repo"
	d := Document{Version: 1, Repository: repo, Hooks: map[string]Entry{"hook.yaml": Empty()}}
	raw, err := json.Marshal(d)
	require.NoError(t, err)
	s, err := Capture(jobdb.ConfigurationSnapshot{Revision: "1", Values: map[string]json.RawMessage{CellKey(repo): raw}}, repo)
	require.NoError(t, err)
	e, _ := s.Select("hook.yaml")
	e.Perms.JobDB.Jobs.Submit = true
	e, _ = s.Select("hook.yaml")
	require.False(t, e.Perms.JobDB.Jobs.Submit)
	_, err = Capture(jobdb.ConfigurationSnapshot{Values: map[string]json.RawMessage{CellKey("https://other/repo"): raw}}, "https://other/repo")
	require.Error(t, err)
	_, err = Capture(jobdb.ConfigurationSnapshot{Values: map[string]json.RawMessage{TenantKey: []byte(`null`)}}, repo)
	require.Error(t, err)
}
