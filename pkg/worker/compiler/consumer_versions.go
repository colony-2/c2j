package compiler

import (
	"maps"
	"runtime/debug"

	"github.com/colony-2/c2j/internal/buildinfo"
)

// ExecutionConsumerVersions supplies provenance through JobDB's execution
// headers and activation records. Embedders may add their own versions or
// override c2j's build label. Their host revision must not be attributed to c2j.
func ExecutionConsumerVersions(overrides map[string]string) map[string]string {
	info, _ := debug.ReadBuildInfo()
	return executionConsumerVersions(info, buildinfo.Current(), overrides)
}

func executionConsumerVersions(info *debug.BuildInfo, current buildinfo.Info, overrides map[string]string) map[string]string {
	version := "dev"
	commit := ""
	if info != nil {
		if info.Main.Path == "github.com/colony-2/c2j" {
			version = current.Version
			commit = current.Commit
		} else {
			for _, dep := range info.Deps {
				if dep.Path == "github.com/colony-2/c2j" {
					if dep.Replace != nil {
						dep = dep.Replace
					}
					if dep.Version != "" {
						version = dep.Version
					}
				}
			}
		}
	}
	versions := map[string]string{"c2j": version}
	if commit != "" {
		versions["c2j.commit"] = commit
	}
	maps.Copy(versions, overrides)
	return versions
}
