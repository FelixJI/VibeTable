package main

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

func TestParseChangedLines(t *testing.T) {
	changed := parseChangedLines("+++ b/sidecar/internal/core/core.go\n@@ -1,2 +10,3 @@\n")
	for _, line := range []int{10, 11, 12} {
		if !changed["sidecar/internal/core/core.go"][line] {
			t.Fatalf("line %d was not marked changed", line)
		}
	}
}

func TestParseProfileRejectsMalformedInput(t *testing.T) {
	path := filepath.Join(t.TempDir(), "coverage.out")
	if err := os.WriteFile(path, []byte("mode: count\nnot-a-profile\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := parseProfile(path); err == nil {
		t.Fatal("expected malformed profile to be rejected")
	}
}

func TestParseProfileRejectsCoordinatesOutsideNativeInt(t *testing.T) {
	tooLarge := "2147483648"
	if strconv.IntSize == 64 {
		tooLarge = "9223372036854775808"
	}
	path := filepath.Join(t.TempDir(), "coverage.out")
	profile := "mode: count\nexample.com/vibetable/sidecar/file.go:" +
		tooLarge + ".1,2.2 1 1\n"
	if err := os.WriteFile(path, []byte(profile), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := parseProfile(path); err == nil {
		t.Fatal("expected out-of-range coordinate to be rejected")
	}
}

func TestMetricUsesEmptySetAsFullyQualified(t *testing.T) {
	value := newMetric(0, 0)
	if value.Percent != 100 {
		t.Fatalf("empty metric percent = %v", value.Percent)
	}
}
