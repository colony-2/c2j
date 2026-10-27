package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"

	"github.com/colony-2/c2j/cmd/c2j/internal/skillinstall"
	"github.com/colony-2/c2j/internal/buildinfo"
	skillsbundle "github.com/colony-2/c2j/skills"
	"github.com/stretchr/testify/require"
)

func TestSkillsUpdateRefreshesBundleWithoutChangingConfig(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	old := fstest.MapFS{
		"c2j-recipes/SKILL.md":    {Data: []byte("---\nname: c2j-recipes\ndescription: old bundle\n---\nOld instructions\n")},
		"c2j-recipes/obsolete.md": {Data: []byte("removed upstream")},
	}
	_, err := skillinstall.InstallBundled(t.Context(), skillinstall.Options{Bundle: old})
	require.NoError(t, err)
	config := []byte("# retain custom configuration\nproject: local\n")
	require.NoError(t, os.WriteFile(filepath.Join(root, ".c2", "config.yaml"), config, 0o644))
	run := func(args ...string) string {
		t.Helper()
		cmd := newSkillsCmd()
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetArgs(append([]string{"update"}, args...))
		require.NoError(t, cmd.ExecuteContext(t.Context()))
		return out.String()
	}
	require.Contains(t, run(), "installed skills:")
	path := filepath.Join(root, ".agents", "skills", "c2j-recipes", "SKILL.md")
	want, err := skillsbundle.FS.ReadFile("c2j-recipes/SKILL.md")
	require.NoError(t, err)
	got, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, want, got)
	require.NoFileExists(t, filepath.Join(filepath.Dir(path), "obsolete.md"))
	require.Contains(t, run(), "same content digest")
	local := append(bytes.Clone(want), []byte("\nLocal instructions\n")...)
	require.NoError(t, os.WriteFile(path, local, 0o644))
	require.Contains(t, run(), "existing skill has local modifications")
	got, err = os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, local, got)
	require.Contains(t, run("--force"), "installed skills: c2j-recipes")
	got, err = os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, want, got)
	got, err = os.ReadFile(filepath.Join(root, ".c2", "config.yaml"))
	require.NoError(t, err)
	require.Equal(t, config, got)
}

func TestSkillsUpdateUserScopeWithoutConfig(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	cmd := newSkillsCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetArgs([]string{"update", "--scope=user"})
	require.NoError(t, cmd.ExecuteContext(t.Context()))
	require.FileExists(t, filepath.Join(home, "skills", "c2j-recipes", "SKILL.md"))
	require.NoFileExists(t, filepath.Join(root, ".c2", "config.yaml"))
	require.NoDirExists(t, filepath.Join(root, ".agents"))
}

func TestSkillsUpdateRejectsInvalidArguments(t *testing.T) {
	t.Chdir(t.TempDir())
	for _, args := range [][]string{{"update", "--scope=invalid"}, {"update", "unexpected"}} {
		cmd := newSkillsCmd()
		cmd.SilenceErrors, cmd.SilenceUsage = true, true
		cmd.SetArgs(args)
		require.Error(t, cmd.ExecuteContext(t.Context()))
		require.NoDirExists(t, ".agents")
	}
	cmd, _, err := newRootCmd(buildinfo.Info{}).Find([]string{"skills", "update"})
	require.NoError(t, err)
	require.Equal(t, "c2j skills update", cmd.CommandPath())
}
