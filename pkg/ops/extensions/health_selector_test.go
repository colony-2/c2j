package extensions

import (
	"github.com/stretchr/testify/require"
	"testing"
)

func TestHealthDisplaySelector(t *testing.T) {
	for _, test := range []struct{ input, want string }{
		{"./ops/check", "./ops/check"}, {"../check", "../check"},
		{"git+https://user:password@example.com/r.git?token=secret#credential//ops/check@main", "git+https://example.com/r.git//ops/check@main"},
		{"git+ssh://git@example.com/r.git//op@v1", "git+ssh://example.com/r.git//op@v1"},
		{"nix:github:owner/repo/v1#checks.health", "nix:github:owner/repo/v1#checks.health"},
		{"nix:git+https://u:pass@example.com/r?private=secret#ops.health", "nix:git+https://example.com/r#ops.health"},
		{"nix:./local#op", "nix:./local#op"},
		{"nix:nixpkgs#hello", "nix:nixpkgs#hello"},
		{"git+https://bad%zz//op@main", "[redacted]"}, {"invalid secret", "[redacted]"},
		{"git+https://host/r//op@main?token=secret", "[redacted]"},
		{"nix:github:user/repo?secret=abc#op", "[redacted]"}, {"./op\nsecret", "[redacted]"},
	} {
		t.Run(test.input, func(t *testing.T) { require.Equal(t, test.want, DisplaySelector(test.input)) })
	}
}
