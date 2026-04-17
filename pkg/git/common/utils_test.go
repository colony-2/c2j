package common

import "testing"

func TestIsRemoteRepository(t *testing.T) {
	cases := []struct {
		name   string
		input  string
		expect bool
	}{
		{"https url", "https://github.com/org/repo.git", true},
		{"ssh url", "ssh://git@example.com/foo/bar.git", true},
		{"scp style", "git@github.com:org/repo.git", true},
		{"file url", "file:///tmp/repo.git", true},
		{"plain path", "/tmp/repo", false},
		{"relative path", "../repo", false},
	}

	for _, tc := range cases {
		if got := IsRemoteRepository(tc.input); got != tc.expect {
			t.Fatalf("%s: expected %v got %v", tc.name, tc.expect, got)
		}
	}
}
