package objectrepo

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"testing"
)

// legacyCanonicalManifestID is the historical identity algorithm, preserved
// here verbatim as the compatibility reference for the shared manifest
// contract: Unmarshal(payload, any) -> sorted labels wrapper -> json.Marshal
// -> sha256. It exists only in tests; production streams the same wrapper
// into the digest (canonicalManifestID). If the two ever disagree, every
// durable manifest identity ever written would stop verifying.
func legacyCanonicalManifestID(input ManifestInput) (ManifestID, error) {
	if strings.TrimSpace(input.Name) == "" || len(input.Payload) == 0 {
		return "", errors.New("repository.manifest_invalid")
	}
	var payload any
	if err := json.Unmarshal(input.Payload, &payload); err != nil {
		return "", errors.New("repository.manifest_invalid")
	}
	keys := make([]string, 0, len(input.Labels))
	for key := range input.Labels {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	labels := make([][2]string, 0, len(keys))
	for _, key := range keys {
		labels = append(labels, [2]string{key, input.Labels[key]})
	}
	raw, err := json.Marshal(struct {
		Labels  [][2]string `json:"labels"`
		Payload any         `json:"payload"`
	}{Labels: labels, Payload: payload})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return ManifestID("manifest_" + hex.EncodeToString(sum[:])), nil
}

// TestCanonicalManifestIDMatchesLegacyIdentity runs the streamed identity
// against the legacy algorithm across the shapes that historically defined
// the contract: nested containers, HTML/Unicode escaping with an invalid
// surrogate escape, float formatting edges, duplicate keys, nil/empty
// labels, and scalar payloads, plus the shared rejection boundary.
func TestCanonicalManifestIDMatchesLegacyIdentity(t *testing.T) {
	labels := map[string]string{"zeta": "z", "alpha": "<a>&\"", "": "empty"}
	cases := []struct {
		name    string
		labels  map[string]string
		payload string
	}{
		{
			name:    "nested maps and arrays",
			labels:  labels,
			payload: `{"a":{"b":[1,{"c":"d"},[[2],{}]]},"e":[],"f":null}`,
		},
		{
			name: "html unicode key order and invalid surrogate",
			// The payload is a raw string, so \ud800 reaches the decoder as
			// the literal invalid-surrogate escape; both algorithms decode it
			// to U+FFFD before re-marshaling.
			labels:  labels,
			payload: `{"<script>":"&\u003c‮","键":"值😀","z":"😀","bad":"\ud800","deep":{"é键":"😀😀","\u202e":""}}`,
		},
		{
			name:    "negative zero large and exponent floats",
			labels:  labels,
			payload: `[-0.0,1e21,1.2345678901234568e+29,1.5e-10,9007199254740993,0.1,3]`,
		},
		{
			name:    "duplicate keys keep last",
			labels:  labels,
			payload: `{"a":1,"a":2,"nested":{"x":1,"x":2}}`,
		},
		{
			name:    "nil labels",
			labels:  nil,
			payload: `{"k":"v"}`,
		},
		{
			name:    "empty labels",
			labels:  map[string]string{},
			payload: `{"k":"v"}`,
		},
		{
			name:    "null payload",
			labels:  labels,
			payload: `null`,
		},
		{
			name:    "html and emoji scalar payload",
			labels:  labels,
			payload: `"text with <&> and 😀"`,
		},
		{
			name:    "empty object payload",
			labels:  labels,
			payload: `{}`,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			input := ManifestInput{
				Name:    "compat",
				Labels:  testCase.labels,
				Payload: []byte(testCase.payload),
			}
			legacy, legacyErr := legacyCanonicalManifestID(input)
			streamed, streamedErr := canonicalManifestID(input)
			if legacyErr != nil || streamedErr != nil {
				t.Fatalf(
					"identity errors: legacy=%v streamed=%v",
					legacyErr, streamedErr,
				)
			}
			if legacy != streamed {
				t.Fatalf(
					"identity drift for payload %q labels %#v: legacy=%s streamed=%s",
					testCase.payload, testCase.labels, legacy, streamed,
				)
			}
			if !strings.HasPrefix(string(streamed), "manifest_") ||
				len(streamed) != len("manifest_")+sha256.Size*2 {
				t.Fatalf("identity format = %s", streamed)
			}
		})
	}
	for _, rejection := range []struct {
		name  string
		input ManifestInput
	}{
		{
			name:  "broken json payload",
			input: ManifestInput{Name: "compat", Labels: labels, Payload: []byte(`{"broken":`)},
		},
		{
			name:  "empty payload",
			input: ManifestInput{Name: "compat", Labels: labels},
		},
		{
			name:  "blank name",
			input: ManifestInput{Name: "   ", Labels: labels, Payload: []byte(`{}`)},
		},
	} {
		t.Run("reject/"+rejection.name, func(t *testing.T) {
			_, streamedErr := canonicalManifestID(rejection.input)
			if streamedErr == nil ||
				streamedErr.Error() != "repository.manifest_invalid" {
				t.Fatalf("streamed rejection = %v", streamedErr)
			}
			_, legacyErr := legacyCanonicalManifestID(rejection.input)
			if legacyErr == nil ||
				legacyErr.Error() != "repository.manifest_invalid" {
				t.Fatalf("legacy rejection = %v", legacyErr)
			}
		})
	}
	// Pin the numeric formatting boundary that JSON re-marshaling of decoded
	// float64 values historically produced.
	for _, payload := range []string{
		`123456789012345678901234567890`, `1e-7`, `-0`,
	} {
		input := ManifestInput{Name: "compat", Labels: labels, Payload: []byte(payload)}
		legacy, legacyErr := legacyCanonicalManifestID(input)
		streamed, streamedErr := canonicalManifestID(input)
		if legacyErr != nil || streamedErr != nil || legacy != streamed {
			t.Fatalf(
				"numeric identity drift for %q: legacy=%v(%s) streamed=%v(%s)",
				payload, legacyErr, legacy, streamedErr, streamed,
			)
		}
	}
}
