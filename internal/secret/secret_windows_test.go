package secret

import (
	"strings"
	"testing"
)

func TestRoundTripAndNoPlainText(t *testing.T) {
	for _, plain := range []string{"", "a", "0123456789abcdef0123456789abcdef", "ümlaut ✓"} {
		stored, err := Protect(plain)
		if err != nil {
			t.Fatal(err)
		}
		if plain != "" && (!strings.HasPrefix(stored, prefix) || (len(plain) > 8 && strings.Contains(stored, plain))) {
			t.Fatalf("Protect(%q) = %q", plain, stored)
		}
		got, err := Unprotect(stored)
		if err != nil || got != plain {
			t.Fatalf("Unprotect = %q, %v; want %q", got, err, plain)
		}
	}
	if _, err := Unprotect("plain-text"); err == nil {
		t.Fatal("unprotected input must be rejected")
	}
}
