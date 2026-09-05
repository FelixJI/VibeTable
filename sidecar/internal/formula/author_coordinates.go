package formula

import (
	"unicode/utf8"

	"github.com/vibetable/vibetable/sidecar/internal/contracts/workbench"
)

// authorCoordinates maps only Unicode scalar boundaries. A UTF-16 coordinate
// inside a surrogate pair is never a valid token boundary.
type authorCoordinates struct {
	positions map[int]workbench.FormulaTextPosition
	offsets   map[workbench.FormulaTextPosition]int
}

func indexAuthorSource(source string) (authorCoordinates, *Error) {
	if !utf8.ValidString(source) {
		return authorCoordinates{}, formulaError("formula.author.range", "author source is not valid UTF-8", nil)
	}
	index := authorCoordinates{
		positions: make(map[int]workbench.FormulaTextPosition),
		offsets:   make(map[workbench.FormulaTextPosition]int),
	}
	position := workbench.FormulaTextPosition{}
	for offset := 0; offset < len(source); {
		index.positions[offset] = position
		index.offsets[position] = offset
		r, size := utf8.DecodeRuneInString(source[offset:])
		if r == '\r' || r == '\n' {
			if r == '\r' && offset+size < len(source) && source[offset+size] == '\n' {
				size++
			}
			position.Line++
			position.Character = 0
		} else {
			position.Character++
			if r > 0xffff {
				position.Character++
			}
		}
		offset += size
	}
	index.positions[len(source)] = position
	index.offsets[position] = len(source)
	return index, nil
}

func (index authorCoordinates) byteRange(value workbench.FormulaTextRange) (SourceSpan, *Error) {
	start, startOK := index.offsets[value.Start]
	end, endOK := index.offsets[value.End]
	if !startOK || !endOK || start >= end {
		return SourceSpan{}, formulaError(
			"formula.author.range", "token range must cover complete characters in source order", nil,
		)
	}
	return SourceSpan{Start: start, End: end}, nil
}
