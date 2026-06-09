package buildinfo

import (
	"runtime/debug"
	"strconv"
	"strings"
)

const (
	defaultVersion = "dev"
)

type Settings struct {
	Version string
	Commit  string
	Date    string
}

type Info struct {
	Version       string
	Commit        string
	Date          string
	Modified      bool
	ModifiedKnown bool
}

func Resolve(settings Settings) Info {
	bi, ok := debug.ReadBuildInfo()
	return ResolveFromBuildInfo(settings, bi, ok)
}

func ResolveFromBuildInfo(settings Settings, bi *debug.BuildInfo, ok bool) Info {
	version := clean(settings.Version)
	if version == "" {
		version = defaultVersion
	}
	commit := clean(settings.Commit)
	date := clean(settings.Date)

	var (
		mainVersion   string
		modified      bool
		modifiedKnown bool
	)
	if ok && bi != nil {
		mainVersion = clean(bi.Main.Version)
		for _, setting := range bi.Settings {
			switch setting.Key {
			case "vcs.revision":
				if commit == "" {
					commit = clean(setting.Value)
				}
			case "vcs.time":
				if date == "" {
					date = clean(setting.Value)
				}
			case "vcs.modified":
				if parsed, err := strconv.ParseBool(clean(setting.Value)); err == nil {
					modified = parsed
					modifiedKnown = true
				}
			}
		}
	}

	if isDefaultVersion(version) {
		switch {
		case isModuleVersion(mainVersion):
			version = mainVersion
		case commit != "":
			version = defaultVersion + "-" + shortCommit(commit)
			if modified {
				version += "-dirty"
			}
		}
	}

	return Info{
		Version:       valueOrDefault(version, defaultVersion),
		Commit:        commit,
		Date:          date,
		Modified:      modified,
		ModifiedKnown: modifiedKnown,
	}
}

func clean(value string) string {
	return strings.TrimSpace(value)
}

func isDefaultVersion(version string) bool {
	return version == "" || version == defaultVersion
}

func isModuleVersion(version string) bool {
	return version != "" && version != "(devel)"
}

func shortCommit(commit string) string {
	commit = clean(commit)
	if len(commit) <= 7 {
		return commit
	}
	return commit[:7]
}

func valueOrDefault(value string, fallback string) string {
	value = clean(value)
	if value == "" {
		return fallback
	}
	return value
}
