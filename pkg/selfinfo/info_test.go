package selfinfo

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/colony-2/c2j/pkg/permissions"
	"github.com/colony-2/c2j/pkg/secrets"
	"github.com/stretchr/testify/require"
)

func TestStandaloneAndDeniedAreDistinct(t *testing.T) {
	i := Standalone(true)
	require.Equal(t, "absent", i.Execution.Status)
	require.Nil(t, i.Execution.TaskID)
	require.Equal(t, "not_applicable", i.Permissions.Status)
	require.Equal(t, "unknown", i.Capabilities["jobs.submit"].Permission)
	require.Equal(t, "configured_jobdb", Value(i.Capabilities["jobs.submit"].Route))
	require.Equal(t, "unavailable", i.Capabilities["jobs.submit_from_workspace"].Availability)
	i = Unknown("test")
	i.Lease.Status = "active"
	i.Permissions.JobDB = &permissions.JobDB{}
	i.Permissions.Status = "known"
	i.SetCapabilities(true, true, true)
	require.Equal(t, "denied", i.Capabilities["jobs.submit"].Permission)
	require.Equal(t, "unavailable", i.Capabilities["secrets.resolve"].Availability)
	raw, err := json.Marshal(i.Permissions)
	require.NoError(t, err)
	require.Contains(t, string(raw), `"submit":false`)
	require.Contains(t, string(raw), `"resolve":[]`)
	i.Permissions.JobDB.Jobs.Submit = true
	i.Lease.Status = "lost"
	i.SetCapabilities(true, true, true)
	require.Equal(t, "granted", i.Capabilities["jobs.submit"].Permission)
	require.Equal(t, "unavailable", i.Capabilities["jobs.submit"].Availability)
}
func TestSafeMetadata(t *testing.T) {
	r := secrets.NewRedactor()
	require.NoError(t, r.Register("private-value"))
	data := map[string]any{"repo": "git+https://user:password@example.test/repo?access_token=private#secret", "text": "url https://user:password@example.test/path?q=private-value and private-value", "number": int64(9007199254740993)}
	raw, err := MarshalSafe(data, r.Redact)
	require.NoError(t, err)
	for _, secret := range []string{"user:", "password", "access_token", "private-value", "#secret"} {
		require.NotContains(t, string(raw), secret)
	}
	require.Contains(t, string(raw), "9007199254740993")
	require.True(t, strings.Contains(string(raw), "git+https://example.test/repo"))
}

func TestCellCreationDiscoveryIsIndependentOfSubmission(t *testing.T) {
	i := Unknown("test")
	i.Lease.Status = "active"
	i.Permissions.JobDB = &permissions.JobDB{Cells: permissions.CellPermissions{Create: true, CellNames: []string{"reports"}}}
	i.Permissions.ChildCeiling = Ceiling{Status: "known", JobDB: &permissions.JobDB{}}
	i.SetCapabilities(false, false, false, true)
	require.Equal(t, "denied", i.Capabilities["jobs.submit"].Permission)
	require.Equal(t, "granted", i.Capabilities["cells.create"].Permission)
	require.Equal(t, "ready", i.Capabilities["cells.create"].Availability)
	require.Equal(t, []string{"reports"}, i.Capabilities["cells.create"].Names)
	require.Empty(t, i.Capabilities["cells.create"].Restrictions)
	raw, err := json.Marshal(i.Permissions)
	require.NoError(t, err)
	require.Contains(t, string(raw), `"cells":{"cellNames":["reports"],"create":true}`)
	i.Lease.Status = "lost"
	i.SetCapabilities(false, false, false, true)
	require.Equal(t, "unavailable", i.Capabilities["cells.create"].Availability)
}
