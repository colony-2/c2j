package buildinfo

import (
	"runtime/debug"
	"testing"
)

func TestResolveFromBuildInfoPrefersExplicitReleaseVersion(t *testing.T) {
	info := ResolveFromBuildInfo(Settings{
		Version: "1.2.3",
		Commit:  "release-commit",
		Date:    "2026-06-09T01:02:03Z",
	}, &debug.BuildInfo{
		Main: debug.Module{Version: "v0.0.0-dev"},
		Settings: []debug.BuildSetting{
			{Key: "vcs.revision", Value: "abcdef1234567890"},
			{Key: "vcs.time", Value: "2026-01-01T00:00:00Z"},
			{Key: "vcs.modified", Value: "true"},
		},
	}, true)

	if info.Version != "1.2.3" {
		t.Fatalf("Version = %q, want release version", info.Version)
	}
	if info.Commit != "release-commit" {
		t.Fatalf("Commit = %q, want explicit commit", info.Commit)
	}
	if info.Date != "2026-06-09T01:02:03Z" {
		t.Fatalf("Date = %q, want explicit date", info.Date)
	}
	if !info.ModifiedKnown || !info.Modified {
		t.Fatalf("Modified = %v/%v, want known true", info.Modified, info.ModifiedKnown)
	}
}

func TestResolveFromBuildInfoUsesModuleVersionForDevBuild(t *testing.T) {
	info := ResolveFromBuildInfo(Settings{Version: "dev"}, &debug.BuildInfo{
		Main: debug.Module{Version: "v0.0.26-0.20260609022330-fcc529947af4+dirty"},
		Settings: []debug.BuildSetting{
			{Key: "vcs.revision", Value: "fcc529947af4c6b1eb63dd0f54b1dca66241a204"},
			{Key: "vcs.time", Value: "2026-06-09T02:23:30Z"},
			{Key: "vcs.modified", Value: "true"},
		},
	}, true)

	if info.Version != "v0.0.26-0.20260609022330-fcc529947af4+dirty" {
		t.Fatalf("Version = %q, want module version", info.Version)
	}
	if info.Commit != "fcc529947af4c6b1eb63dd0f54b1dca66241a204" {
		t.Fatalf("Commit = %q, want VCS revision", info.Commit)
	}
	if info.Date != "2026-06-09T02:23:30Z" {
		t.Fatalf("Date = %q, want VCS time", info.Date)
	}
	if !info.ModifiedKnown || !info.Modified {
		t.Fatalf("Modified = %v/%v, want known true", info.Modified, info.ModifiedKnown)
	}
}

func TestResolveFromBuildInfoUsesShortCommitWhenModuleVersionUnavailable(t *testing.T) {
	info := ResolveFromBuildInfo(Settings{Version: "dev"}, &debug.BuildInfo{
		Main: debug.Module{Version: "(devel)"},
		Settings: []debug.BuildSetting{
			{Key: "vcs.revision", Value: "abcdef1234567890"},
			{Key: "vcs.modified", Value: "true"},
		},
	}, true)

	if info.Version != "dev-abcdef1-dirty" {
		t.Fatalf("Version = %q, want short commit fallback", info.Version)
	}
	if info.Commit != "abcdef1234567890" {
		t.Fatalf("Commit = %q, want full VCS revision", info.Commit)
	}
}

func TestResolveFromBuildInfoDoesNotAddDirtySuffixWhenClean(t *testing.T) {
	info := ResolveFromBuildInfo(Settings{Version: "dev"}, &debug.BuildInfo{
		Main: debug.Module{Version: "(devel)"},
		Settings: []debug.BuildSetting{
			{Key: "vcs.revision", Value: "abcdef1234567890"},
			{Key: "vcs.modified", Value: "false"},
		},
	}, true)

	if info.Version != "dev-abcdef1" {
		t.Fatalf("Version = %q, want clean short commit fallback", info.Version)
	}
	if info.ModifiedKnown && info.Modified {
		t.Fatal("Modified = true, want false")
	}
}

func TestResolveFromBuildInfoFallsBackToDevWithoutBuildInfo(t *testing.T) {
	info := ResolveFromBuildInfo(Settings{}, nil, false)

	if info.Version != "dev" {
		t.Fatalf("Version = %q, want dev", info.Version)
	}
	if info.ModifiedKnown {
		t.Fatal("ModifiedKnown = true, want false")
	}
}
