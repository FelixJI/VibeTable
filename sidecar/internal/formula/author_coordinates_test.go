package formula

import (
	"testing"

	"github.com/vibetable/vibetable/sidecar/internal/contracts/workbench"
)

func TestAuthorCoordinatesPreserveUTF16AndLineEndings(t *testing.T) {
	source := "😀 +\r\n{金额}\n{é}\r{尾}"
	index, err := indexAuthorSource(source)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		line, start, end int64
		want             string
	}{
		{0, 0, 2, "😀"},
		{1, 0, 4, "{金额}"},
		{2, 0, 4, "{é}"},
		{3, 0, 3, "{尾}"},
	} {
		value := workbench.FormulaTextRange{
			Start: workbench.FormulaTextPosition{Line: test.line, Character: test.start},
			End:   workbench.FormulaTextPosition{Line: test.line, Character: test.end},
		}
		span, rangeErr := index.byteRange(value)
		if rangeErr != nil {
			t.Fatal(rangeErr)
		}
		if got := source[span.Start:span.End]; got != test.want {
			t.Fatalf("source range = %q, want %q", got, test.want)
		}
		if index.positions[span.Start] != value.Start || index.positions[span.End] != value.End {
			t.Fatal("coordinate round trip changed range")
		}
	}
}

func TestAuthorCoordinatesRejectInvalidTokenBoundaries(t *testing.T) {
	index, err := indexAuthorSource("😀\r\nx")
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct{ start, end int64 }{
		{0, 1}, {1, 2}, {-1, 2}, {2, 0}, {0, 0}, {0, 3},
	} {
		_, err := index.byteRange(workbench.FormulaTextRange{
			Start: workbench.FormulaTextPosition{Character: test.start},
			End:   workbench.FormulaTextPosition{Character: test.end},
		})
		assertFormulaCode(t, err, "formula.author.range")
	}
	if _, exists := index.positions[5]; exists {
		t.Fatal("CRLF interior exposed as a token boundary")
	}
	_, err = indexAuthorSource(string([]byte{0xff}))
	assertFormulaCode(t, err, "formula.author.range")
}
