package compiler

import (
	"maps"
	"runtime/debug"
)

// ExecutionConsumerVersions supplies provenance through JobDB's execution
// header. Embedders may add their own versions or override c2j's build label.
func ExecutionConsumerVersions(overrides map[string]string) map[string]string {
	version := "dev"
	if info, ok := debug.ReadBuildInfo(); ok {
		if info.Main.Path == "github.com/colony-2/c2j" {
			if info.Main.Version != "" && info.Main.Version != "(devel)" {
				version = info.Main.Version
			} else {
				for _, s := range info.Settings {
					if s.Key == "vcs.revision" {
						version = s.Value
					}
				}
			}
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
	maps.Copy(versions, overrides)
	return versions
}
