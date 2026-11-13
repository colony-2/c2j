package shellcmd

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPOSIXText(t *testing.T) {
	for _, shell := range []string{"sh", "bash"} {
		t.Run(shell, func(t *testing.T) {
			binary, err := exec.LookPath(shell)
			require.NoError(t, err, "both supported Unix shells are required")
			marker := filepath.Join(t.TempDir(), "injected")
			for _, text := range []string{"", "two words\t", `back\slash\c\n`, "apostrophe's", `"double"`, "-n", "%s %% %n", "embedded\nnewlines\n", "echo data", "$(touch " + QuotePOSIX(marker) + "); `touch " + QuotePOSIX(marker) + "` $HOME * & | < >"} {
				for _, tc := range []struct{ name, script, want string }{
					{"quote", "printf '%s' " + QuotePOSIX(text), text},
					{"literal", Literal(text), text},
					{"line", LiteralLine(text), text + "\n"},
				} {
					out, err := exec.Command(binary, "-c", tc.script).CombinedOutput()
					require.NoError(t, err, "%s: %q: %s", tc.name, text, out)
					require.Equal(t, tc.want, string(out), "%s: %q", tc.name, text)
				}
			}
			_, err = os.Stat(marker)
			require.True(t, os.IsNotExist(err), "literal data must never execute")
		})
	}
}
