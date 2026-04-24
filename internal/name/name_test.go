package name_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/kurisu-agent/drift/internal/name"
	"github.com/kurisu-agent/drift/internal/rpcerr"
)

func TestValid(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"simple", "myproject", true},
		{"hyphenated", "my-project", true},
		{"with-digits", "proj-01", true},
		{"single-letter", "a", true},
		{"max-length", "a" + strings.Repeat("x", 62), true},

		{"empty", "", false},
		{"starts-with-digit", "1abc", false},
		{"starts-with-hyphen", "-abc", false},
		{"uppercase", "MyProj", false},
		{"underscore", "my_proj", false},
		{"too-long", "a" + strings.Repeat("x", 63), false},
		{"reserved-default", "default", false},
		{"reserved-none", "none", false},
		{"whitespace", "my proj", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := name.Valid(tc.in); got != tc.want {
				t.Errorf("Valid(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestValidate_InvalidSurfacesInvalidName(t *testing.T) {
	err := name.Validate("kart", "Bad_Name")
	if err == nil {
		t.Fatal("expected error")
	}
	var re *rpcerr.Error
	if !errors.As(err, &re) {
		t.Fatalf("want *rpcerr.Error, got %T: %v", err, err)
	}
	if re.Type != rpcerr.TypeInvalidName {
		t.Errorf("type = %q, want %q", re.Type, rpcerr.TypeInvalidName)
	}
	if re.Code != rpcerr.CodeUserError {
		t.Errorf("code = %d, want %d", re.Code, rpcerr.CodeUserError)
	}
	if got := re.Data["kind"]; got != "kart" {
		t.Errorf("data.kind = %v, want kart", got)
	}
}

func TestValidate_ReservedFlaggedDistinctly(t *testing.T) {
	err := name.Validate("circuit", "default")
	if err == nil {
		t.Fatal("expected error")
	}
	if !name.Reserved("default") {
		t.Errorf("Reserved(default) = false, want true")
	}
}

func TestValidate_OKReturnsNil(t *testing.T) {
	if err := name.Validate("kart", "my-kart"); err != nil {
		t.Errorf("Validate: %v", err)
	}
}

func TestValidateCharacterRejectsLongName(t *testing.T) {
	// 33 chars — valid as a generic name, invalid as a character / POSIX user.
	longOK := "a" + strings.Repeat("b", 32)
	if err := name.Validate("kart", longOK); err != nil {
		t.Fatalf("generic validate should accept 33-char name: %v", err)
	}
	if err := name.ValidateCharacter(longOK); err == nil {
		t.Fatalf("ValidateCharacter should reject 33-char name")
	}
}

func TestValidateCharacterAcceptsNormalName(t *testing.T) {
	if err := name.ValidateCharacter("kurisu"); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if err := name.ValidateCharacter("a-b"); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
}

func TestValidateCharacterAlsoRejectsReserved(t *testing.T) {
	if err := name.ValidateCharacter("default"); err == nil {
		t.Fatalf("reserved name should still be rejected")
	}
}
