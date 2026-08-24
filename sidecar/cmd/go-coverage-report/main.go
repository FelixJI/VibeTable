package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"go/ast"
	"go/build"
	"go/parser"
	"go/token"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

type profileBlock struct {
	startLine int
	startCol  int
	endLine   int
	endCol    int
	count     int64
}

type fileCoverage struct {
	blocks []profileBlock
}

type metric struct {
	Covered int     `json:"covered"`
	Total   int     `json:"total"`
	Percent float64 `json:"percent"`
}

type report struct {
	FormatVersion      int                `json:"formatVersion"`
	Group              string             `json:"group"`
	Scope              []string           `json:"scope"`
	BaseRef            string             `json:"baseRef"`
	Definitions        map[string]string  `json:"definitions"`
	Line               metric             `json:"line"`
	Branch             metric             `json:"branch"`
	Diff               metric             `json:"diff"`
	UncoveredLines     []string           `json:"uncoveredLines,omitempty"`
	UncoveredDiffLines []string           `json:"uncoveredDiffLines,omitempty"`
	Thresholds         map[string]float64 `json:"thresholds"`
}

var profilePattern = regexp.MustCompile(
	`^(.+):(\d+)\.(\d+),(\d+)\.(\d+)\s+\d+\s+(\d+)$`,
)

type options struct {
	group          string
	profilePath    string
	repositoryRoot string
	baseRef        string
	reportPath     string
	lineMinimum    float64
	branchMinimum  float64
	diffMinimum    float64
	scopes         stringList
}

func parseOptions(args []string) (options, error) {
	var result options
	flags := flag.NewFlagSet("go-coverage-report", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.StringVar(&result.group, "group", "", "coverage group name")
	flags.StringVar(&result.profilePath, "profile", "", "Go count-mode coverprofile")
	flags.StringVar(&result.repositoryRoot, "repository-root", "", "repository root")
	flags.StringVar(&result.baseRef, "base-ref", "", "git base ref for diff coverage")
	flags.StringVar(&result.reportPath, "report", "", "JSON report path")
	flags.Float64Var(
		&result.lineMinimum, "line-min", math.NaN(), "minimum executable-line coverage",
	)
	flags.Float64Var(
		&result.branchMinimum, "branch-min", math.NaN(), "minimum decision-arm coverage",
	)
	flags.Float64Var(
		&result.diffMinimum, "diff-min", math.NaN(), "minimum changed executable-line coverage",
	)
	flags.Var(&result.scopes, "scope", "repository-relative production source directory; repeatable")
	if err := flags.Parse(args); err != nil {
		return options{}, err
	}
	if result.group == "" {
		return options{}, errors.New("coverage group is required")
	}
	if result.profilePath == "" || result.repositoryRoot == "" || result.reportPath == "" ||
		len(result.scopes) == 0 {
		return options{}, errors.New(
			"profile, repository-root, report, and at least one scope are required",
		)
	}
	thresholds := []float64{result.lineMinimum, result.branchMinimum, result.diffMinimum}
	for _, minimum := range thresholds {
		if math.IsNaN(minimum) {
			return options{}, errors.New("line, branch, and diff thresholds are required")
		}
		if minimum <= 0 || minimum > 100 {
			return options{}, errors.New("coverage thresholds must be between 1 and 100")
		}
	}
	return result, nil
}

func main() {
	config, err := parseOptions(os.Args[1:])
	if err != nil {
		fatal(err)
	}

	profiles, err := parseProfile(config.profilePath)
	if err != nil {
		fatal(err)
	}
	changed, resolvedBase, err := changedLines(
		config.repositoryRoot,
		config.baseRef,
		config.scopes,
	)
	if err != nil {
		fatal(err)
	}
	result, err := analyze(config.repositoryRoot, config.scopes, profiles, changed)
	if err != nil {
		fatal(err)
	}
	result = finalizeReport(result, config, resolvedBase)
	if err := writeReport(config.reportPath, result); err != nil {
		fatal(err)
	}
	fmt.Print(formatCoverageSummary(result))
	if result.Line.Percent < config.lineMinimum || result.Branch.Percent < config.branchMinimum ||
		result.Diff.Percent < config.diffMinimum {
		os.Exit(1)
	}
}

func finalizeReport(result report, config options, resolvedBase string) report {
	result.FormatVersion = 2
	result.Group = config.group
	result.Scope = append([]string(nil), config.scopes...)
	result.BaseRef = resolvedBase
	result.Definitions = map[string]string{
		"line":   "unique AST executable source lines reached by a Go coverprofile block",
		"branch": "if true/false and explicit switch/select decision arms reached; generated and test files excluded",
		"diff":   "changed AST executable source lines reached relative to baseRef, including untracked scope files",
	}
	result.Thresholds = map[string]float64{
		"line": config.lineMinimum, "branch": config.branchMinimum, "diff": config.diffMinimum,
	}
	return result
}

func formatCoverageSummary(result report) string {
	return fmt.Sprintf(
		"Go %s coverage: line %.2f%% (%d/%d), branch %.2f%% (%d/%d), diff %.2f%% (%d/%d)\n",
		result.Group,
		result.Line.Percent, result.Line.Covered, result.Line.Total,
		result.Branch.Percent, result.Branch.Covered, result.Branch.Total,
		result.Diff.Percent, result.Diff.Covered, result.Diff.Total,
	)
}

type stringList []string

func (values *stringList) String() string { return strings.Join(*values, ",") }
func (values *stringList) Set(value string) error {
	value = filepath.ToSlash(filepath.Clean(value))
	if value == "." || strings.HasPrefix(value, "../") {
		return fmt.Errorf("invalid coverage scope %q", value)
	}
	*values = append(*values, value)
	return nil
}

func parseProfile(path string) (map[string]fileCoverage, error) {
	input, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open coverprofile: %w", err)
	}
	defer input.Close()
	result := make(map[string]fileCoverage)
	scanner := bufio.NewScanner(input)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "mode:") {
			continue
		}
		parts := profilePattern.FindStringSubmatch(line)
		if len(parts) != 7 {
			return nil, fmt.Errorf("invalid coverprofile line %q", line)
		}
		pathPart := parts[1]
		marker := "/sidecar/"
		index := strings.Index(filepath.ToSlash(pathPart), marker)
		if index < 0 {
			continue
		}
		relative := "sidecar/" + filepath.ToSlash(pathPart)[index+len(marker):]
		coordinates := make([]int, 4)
		for index, raw := range parts[2:6] {
			value, parseErr := strconv.Atoi(raw)
			if parseErr != nil {
				return nil, fmt.Errorf("parse coverprofile line %q: %w", line, parseErr)
			}
			coordinates[index] = value
		}
		count, parseErr := strconv.ParseInt(parts[6], 10, 64)
		if parseErr != nil {
			return nil, fmt.Errorf("parse coverprofile line %q: %w", line, parseErr)
		}
		entry := result[relative]
		block := profileBlock{
			startLine: coordinates[0], startCol: coordinates[1],
			endLine: coordinates[2], endCol: coordinates[3], count: count,
		}
		merged := false
		for index := range entry.blocks {
			existing := &entry.blocks[index]
			if existing.startLine == block.startLine && existing.startCol == block.startCol &&
				existing.endLine == block.endLine && existing.endCol == block.endCol {
				existing.count += block.count
				merged = true
				break
			}
		}
		if !merged {
			entry.blocks = append(entry.blocks, block)
		}
		result[relative] = entry
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read coverprofile: %w", err)
	}
	return result, nil
}

func analyze(
	repositoryRoot string,
	scopes []string,
	profiles map[string]fileCoverage,
	changed map[string]map[int]bool,
) (report, error) {
	buildContext := coverageBuildContext()
	lineCovered := make(map[string]bool)
	lineTotal := make(map[string]bool)
	diffCovered := make(map[string]bool)
	diffTotal := make(map[string]bool)
	branchCovered, branchTotal := 0, 0
	for _, scope := range scopes {
		root := filepath.Join(repositoryRoot, filepath.FromSlash(scope))
		err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") ||
				strings.HasSuffix(entry.Name(), "_test.go") || strings.Contains(entry.Name(), ".generated.") {
				return nil
			}
			matches, matchErr := buildContext.MatchFile(filepath.Dir(path), entry.Name())
			if matchErr != nil {
				return fmt.Errorf("match Go build constraints for %s: %w", path, matchErr)
			}
			if !matches {
				return nil
			}
			relativePath, relErr := filepath.Rel(repositoryRoot, path)
			if relErr != nil {
				return relErr
			}
			relative := filepath.ToSlash(relativePath)
			coverage := profiles[relative]
			files := token.NewFileSet()
			parsed, parseErr := parser.ParseFile(files, path, nil, 0)
			if parseErr != nil {
				return fmt.Errorf("parse %s: %w", relative, parseErr)
			}
			ast.Inspect(parsed, func(node ast.Node) bool {
				statement, ok := node.(ast.Stmt)
				if ok && executableStatement(statement) {
					line := files.Position(statement.Pos()).Line
					key := fmt.Sprintf("%s:%d", relative, line)
					lineTotal[key] = true
					covered := countAt(files, statement.Pos(), coverage.blocks) > 0
					lineCovered[key] = lineCovered[key] || covered
					if changed[relative][line] {
						diffTotal[key] = true
						diffCovered[key] = diffCovered[key] || covered
					}
				}
				covered, total := decisionCoverage(files, node, coverage.blocks)
				branchCovered += covered
				branchTotal += total
				return true
			})
			return nil
		})
		if err != nil {
			return report{}, err
		}
	}
	if len(lineTotal) == 0 {
		return report{}, errors.New("coverage scope has no executable-line denominator")
	}
	if branchTotal == 0 {
		return report{}, errors.New("coverage scope has no decision-arm denominator")
	}
	return report{
		Line:               newMetric(countTrue(lineCovered), len(lineTotal)),
		Branch:             newMetric(branchCovered, branchTotal),
		Diff:               newMetric(countTrue(diffCovered), len(diffTotal)),
		UncoveredLines:     falseKeys(lineTotal, lineCovered),
		UncoveredDiffLines: falseKeys(diffTotal, diffCovered),
	}, nil
}

func coverageBuildContext() build.Context {
	context := build.Default
	context.GOOS = "windows"
	context.GOARCH = "amd64"
	return context
}

func executableStatement(statement ast.Stmt) bool {
	switch statement.(type) {
	case *ast.BlockStmt, *ast.EmptyStmt, *ast.DeclStmt, *ast.LabeledStmt,
		*ast.CaseClause, *ast.CommClause:
		return false
	default:
		return true
	}
}

func decisionCoverage(files *token.FileSet, node ast.Node, blocks []profileBlock) (int, int) {
	switch value := node.(type) {
	case *ast.IfStmt:
		entry := countAt(files, value.Pos(), blocks)
		trueCount := countAt(files, firstPosition(value.Body.List, value.Body.Lbrace), blocks)
		covered := boolCount(trueCount > 0)
		if value.Else != nil {
			covered += boolCount(countAt(files, value.Else.Pos(), blocks) > 0)
		} else {
			covered += boolCount(entry > trueCount)
		}
		return covered, 2
	case *ast.SwitchStmt:
		return clauseCoverage(files, value.Body.List, blocks)
	case *ast.TypeSwitchStmt:
		return clauseCoverage(files, value.Body.List, blocks)
	case *ast.SelectStmt:
		return clauseCoverage(files, value.Body.List, blocks)
	default:
		return 0, 0
	}
}

func clauseCoverage(files *token.FileSet, clauses []ast.Stmt, blocks []profileBlock) (int, int) {
	covered := 0
	for _, statement := range clauses {
		position := statement.Pos()
		switch clause := statement.(type) {
		case *ast.CaseClause:
			position = firstPosition(clause.Body, clause.Colon)
		case *ast.CommClause:
			position = firstPosition(clause.Body, clause.Colon)
		}
		covered += boolCount(countAt(files, position, blocks) > 0)
	}
	return covered, len(clauses)
}

func firstPosition(statements []ast.Stmt, fallback token.Pos) token.Pos {
	if len(statements) == 0 {
		return fallback
	}
	return statements[0].Pos()
}

func countAt(files *token.FileSet, position token.Pos, blocks []profileBlock) int64 {
	point := files.Position(position)
	var selected *profileBlock
	for index := range blocks {
		block := &blocks[index]
		if before(point.Line, point.Column, block.startLine, block.startCol) ||
			before(block.endLine, block.endCol, point.Line, point.Column) {
			continue
		}
		if selected == nil || span(*block) < span(*selected) {
			selected = block
		}
	}
	if selected == nil {
		return 0
	}
	return selected.count
}

func before(line, column, otherLine, otherColumn int) bool {
	return line < otherLine || (line == otherLine && column < otherColumn)
}

func span(block profileBlock) int {
	return (block.endLine-block.startLine)*10000 + block.endCol - block.startCol
}

func changedLines(
	repositoryRoot string,
	requestedBase string,
	scopes []string,
) (map[string]map[int]bool, string, error) {
	base, err := resolveBase(repositoryRoot, requestedBase)
	if err != nil {
		return nil, "", err
	}
	args := []string{"diff", "--unified=0", "--no-ext-diff", base, "--"}
	args = append(args, scopes...)
	command := exec.Command("git", args...)
	command.Dir = repositoryRoot
	output, err := command.Output()
	if err != nil {
		return nil, "", fmt.Errorf("git diff %s: %w", base, err)
	}
	result := parseChangedLines(string(output))
	untracked := exec.Command("git", append([]string{"ls-files", "--others", "--exclude-standard", "--"}, scopes...)...)
	untracked.Dir = repositoryRoot
	untrackedOutput, err := untracked.Output()
	if err != nil {
		return nil, "", fmt.Errorf("list untracked Go files: %w", err)
	}
	for _, path := range strings.Fields(string(untrackedOutput)) {
		path = filepath.ToSlash(path)
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			continue
		}
		content, readErr := os.ReadFile(filepath.Join(repositoryRoot, filepath.FromSlash(path)))
		if readErr != nil {
			return nil, "", readErr
		}
		lines := strings.Count(string(content), "\n") + 1
		for line := 1; line <= lines; line++ {
			markChanged(result, path, line)
		}
	}
	return result, base, nil
}

var hunkPattern = regexp.MustCompile(`^@@ -\d+(?:,\d+)? \+(\d+)(?:,(\d+))? @@`)

func parseChangedLines(diff string) map[string]map[int]bool {
	result := make(map[string]map[int]bool)
	path := ""
	for _, line := range strings.Split(diff, "\n") {
		if strings.HasPrefix(line, "+++ b/") {
			path = strings.TrimPrefix(line, "+++ b/")
			continue
		}
		match := hunkPattern.FindStringSubmatch(line)
		if len(match) == 0 || path == "" {
			continue
		}
		start, _ := strconv.Atoi(match[1])
		length := 1
		if match[2] != "" {
			length, _ = strconv.Atoi(match[2])
		}
		for offset := 0; offset < length; offset++ {
			markChanged(result, path, start+offset)
		}
	}
	return result
}

func markChanged(result map[string]map[int]bool, path string, line int) {
	if result[path] == nil {
		result[path] = make(map[int]bool)
	}
	result[path][line] = true
}

func resolveBase(repositoryRoot string, requested string) (string, error) {
	candidates := []string{requested, "GitHub/main", "origin/main", "main", "HEAD^"}
	for _, candidate := range candidates {
		if candidate == "" {
			continue
		}
		command := exec.Command("git", "rev-parse", "--verify", candidate)
		command.Dir = repositoryRoot
		if command.Run() == nil {
			return candidate, nil
		}
	}
	return "", errors.New("no usable git base ref found")
}

func newMetric(covered, total int) metric {
	percent := 100.0
	if total > 0 {
		percent = float64(covered) * 100 / float64(total)
	}
	return metric{Covered: covered, Total: total, Percent: percent}
}

func countTrue(values map[string]bool) int {
	count := 0
	for _, value := range values {
		if value {
			count++
		}
	}
	return count
}

func falseKeys(total, covered map[string]bool) []string {
	result := make([]string, 0)
	for key := range total {
		if !covered[key] {
			result = append(result, key)
		}
	}
	sort.Strings(result)
	return result
}

func boolCount(value bool) int {
	if value {
		return 1
	}
	return 0
}

func writeReport(path string, value report) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	value.Scope = append([]string(nil), value.Scope...)
	sort.Strings(value.Scope)
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(encoded, '\n'), 0o644)
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(2)
}
