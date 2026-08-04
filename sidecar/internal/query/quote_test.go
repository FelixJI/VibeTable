package query

import "testing"

func TestQuoteEscapesSQLiteIdentifierDelimiters(t *testing.T) {
	identifier := `field" FROM secrets --`

	if got, want := quote(identifier), `"field"" FROM secrets --"`; got != want {
		t.Fatalf("quote(%q) = %q, want %q", identifier, got, want)
	}
}
