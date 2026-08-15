// Package relatedcomputation owns the freshness contract shared by Formula,
// Lookup, mutation and Query. Computed values are persisted as versioned
// envelopes; a scalar is usable only when every version component matches.
package relatedcomputation

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
)

const RowRevisionField = "__vt_row_revision"

type CellVersion struct {
	DefinitionVersion   int    `json:"definitionVersion"`
	SourceDataRevision  int64  `json:"sourceDataRevision"`
	DependencyWatermark string `json:"dependencyWatermark"`
}

type Expectation struct {
	DefinitionVersion   int
	SourceDataRevision  int64
	DependencyWatermark string
}

type Diagnostic struct {
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Details map[string]any `json:"details"`
}

type CellEnvelope struct {
	State      string      `json:"state"`
	Value      any         `json:"value"`
	Version    CellVersion `json:"version"`
	Diagnostic *Diagnostic `json:"diagnostic,omitempty"`
}

func Ready(value any, version CellVersion) CellEnvelope {
	return CellEnvelope{State: "ready", Value: value, Version: version}
}

func (envelope CellEnvelope) Fresh(expectation Expectation) bool {
	return envelope.State == "ready" &&
		envelope.Version.DefinitionVersion == expectation.DefinitionVersion &&
		envelope.Version.SourceDataRevision == expectation.SourceDataRevision &&
		envelope.Version.DependencyWatermark == expectation.DependencyWatermark
}

func (envelope CellEnvelope) ProductValue(expectation Expectation) any {
	if envelope.Fresh(expectation) {
		return envelope.Value
	}
	return map[string]any{
		"state": "updating",
		"value": nil,
		"diagnostic": map[string]any{
			"code":    "calculation.pending",
			"message": "computed value is waiting for a matching recalculation",
			"details": map[string]any{},
		},
	}
}

func Decode(value any) (CellEnvelope, bool) {
	raw, err := json.Marshal(value)
	if err != nil {
		return CellEnvelope{}, false
	}
	var envelope CellEnvelope
	if err := json.Unmarshal(raw, &envelope); err != nil ||
		envelope.State == "" || envelope.Version.DefinitionVersion < 1 ||
		envelope.Version.SourceDataRevision < 1 ||
		envelope.Version.DependencyWatermark == "" {
		return CellEnvelope{}, false
	}
	return envelope, true
}

func Watermark(revisions map[string]int64) string {
	keys := make([]string, 0, len(revisions))
	for key := range revisions {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	hash := sha256.New()
	for _, key := range keys {
		_, _ = fmt.Fprintf(hash, "%s\x00%d\x00", key, revisions[key])
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil))
}
