package app

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// describeRevision preserves the two EXISTING Python wire tokens (capabilityHash
// and lookupRevision). It is not a general canonicalization or integrity API.
func describeRevision(value any) (string, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var decoded any
	if err := decoder.Decode(&decoded); err != nil {
		return "", err
	}
	var encoded strings.Builder
	if err := appendDescribeRevision(&encoded, decoded); err != nil {
		return "", err
	}
	return fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(encoded.String()))), nil
}

func appendDescribeRevision(out *strings.Builder, value any) error {
	switch value := value.(type) {
	case map[string]any:
		keys := make([]string, 0, len(value))
		for key := range value {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		out.WriteByte('{')
		for i, key := range keys {
			if i > 0 {
				out.WriteByte(',')
			}
			if err := appendDescribeRevision(out, key); err != nil {
				return err
			}
			out.WriteByte(':')
			if err := appendDescribeRevision(out, value[key]); err != nil {
				return err
			}
		}
		out.WriteByte('}')
	case []any:
		out.WriteByte('[')
		for i, item := range value {
			if i > 0 {
				out.WriteByte(',')
			}
			if err := appendDescribeRevision(out, item); err != nil {
				return err
			}
		}
		out.WriteByte(']')
	case string:
		out.WriteByte('"')
		for _, char := range value {
			switch char {
			case '"', '\\':
				out.WriteByte('\\')
				out.WriteRune(char)
			case '\b':
				out.WriteString(`\b`)
			case '\f':
				out.WriteString(`\f`)
			case '\n':
				out.WriteString(`\n`)
			case '\r':
				out.WriteString(`\r`)
			case '\t':
				out.WriteString(`\t`)
			default:
				if char < 0x20 {
					fmt.Fprintf(out, `\u%04x`, char)
				} else {
					out.WriteRune(char)
				}
			}
		}
		out.WriteByte('"')
	case json.Number:
		text := string(value)
		if strings.ContainsAny(text, ".eE") {
			var err error
			text, err = describePythonFloat(value)
			if err != nil {
				return err
			}
		} else if text == "-0" {
			text = "0"
		}
		out.WriteString(text)
	case bool:
		fmt.Fprint(out, value)
	case nil:
		out.WriteString("null")
	}
	return nil
}

func describePythonFloat(number json.Number) (string, error) {
	value, err := number.Float64()
	if err != nil {
		return "", err
	}
	scientific := strconv.FormatFloat(value, 'e', -1, 64)
	exponent, err := strconv.Atoi(scientific[strings.LastIndexByte(scientific, 'e')+1:])
	if err != nil {
		return "", err
	}
	// Python repr uses fixed notation for -4 <= exponent < 16 and retains .0.
	if exponent >= -4 && exponent < 16 {
		fixed := strconv.FormatFloat(value, 'f', -1, 64)
		if !strings.Contains(fixed, ".") {
			fixed += ".0"
		}
		return fixed, nil
	}
	return scientific, nil
}

func normalizeDescribeRanges(snapshot map[string]any) error {
	settings := append([]any{}, snapshot["fields"].([]any)...)
	for _, capability := range snapshot["capabilities"].([]any) {
		settings = append(settings, capability.(map[string]any)["recommended"])
	}
	for _, setting := range settings {
		limits := setting.(map[string]any)["constraints"].(map[string]any)["range"].(map[string]any)
		for _, bound := range []string{"min", "max"} {
			if number, ok := limits[bound].(json.Number); ok {
				// Pydantic RangeSpec is float | str | None, unlike arbitrary JsonValue.
				// Python JSON first decodes integer -0 as 0; floating -0.0 keeps its sign.
				if number == "-0" {
					number = "0"
				}
				text, err := describePythonFloat(number)
				if err != nil {
					return err
				}
				limits[bound] = json.Number(text)
			}
		}
	}
	return nil
}
