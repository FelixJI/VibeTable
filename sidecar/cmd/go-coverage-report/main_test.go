package main

import (
	"encoding/json"
	"os"
	"os/exec"
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

func TestChangedLinesExcludesChangesAddedOnlyToAdvancedBase(t *testing.T) {
	repositoryRoot := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		command := exec.Command("git", args...)
		command.Dir = repositoryRoot
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, output)
		}
		return strings.TrimSpace(string(output))
	}
	write := func(name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(repositoryRoot, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	git("init", "-b", "main")
	git("config", "user.name", "Coverage test")
	git("config", "user.email", "coverage@example.invalid")
	write("existing.go", "package fixture\n\nfunc existing() int { return 1 }\n")
	write("local.go", "package fixture\n\nfunc local() int { return 1 }\n")
	git("add", "existing.go", "local.go")
	git("commit", "-m", "initial")
	originalBase := git("rev-parse", "HEAD")
	git("switch", "-c", "feature")
	write("feature.go", "package fixture\n\nfunc feature() int { return 1 }\n")
	git("add", "feature.go")
	git("commit", "-m", "feature")
	git("switch", "-c", "synthetic", originalBase)
	git("merge", "--no-ff", "feature", "-m", "PR checkout")
	git("switch", "main")
	write("existing.go", "package fixture\n\nfunc existing() int { return 2 }\n")
	git("add", "existing.go")
	git("commit", "-m", "unrelated main change")
	git("switch", "synthetic")
	write("feature.go", "package fixture\n\nfunc feature() int { return 3 }\n")
	write("local.go", "package fixture\n\nfunc local() int { return 5 }\n")
	write("untracked.go", "package fixture\n\nfunc untracked() int { return 4 }\n")
	changed, base, err := changedLines(repositoryRoot, "main", []string{"."})
	if err != nil {
		t.Fatal(err)
	}
	if len(changed["existing.go"]) != 0 {
		t.Fatalf("advanced main was counted as PR changes: %v", changed)
	}
	if !changed["feature.go"][3] || !changed["local.go"][3] || !changed["untracked.go"][3] {
		t.Fatalf("PR or untracked changes were omitted: %v", changed)
	}
	if base != originalBase {
		t.Fatalf("resolved base = %q, want common ancestor %q", base, originalBase)
	}
}
