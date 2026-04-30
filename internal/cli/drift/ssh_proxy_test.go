package drift

import (
	"testing"
)

func TestParseKartAlias(t *testing.T) {
	cases := []struct {
		name     string
		alias    string
		circuit  string
		kart     string
		wantErr  bool
		errSubst string
	}{
		{
			name:    "happy path",
			alias:   "drift.my-circuit.my-kart",
			circuit: "my-circuit",
			kart:    "my-kart",
		},
		{
			name:     "missing drift prefix",
			alias:    "my-circuit.my-kart",
			wantErr:  true,
			errSubst: "drift.<circuit>",
		},
		{
			name:     "only two parts",
			alias:    "drift.my-circuit",
			wantErr:  true,
			errSubst: "drift.<circuit>",
		},
		{
			name:     "four parts rejected",
			alias:    "drift.a.b.c",
			wantErr:  true,
			errSubst: "drift.<circuit>",
		},
		{
			name:     "invalid circuit name",
			alias:    "drift.BAD.kart",
			wantErr:  true,
			errSubst: "circuit",
		},
		{
			name:     "invalid kart name",
			alias:    "drift.circuit.BAD",
			wantErr:  true,
			errSubst: "kart",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotCircuit, gotKart, err := parseKartAlias(tc.alias)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("parseKartAlias(%q) returned nil err, want error containing %q", tc.alias, tc.errSubst)
				}
				if tc.errSubst != "" && !containsCI(err.Error(), tc.errSubst) {
					t.Errorf("err = %q, want it to mention %q", err.Error(), tc.errSubst)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseKartAlias(%q) returned err %v, want success", tc.alias, err)
			}
			if gotCircuit != tc.circuit {
				t.Errorf("circuit = %q, want %q", gotCircuit, tc.circuit)
			}
			if gotKart != tc.kart {
				t.Errorf("kart = %q, want %q", gotKart, tc.kart)
			}
		})
	}
}

func containsCI(s, sub string) bool {
	// Cheap case-insensitive substring — the validator messages are ASCII.
	if len(sub) == 0 {
		return true
	}
	for i := 0; i+len(sub) <= len(s); i++ {
		ok := true
		for j := 0; j < len(sub); j++ {
			a, b := s[i+j], sub[j]
			if a >= 'A' && a <= 'Z' {
				a += 'a' - 'A'
			}
			if b >= 'A' && b <= 'Z' {
				b += 'a' - 'A'
			}
			if a != b {
				ok = false
				break
			}
		}
		if ok {
			return true
		}
	}
	return false
}
