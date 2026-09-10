package metadata

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
)

// The existing workspace revision is produced by Python's sorted UTF-8 JSON.
// Keep that wire contract (including float spelling), rather than adding a new digest.
func dashboardCanonicalJSON(value any) ([]byte, error) {
	var out bytes.Buffer
	var write func(any) error
	text := func(s string) {
		out.WriteByte('"')
		for _, r := range s {
			switch r {
			case '"':
				out.WriteString(`\"`)
			case '\\':
				out.WriteString(`\\`)
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
				if r < 32 {
					fmt.Fprintf(&out, `\u%04x`, r)
				} else {
					out.WriteRune(r)
				}
			}
		}
		out.WriteByte('"')
	}
	write = func(v any) error {
		switch x := v.(type) {
		case nil:
			out.WriteString("null")
		case bool:
			out.WriteString(strconv.FormatBool(x))
		case string:
			text(x)
		case int:
			out.WriteString(strconv.Itoa(x))
		case json.Number:
			if !strings.ContainsAny(string(x), ".eE") {
				if x == "-0" {
					out.WriteByte('0')
				} else {
					out.WriteString(string(x))
				}
				break
			}
			f, err := x.Float64()
			if err != nil || math.IsInf(f, 0) || math.IsNaN(f) {
				return dashboardInvalidParams()
			}
			a := math.Abs(f)
			var s string
			if a == 0 || (a >= 1e-4 && a < 1e16) {
				s = strconv.FormatFloat(f, 'f', -1, 64)
				if !strings.Contains(s, ".") {
					s += ".0"
				}
			} else {
				s = strconv.FormatFloat(f, 'e', -1, 64)
			}
			out.WriteString(s)
		case float64:
			raw := strconv.FormatFloat(x, 'g', -1, 64)
			if !strings.ContainsAny(raw, ".eE") {
				raw += ".0"
			}
			return write(json.Number(raw))
		case []any:
			out.WriteByte('[')
			for i, item := range x {
				if i > 0 {
					out.WriteByte(',')
				}
				if err := write(item); err != nil {
					return err
				}
			}
			out.WriteByte(']')
		case DashboardParams:
			return write(map[string]any(x))
		case map[string]any:
			keys := make([]string, 0, len(x))
			for key := range x {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			out.WriteByte('{')
			for i, key := range keys {
				if i > 0 {
					out.WriteByte(',')
				}
				text(key)
				out.WriteByte(':')
				if err := write(x[key]); err != nil {
					return err
				}
			}
			out.WriteByte('}')
		default:
			return fmt.Errorf("unsupported Dashboard JSON value %T", v)
		}
		return nil
	}
	if err := write(value); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}
func dashboardRevision(value any) (string, error) {
	raw, err := dashboardCanonicalJSON(value)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:]), nil
}
