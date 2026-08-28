package workspacesearch

import (
	"encoding/hex"
	"strings"
	"unicode"
)

const symbolProjectionPrefix = "vtsym"

// projectSearchText preserves normalized lexical text while replacing each
// complete symbol sequence with one FTS-safe token. Literal "v" code points
// are doubled so ordinary text cannot collide with the reserved token prefix.
func projectSearchText(value string) string {
	runes := []rune(value)
	var projected strings.Builder
	projected.Grow(len(value))
	lexicalStart := 0
	for index := 0; index < len(runes); {
		if clusterEnd, found := symbolClusterEnd(runes, index); found {
			writeProjectedLexicalText(&projected, string(runes[lexicalStart:index]))
			start := index
			index = clusterEnd
			projected.WriteByte(' ')
			projected.WriteString(symbolProjectionPrefix)
			projected.WriteString(hex.EncodeToString([]byte(string(runes[start:index]))))
			projected.WriteByte(' ')
			lexicalStart = index
			continue
		}
		index++
	}
	writeProjectedLexicalText(&projected, string(runes[lexicalStart:]))
	return projected.String()
}

func writeProjectedLexicalText(projected *strings.Builder, value string) {
	for _, current := range Normalize(value) {
		if current == 'v' {
			projected.WriteString("vv")
		} else {
			projected.WriteRune(current)
		}
	}
}

func symbolClusterEnd(runes []rune, index int) (int, bool) {
	if !isSymbolBase(runes, index) {
		if isSymbolContinuation(runes[index]) {
			return index + 1, true
		}
		return index, false
	}
	end := consumeSymbolElement(runes, index)
	for end < len(runes) && runes[end] == '\u200d' &&
		end+1 < len(runes) && isSymbolBase(runes, end+1) {
		end = consumeSymbolElement(runes, end+1)
	}
	return end, true
}

func consumeSymbolElement(runes []rune, index int) int {
	end := index + 1
	if isRegionalIndicator(runes[index]) && end < len(runes) &&
		isRegionalIndicator(runes[end]) {
		end++
	}
	for end < len(runes) &&
		(isVariationSelector(runes[end]) || isEmojiModifier(runes[end])) {
		end++
	}
	if end < len(runes) && runes[end] == '\u20e3' {
		end++
	}
	for end < len(runes) && isEmojiTag(runes[end]) {
		end++
	}
	return end
}

func isSymbolBase(runes []rune, index int) bool {
	value := runes[index]
	if unicode.IsSymbol(value) || isEmojiPropertyException(value) {
		return true
	}
	if !isKeycapBase(value) || index+1 >= len(runes) {
		return false
	}
	next := runes[index+1]
	return isVariationSelector(next) || next == '\u20e3'
}

func isSymbolContinuation(value rune) bool {
	return value == '\u200d' || value == '\u20e3' ||
		isVariationSelector(value) || isEmojiTag(value)
}

func isKeycapBase(value rune) bool {
	return value == '#' || value == '*' || value >= '0' && value <= '9'
}

func isEmojiModifier(value rune) bool {
	return value >= '\U0001f3fb' && value <= '\U0001f3ff'
}

func isRegionalIndicator(value rune) bool {
	return value >= '\U0001f1e6' && value <= '\U0001f1ff'
}

func isEmojiTag(value rune) bool {
	return value >= '\U000e0020' && value <= '\U000e007f'
}

func isVariationSelector(value rune) bool {
	return value >= '\ufe00' && value <= '\ufe0f' ||
		value >= '\U000e0100' && value <= '\U000e01ef'
}

// Unicode 17 Emoji=Yes code points outside the Symbol categories and the
// ASCII keycap bases. Keeping this list aligned with unicode.Version avoids
// treating Emoji punctuation or the compatibility-decomposable ℹ as lexical.
func isEmojiPropertyException(value rune) bool {
	switch value {
	case '\u203c', '\u2049', '\u2139', '\u3030', '\u303d':
		return true
	default:
		return false
	}
}
