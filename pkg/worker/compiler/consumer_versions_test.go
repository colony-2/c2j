package compiler

import (
	"runtime/debug"
	"testing"

	"github.com/colony-2/c2j/internal/buildinfo"
	"github.com/stretchr/testify/require"
)

func TestExecutionConsumerVersions(t *testing.T) {
	const module = "github.com/colony-2/c2j"
	for _, tc := range []struct {
		name     string
		info     *debug.BuildInfo
		settings buildinfo.Settings
		want     map[string]string
	}{
		{
			name:     "release without VCS stamping",
			info:     &debug.BuildInfo{Main: debug.Module{Path: module, Version: "(devel)"}},
			settings: buildinfo.Settings{Version: "1.2.3", Commit: "abcdef1234567890"},
			want:     map[string]string{"c2j": "1.2.3", "c2j.commit": "abcdef1234567890"},
		},
		{
			name: "checkout",
			info: &debug.BuildInfo{Main: debug.Module{Path: module, Version: "(devel)"}, Settings: []debug.BuildSetting{{Key: "vcs.revision", Value: "abcdef1234567890"}}},
			want: map[string]string{"c2j": "dev-abcdef1", "c2j.commit": "abcdef1234567890"},
		},
		{
			name: "go install",
			info: &debug.BuildInfo{Main: debug.Module{Path: module, Version: "v1.2.3"}},
			want: map[string]string{"c2j": "v1.2.3"},
		},
		{
			name: "embedded dependency uses c2j version, not host version or revision",
			info: &debug.BuildInfo{Main: debug.Module{Path: "example.test/host", Version: "v9.0.0"}, Deps: []*debug.Module{{Path: module, Version: "v1.2.3"}}, Settings: []debug.BuildSetting{{Key: "vcs.revision", Value: "host-commit"}}},
			want: map[string]string{"c2j": "v1.2.3"},
		},
		{
			name: "replaced dependency",
			info: &debug.BuildInfo{Main: debug.Module{Path: "example.test/host"}, Deps: []*debug.Module{{Path: module, Version: "v1.2.3", Replace: &debug.Module{Version: "v1.2.4"}}}},
			want: map[string]string{"c2j": "v1.2.4"},
		},
		{
			name: "local dependency has no known revision",
			info: &debug.BuildInfo{Main: debug.Module{Path: "example.test/host"}, Deps: []*debug.Module{{Path: module, Version: "v1.2.3", Replace: &debug.Module{Path: "../c2j"}}}},
			want: map[string]string{"c2j": "dev"},
		},
		{name: "unknown build", want: map[string]string{"c2j": "dev"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			current := buildinfo.ResolveFromBuildInfo(tc.settings, tc.info, tc.info != nil)
			require.Equal(t, tc.want, executionConsumerVersions(tc.info, current, nil))
			overrides := map[string]string{"c2j": "custom", "host": "v2"}
			got := executionConsumerVersions(tc.info, current, overrides)
			require.Equal(t, "custom", got["c2j"])
			require.Equal(t, "v2", got["host"])
			got["host"] = "changed"
			require.Equal(t, "v2", overrides["host"])
		})
	}
}
