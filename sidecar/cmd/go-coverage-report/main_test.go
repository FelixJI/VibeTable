package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
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

func TestAnalyzeUsesWindowsBuildConstraintsForCoverageDenominator(t *testing.T) {
	repositoryRoot := t.TempDir()
	scope := filepath.Join("sidecar", "internal", "platformfixture")
	scopeRoot := filepath.Join(repositoryRoot, scope)
	if err := os.MkdirAll(scopeRoot, 0o755); err != nil {
		t.Fatal(err)
	}

	files := map[string]string{
		"common.go": `package platformfixture

func common(value bool) bool {
	if value {
		return true
	}
	return false
}
`,
		"source_windows.go": `//go:build windows

package platformfixture

func windowsOnly(value bool) bool {
	if value {
		return true
	}
	return false
}
`,
		"source_other.go": `//go:build !windows

package platformfixture

func nonWindowsOnly(value bool) bool {
	if value {
		return true
	}
	return false
}
`,
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(scopeRoot, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	result, err := analyze(repositoryRoot, []string{scope}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Line.Total != 6 || result.Branch.Total != 4 {
		t.Fatalf(
			"Windows denominator = line %d, branch %d; want line 6, branch 4",
			result.Line.Total,
			result.Branch.Total,
		)
	}
}

func TestCoverageBuildContextTargetsWindowsAMD64(t *testing.T) {
	context := coverageBuildContext()
	if context.GOOS != "windows" || context.GOARCH != "amd64" {
		t.Fatalf("coverage build target = %s/%s; want windows/amd64", context.GOOS, context.GOARCH)
	}
}

func TestAnalyzeRejectsEmptyLineDenominator(t *testing.T) {
	repositoryRoot := t.TempDir()
	scope := filepath.Join("sidecar", "internal", "emptyfixture")
	scopeRoot := filepath.Join(repositoryRoot, scope)
	if err := os.MkdirAll(scopeRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(scopeRoot, "empty.go"),
		[]byte("package emptyfixture\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}

	if _, err := analyze(repositoryRoot, []string{scope}, nil, nil); err == nil {
		t.Fatal("expected empty executable-line denominator to be rejected")
	}
}

func TestAnalyzeRejectsEmptyBranchDenominator(t *testing.T) {
	repositoryRoot := t.TempDir()
	scope := filepath.Join("sidecar", "internal", "branchlessfixture")
	scopeRoot := filepath.Join(repositoryRoot, scope)
	if err := os.MkdirAll(scopeRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(scopeRoot, "branchless.go"),
		[]byte("package branchlessfixture\n\nfunc branchless() { println(\"covered\") }\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}

	if _, err := analyze(repositoryRoot, []string{scope}, nil, nil); err == nil {
		t.Fatal("expected empty decision-arm denominator to be rejected")
	}
}

func TestParseOptionsRequiresCoverageGroup(t *testing.T) {
	_, err := parseOptions([]string{
		"--profile", "coverage.out",
		"--repository-root", ".",
		"--report", "report.json",
		"--scope", "sidecar/internal/query",
	})
	if err == nil || !strings.Contains(err.Error(), "group") {
		t.Fatalf("missing group error = %v", err)
	}
}

func TestParseOptionsRequiresExplicitCoverageThresholds(t *testing.T) {
	_, err := parseOptions([]string{
		"--group", "authority",
		"--profile", "coverage.out",
		"--repository-root", ".",
		"--report", "report.json",
		"--scope", "sidecar/internal/query",
	})
	if err == nil || !strings.Contains(err.Error(), "threshold") {
		t.Fatalf("missing threshold error = %v", err)
	}
}

func TestParseOptionsRejectsInvalidCoverageThresholds(t *testing.T) {
	for _, value := range []string{"NaN", "+Inf", "0", "101"} {
		t.Run(value, func(t *testing.T) {
			_, err := parseOptions([]string{
				"--group", "authority",
				"--profile", "coverage.out",
				"--repository-root", ".",
				"--report", "report.json",
				"--scope", "sidecar/internal/query",
				"--line-min", value,
				"--branch-min", "40",
				"--diff-min", "90",
			})
			if err == nil {
				t.Fatalf("invalid threshold %q was accepted", value)
			}
		})
	}
}

func TestReportEvidenceIncludesGroupIdentityAndNamedSummary(t *testing.T) {
	config := options{
		group: "authority",
		scopes: stringList{
			"sidecar/internal/query",
			"sidecar/internal/mutation",
		},
		lineMinimum: 65, branchMinimum: 55, diffMinimum: 90,
	}
	result := finalizeReport(report{
		Line:   metric{Covered: 8, Total: 10, Percent: 80},
		Branch: metric{Covered: 3, Total: 4, Percent: 75},
		Diff:   metric{Percent: 100},
	}, config, "GitHub/main")

	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(encoded, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["formatVersion"] != float64(2) || payload["group"] != "authority" {
		t.Fatalf("report identity = %#v", payload)
	}
	const expected = "Go authority coverage: line 80.00% (8/10), " +
		"branch 75.00% (3/4), diff 100.00% (0/0)\n"
	if summary := formatCoverageSummary(result); summary != expected {
		t.Fatalf("coverage summary = %q, want %q", summary, expected)
	}
}
