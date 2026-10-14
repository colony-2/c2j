package workjob

import (
	"context"
	"github.com/colony-2/c2j/cmd/c2j/internal/defaults"
	"strings"
	"testing"
)

func TestReadyOptionsCompleteLeavesJobDBEmptyWhenUnknown(t *testing.T) {
	t.Setenv(defaults.JobDBEnv, "")

	opts := ReadyOptions{WorkingDir: t.TempDir()}
	if err := opts.Complete(context.Background()); err != nil {
		t.Fatalf("Complete(): %v", err)
	}

	if opts.TenantID != "" || opts.SWFURL != "" {
		t.Fatalf("resolved target = swf %q tenant %q, want empty", opts.SWFURL, opts.TenantID)
	}
}

func TestReadyOptionsValidate(t *testing.T) {
	tests := []struct {
		name    string
		opts    ReadyOptions
		wantErr string
	}{
		{
			name: "valid http",
			opts: ReadyOptions{JobDBURI: "http://localhost:9047/tenant", TenantID: "tenant", SWFURL: "http://localhost:9047"},
		},
		{
			name: "valid embed",
			opts: ReadyOptions{JobDBURI: defaults.EmbedURL, TenantID: defaults.EmbeddedTenantID, SWFURL: defaults.EmbedURL},
		},
		{
			name:    "missing jobdb",
			opts:    ReadyOptions{},
			wantErr: "--jobdb is required",
		},
		{
			name:    "unsupported scheme",
			opts:    ReadyOptions{JobDBURI: "ftp://example.invalid/tenant", TenantID: "tenant", SWFURL: "ftp://example.invalid"},
			wantErr: "unsupported JobDB runtime URL",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.opts.Validate()
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("Validate(): %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("Validate() succeeded, want error containing %q", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("Validate() error = %q, want substring %q", err.Error(), tt.wantErr)
			}
		})
	}
}
