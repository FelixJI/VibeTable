package relatedcomputation

import (
	"math"
	"testing"
)

func TestComputedCellVersionRequiresExactDefinitionSourceAndDependencyMatch(t *testing.T) {
	version := CellVersion{
		DefinitionVersion: 3, SourceDataRevision: 8,
		DependencyWatermark: Watermark(map[string]int64{"customers": 12}),
	}
	envelope := Ready("current", version)
	exact := Expectation{
		DefinitionVersion:   version.DefinitionVersion,
		SourceDataRevision:  version.SourceDataRevision,
		DependencyWatermark: version.DependencyWatermark,
	}
	if !envelope.Fresh(exact) || envelope.ProductValue(exact) != "current" {
		t.Fatal("exact computed version was not accepted")
	}
	for name, changed := range map[string]Expectation{
		"definition": {4, 8, version.DependencyWatermark},
		"source":     {3, 9, version.DependencyWatermark},
		"dependency": {3, 8, Watermark(map[string]int64{"customers": 13})},
	} {
		t.Run(name, func(t *testing.T) {
			if envelope.Fresh(changed) {
				t.Fatal("stale computed version was accepted")
			}
			pending, ok := envelope.ProductValue(changed).(map[string]any)
			if !ok || pending["state"] != "updating" || pending["value"] != nil {
				t.Fatalf("pending envelope = %#v", pending)
			}
		})
	}
}

func TestDependencyWatermarkIsDeterministicAndRevisionSensitive(t *testing.T) {
	left := Watermark(map[string]int64{"b": 2, "a": 1})
	right := Watermark(map[string]int64{"a": 1, "b": 2})
	if left != right {
		t.Fatalf("watermark depends on map order: %q != %q", left, right)
	}
	if left == Watermark(map[string]int64{"a": 1, "b": 3}) {
		t.Fatal("watermark ignored a dependency revision")
	}
}

func TestDecodeRejectsMalformedOrIncompleteEnvelopes(t *testing.T) {
	valid := Ready("current", CellVersion{
		DefinitionVersion: 1, SourceDataRevision: 2,
		DependencyWatermark: Watermark(map[string]int64{}),
	})
	if decoded, ok := Decode(valid); !ok || decoded.Value != "current" {
		t.Fatalf("valid envelope = %#v, %t", decoded, ok)
	}
	for name, value := range map[string]any{
		"marshal":      math.Inf(1),
		"scalar":       "plain",
		"state":        map[string]any{"version": valid.Version},
		"definition":   Ready(nil, CellVersion{SourceDataRevision: 2, DependencyWatermark: "x"}),
		"source":       Ready(nil, CellVersion{DefinitionVersion: 1, DependencyWatermark: "x"}),
		"dependencies": Ready(nil, CellVersion{DefinitionVersion: 1, SourceDataRevision: 2}),
	} {
		t.Run(name, func(t *testing.T) {
			if _, ok := Decode(value); ok {
				t.Fatalf("invalid envelope was decoded: %#v", value)
			}
		})
	}
}
