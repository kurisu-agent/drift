package client

import (
	"errors"
	"testing"

	"github.com/kurisu-agent/drift/internal/rpcerr"
)

func TestCompareSemver(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		local        string
		remote       string
		wantErr      bool
		wantWarn     bool
		wantErrType  rpcerr.Type
	}{
		{"identical", "1.2.3", "1.2.3", false, false, ""},
		{"patch differs is silent", "1.2.3", "1.2.9", false, false, ""},
		{"minor differs warns", "1.2.3", "1.5.0", false, true, ""},
		{"major differs aborts", "1.2.3", "2.0.0", true, false, "version_mismatch"},
		{"local devel tolerates", "devel", "1.0.0", false, false, ""},
		{"remote devel tolerates", "1.0.0", "devel", false, false, ""},
		{"v-prefix stripped", "v1.2.3", "1.2.3", false, false, ""},
		{"pre-release stripped", "1.2.3-rc.1", "1.2.3", false, false, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			out := compareSemver(tt.local, tt.remote, "c1")
			hasErr := out.err != nil
			if hasErr != tt.wantErr {
				t.Fatalf("err = %v, want err=%v", out.err, tt.wantErr)
			}
			hasWarn := out.warn != ""
			if hasWarn != tt.wantWarn {
				t.Fatalf("warn = %q, want warn=%v", out.warn, tt.wantWarn)
			}
			if tt.wantErrType != "" {
				var re *rpcerr.Error
				if !errors.As(out.err, &re) {
					t.Fatalf("err is not *rpcerr.Error: %T", out.err)
				}
				if re.Type != tt.wantErrType {
					t.Fatalf("err type = %q, want %q", re.Type, tt.wantErrType)
				}
			}
		})
	}
}
