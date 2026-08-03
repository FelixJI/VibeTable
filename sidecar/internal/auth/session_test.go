package auth

import (
	"encoding/hex"
	"strings"
	"testing"
)

func TestParseAcceptsHexAndBase64URL(t *testing.T) {
	raw := strings.Repeat("ab", secretSize)
	hexSecret, err := Parse(raw)
	if err != nil {
		t.Fatalf("Parse(hex): %v", err)
	}
	if !hexSecret.Matches(raw) {
		t.Fatal("parsed hex secret did not match its source")
	}

	generated, encoded, err := Generate()
	if err != nil {
		t.Fatalf("Generate(): %v", err)
	}
	if len(encoded) != 43 {
		t.Fatalf("base64url length = %d, want 43", len(encoded))
	}
	if !generated.Matches(encoded) {
		t.Fatal("generated secret did not match its encoded value")
	}
}

func TestParseRejectsWrongSizeAndWhitespace(t *testing.T) {
	for _, value := range []string{
		"",
		"short",
		strings.Repeat("00", secretSize-1),
		strings.Repeat("00", secretSize+1),
		" " + strings.Repeat("00", secretSize),
	} {
		if _, err := Parse(value); err == nil {
			t.Fatalf("Parse(%q) unexpectedly succeeded", value)
		}
	}
}

func TestMatchesRejectsDifferentOrMalformedSecret(t *testing.T) {
	source := make([]byte, secretSize)
	source[0] = 1
	secret, err := Parse(hex.EncodeToString(source))
	if err != nil {
		t.Fatalf("Parse(): %v", err)
	}

	different := make([]byte, secretSize)
	different[0] = 2
	if secret.Matches(hex.EncodeToString(different)) {
		t.Fatal("different secret matched")
	}
	if secret.Matches("malformed") {
		t.Fatal("malformed secret matched")
	}
	if secret.IsZero() {
		t.Fatal("nonzero secret reported as zero")
	}
}
