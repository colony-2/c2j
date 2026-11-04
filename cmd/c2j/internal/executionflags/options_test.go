package executionflags

import (
	"strings"
	"testing"

	"github.com/colony-2/c2j/pkg/execution"
	"github.com/spf13/pflag"
	"github.com/stretchr/testify/require"
)

func env(values map[string]string) func(string) (string, bool) {
	return func(key string) (string, bool) { value, ok := values[key]; return value, ok }
}

func parseArgs(t *testing.T, args ...string) Options {
	t.Helper()
	var o Options
	flags := pflag.NewFlagSet("execution-test", pflag.ContinueOnError)
	o.AddFlags(flags)
	require.NoError(t, flags.Parse(args))
	return o
}

func TestMixedSourcesAndPrecedence(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	o := parseArgs(t, "--execution-cpu=2", "--execution-image=alpine:3", "--execution-image-id=runtime://image")
	a, err := o.Parse(env(map[string]string{
		"C2J_EXECUTION_CPU":               "bad ignored value",
		"C2J_EXECUTION_MEMORY":            "1024Mi",
		"C2J_EXECUTION_EPHEMERAL_STORAGE": "10Gi",
		"C2J_EXECUTION_PLATFORM":          "LINUX/AMD64",
		"C2J_EXECUTION_IMAGE_DIGEST":      digest,
	}))
	require.NoError(t, err)
	require.Equal(t, execution.SchemaVersion, a.SchemaVersion)
	require.Equal(t, "2", *a.Resources.CPU)
	require.Equal(t, "1Gi", *a.Resources.Memory)
	require.Equal(t, "10Gi", *a.Resources.EphemeralStorage)
	require.Equal(t, "linux/amd64", *a.Platform)
	require.Equal(t, "docker.io/library/alpine:3", a.Image.Reference)
	require.Equal(t, digest, a.Image.ManifestDigest)
	require.Equal(t, "runtime://image", a.Image.ImageID)
	require.Nil(t, o.Memory, "environment resolution does not mutate reusable options")
}

func TestEveryArgumentOverridesItsEnvironment(t *testing.T) {
	var empty Options
	valid := []string{"macos-123-456", "1", "1Gi", "2Gi", "linux/amd64", "alpine:3", "sha256:" + strings.Repeat("a", 64), "runtime://image"}
	for i, b := range empty.bindings() {
		t.Run(b.flag, func(t *testing.T) {
			lookup := env(map[string]string{b.env: ""})
			_, err := parseArgs(t, "--"+b.flag+"="+valid[i]).Parse(lookup)
			require.NoError(t, err)
			_, err = parseArgs(t, "--"+b.flag+"=").Parse(env(map[string]string{b.env: valid[i]}))
			require.ErrorContains(t, err, "must not be empty")
			_, err = (Options{}).Parse(lookup)
			require.ErrorContains(t, err, b.env)
		})
	}
}

func TestUnknownAndInvalidAllocation(t *testing.T) {
	a, err := (Options{}).Parse(nil)
	require.NoError(t, err)
	require.False(t, a.HasCompatibilityFacts())
	require.Nil(t, a.Resources.CPU)
	_, err = parseArgs(t, "--execution-memory=4").Parse(nil)
	require.ErrorContains(t, err, "execution.resources.memory")
	_, err = (Options{}).Parse(env(map[string]string{"C2J_EXECUTION_CPU": "0"}))
	require.ErrorContains(t, err, "execution.resources.cpu")
}

func TestBaseFlagsAndEnvironment(t *testing.T) {
	a, err := (Options{}).Parse(env(map[string]string{"C2J_EXECUTION_BASE": "macos-123-456"}))
	require.NoError(t, err)
	require.Equal(t, "macos-123-456", a.Base)
	require.True(t, a.HasCompatibilityFacts())
	o := parseArgs(t, "--execution-base=ubuntu-26-arm64")
	require.True(t, o.HasArguments())
	a, err = o.Parse(env(map[string]string{"C2J_EXECUTION_BASE": "invalid ignored value"}))
	require.NoError(t, err)
	require.Equal(t, "ubuntu-26-arm64", a.Base)
	_, err = parseArgs(t, "--execution-base=macos*").Parse(nil)
	require.ErrorContains(t, err, "concrete")
	_, err = o.ParseFilter(false, false, nil)
	require.ErrorContains(t, err, "requires --compatible-with-execution")
	f, err := o.ParseFilter(true, false, nil)
	require.NoError(t, err)
	require.Equal(t, a.Base, f.Allocation.Base)
	var r execution.Requirements
	flags := pflag.NewFlagSet("requirements", pflag.ContinueOnError)
	AddRequirementFlags(flags, &r)
	require.NoError(t, flags.Parse([]string{"--require-base=macos*"}))
	r, err = r.Normalize()
	require.NoError(t, err)
	require.Equal(t, "macos*", *r.Base)
	a, err = (Options{}).Parse(nil)
	require.NoError(t, err)
	require.Empty(t, a.Base, "requested base never supplies allocation facts")
}

func TestListOptIn(t *testing.T) {
	panicLookup := func(string) (string, bool) {
		t.Fatal("list must not inspect allocation environment without opt-in")
		return "", false
	}
	f, err := (Options{}).ParseFilter(false, false, panicLookup)
	require.NoError(t, err)
	require.Nil(t, f)
	_, err = parseArgs(t, "--execution-cpu=1").ParseFilter(false, false, panicLookup)
	require.ErrorContains(t, err, "requires --compatible-with-execution")
	_, err = (Options{}).ParseFilter(false, true, panicLookup)
	require.ErrorContains(t, err, "requires --compatible-with-execution")
	_, err = (Options{}).ParseFilter(true, false, nil)
	require.ErrorContains(t, err, "at least one allocation fact")
	_, err = parseArgs(t, "--execution-image-id=runtime://id").ParseFilter(true, false, nil)
	require.ErrorContains(t, err, "diagnostic only")
	f, err = (Options{}).ParseFilter(true, true, env(map[string]string{"C2J_EXECUTION_MEMORY": "2Gi"}))
	require.NoError(t, err)
	require.True(t, f.IncludeUnresolved)
	require.Equal(t, "2Gi", *f.Allocation.Resources.Memory)
	_, err = (Options{}).ParseFilter(true, false, env(map[string]string{"C2J_EXECUTION_MEMORY": "invalid"}))
	require.Error(t, err)
}

func TestCapabilityFlagsAndEnvironment(t *testing.T) {
	lookup := env(map[string]string{"C2J_EXECUTION_CAPABILITIES": `["env.only","env.only"]`})
	o := parseArgs(t)
	require.False(t, o.HasArguments())
	a, err := o.Parse(lookup)
	require.NoError(t, err)
	require.Equal(t, []string{"env.only"}, a.Capabilities)
	require.Nil(t, o.Capabilities, "environment must not mutate reusable options")
	o = parseArgs(t, "--execution-capability=z.feature", "--execution-capability=a.feature", "--execution-capability=z.feature")
	require.True(t, o.HasArguments())
	a, err = o.Parse(lookup)
	require.NoError(t, err)
	require.Equal(t, []string{"a.feature", "z.feature"}, a.Capabilities)
	_, err = o.Parse(env(map[string]string{"C2J_EXECUTION_CAPABILITIES": "invalid ignored environment"}))
	require.NoError(t, err)
	_, err = o.ParseFilter(false, false, nil)
	require.ErrorContains(t, err, "requires --compatible-with-execution")
	f, err := o.ParseFilter(true, false, nil)
	require.NoError(t, err)
	require.Equal(t, a.Capabilities, f.Allocation.Capabilities)
	for _, value := range []string{"", "null", `{}`, `[true]`, `[12]`, `[null]`, `["Docker"]`} {
		_, err := (Options{}).Parse(env(map[string]string{"C2J_EXECUTION_CAPABILITIES": value}))
		require.Error(t, err, value)
	}
	a, err = (Options{}).Parse(env(map[string]string{"C2J_EXECUTION_CAPABILITIES": `[]`}))
	require.NoError(t, err)
	require.Empty(t, a.Capabilities)
	_, err = parseArgs(t, "--execution-capability=").Parse(lookup)
	require.Error(t, err, "an empty explicit flag must not fall back to environment")
	var r execution.Requirements
	flags := pflag.NewFlagSet("requirements", pflag.ContinueOnError)
	AddRequirementFlags(flags, &r)
	require.NoError(t, flags.Parse([]string{"--require-capability=unknown.feature", "--require-capability=other"}))
	r, err = r.Normalize()
	require.NoError(t, err)
	require.Equal(t, []string{"other", "unknown.feature"}, r.Capabilities)
	a, err = (Options{}).Parse(nil)
	require.NoError(t, err)
	require.Empty(t, a.Capabilities, "requirements never become allocation facts")
}
