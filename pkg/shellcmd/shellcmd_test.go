package shellcmd

import (
	"reflect"
	"testing"
)

func TestBuildArgvUsesNonLoginShellForPosixShells(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		shell string
		want  []string
	}{
		{name: "bash", shell: "bash", want: []string{"bash", "-c", "echo ok"}},
		{name: "bash path", shell: "/bin/bash", want: []string{"/bin/bash", "-c", "echo ok"}},
		{name: "zsh", shell: "zsh", want: []string{"zsh", "-c", "echo ok"}},
		{name: "sh", shell: "sh", want: []string{"sh", "-c", "echo ok"}},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := BuildArgv(tc.shell, "echo ok")
			if err != nil {
				t.Fatalf("BuildArgv(): %v", err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("BuildArgv() = %#v, want %#v", got, tc.want)
			}
		})
	}
}

func TestBuildArgvUsesPlatformShellFlags(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		shell string
		want  []string
	}{
		{name: "cmd", shell: "cmd", want: []string{"cmd", "/C", "echo ok"}},
		{name: "cmd exe", shell: `C:\Windows\System32\cmd.exe`, want: []string{`C:\Windows\System32\cmd.exe`, "/C", "echo ok"}},
		{name: "powershell", shell: "powershell", want: []string{"powershell", "-Command", "echo ok"}},
		{name: "pwsh", shell: "pwsh", want: []string{"pwsh", "-Command", "echo ok"}},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := BuildArgv(tc.shell, "echo ok")
			if err != nil {
				t.Fatalf("BuildArgv(): %v", err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("BuildArgv() = %#v, want %#v", got, tc.want)
			}
		})
	}
}
