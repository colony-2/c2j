package repository

import (
	"github.com/stretchr/testify/require"
	"testing"
)

func TestExplicitIdentitiesMatchExistingNormalization(t *testing.T) {
	for _, value := range []string{"github.com/acme/app", "github.com/acme/app.git", "https://github.com/acme/app.git", "ssh://git@example.com/acme/app.git", "git@example.com:acme/app.git", "file:///tmp/app"} {
		want, err := Normalize(value)
		require.NoError(t, err)
		got, err := NormalizeIdentity(value)
		require.NoError(t, err)
		require.Equal(t, want, got)
	}
	for _, value := range []string{"", "app", "./acme/app", "../acme/app", "/tmp/app", "file:relative", "git+https://github.com/acme/app.git//recipe.yaml@main"} {
		_, err := NormalizeIdentity(value)
		require.Error(t, err, value)
	}
}
