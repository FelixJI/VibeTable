package formula

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestClockFunctionsShareBatchAndTrackOnlyVolatileDependencies(t *testing.T) {
	instant := time.Date(2024, 3, 10, 6, 59, 59, 900000000, time.UTC)
	ctx := WithEvaluationTime(context.Background(), instant)
	table := formulaTable(
		formulaField("now_id", "clock_now", dateTimeType, "NOW()"),
		formulaField("utc_id", "utc_day", dateTimeType, `TODAY("UTC")`),
		formulaField("cn_id", "cn_day", dateTimeType, `TODAY("Asia/Shanghai")`),
		formulaField("ny_id", "ny_day", dateTimeType, `TODAY("America/New_York")`),
		formulaField("label_id", "label", textType, `formatDate(utc_day, "yyyy-MM-dd")`),
		formulaField("constant_id", "constant", integerType, "7"),
	)
	plan, err := NewCompiler(DefaultLimits()).CompileExecutionTable(table)
	if err != nil {
		t.Fatalf("clock error: %#v", err)
	}
	for i := 0; i < 3; i++ {
		values, err := plan.Evaluate(ctx, map[string]any{}, nil)
		if err != nil {
			t.Fatalf("clock error: %#v", err)
		}
		if values["clock_now"] != "2024-03-10T06:59:00Z" || values["utc_day"] != "2024-03-10T00:00:00Z" || values["ny_day"] != "2024-03-10T00:00:00Z" || values["label"] != "2024-03-10" {
			t.Fatalf("fixed batch = %#v", values)
		}
	}
	if len(plan.FieldClockReferences("constant_id")) != 0 {
		t.Fatal("ordinary formula became volatile")
	}
	if len(plan.FieldClockReferences("label_id")) != 1 {
		t.Fatal("derived formula lost the clock dependency")
	}
	minuteRefs := plan.FieldClockReferences("now_id")
	dayRefs := plan.FieldClockReferences("ny_id")
	afterDST := WithEvaluationTime(ctx, instant.Add(2*time.Second))
	if ClockSignature(ctx, minuteRefs) == ClockSignature(afterDST, minuteRefs) {
		t.Fatal("minute did not invalidate")
	}
	if ClockSignature(ctx, dayRefs) != ClockSignature(afterDST, dayRefs) {
		t.Fatal("DST jump changed the local date")
	}
	midnight := WithEvaluationTime(ctx, time.Date(2024, 3, 11, 4, 0, 0, 0, time.UTC))
	if ClockSignature(ctx, dayRefs) == ClockSignature(midnight, dayRefs) {
		t.Fatal("local midnight did not invalidate")
	}
	// The system local location is a selected OS setting, never a hard-coded
	// planner locale. Explicit zones remain independent from this override.
	previous := time.Local
	time.Local = time.FixedZone("test-local", -7*3600)
	defer func() { time.Local = previous }()
	local, err := NewCompiler(DefaultLimits()).CompileExecutionTable(formulaTable(formulaField("day_id", "day", dateTimeType, "TODAY()")))
	if err != nil {
		t.Fatalf("clock error: %#v", err)
	}
	values, err := local.Evaluate(WithEvaluationTime(ctx, time.Date(2024, 1, 1, 1, 0, 0, 0, time.UTC)), map[string]any{}, nil)
	if err != nil || values["day"] != "2023-12-31T00:00:00Z" {
		t.Fatalf("system day=%#v err=%v", values, err)
	}
}

func TestClockCallsRejectDynamicZoneAndPreserveLiteralText(t *testing.T) {
	compiler := NewCompiler(DefaultLimits())
	for _, source := range []string{`TODAY(upper("UTC"))`, `TODAY("Missing/Zone")`, `TODAY(1)`, `NOW("UTC")`} {
		if _, err := compiler.InferExecutionSource(formulaTable(), source); err == nil {
			t.Fatalf("accepted %s", source)
		}
	}
	refs, _, err := AnalyzeClock(`CONCATENATE("NOW()", "TODAY()")`, nil)
	if err != nil || len(refs) != 0 {
		t.Fatalf("literal treated as clock: %#v %v", refs, err)
	}
	if signature := ClockSignature(context.Background(), nil); signature != "" {
		t.Fatal(signature)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	plan, err2 := compiler.CompileExecutionTable(formulaTable(formulaField("now_id", "now_value", dateTimeType, "NOW()")))
	if err2 != nil {
		t.Fatal(err2)
	}
	_, failure := plan.Evaluate(ctx, map[string]any{}, nil)
	if failure == nil || !strings.Contains(failure.Code, "resource") {
		t.Fatalf("cancelled evaluation=%v", failure)
	}
}
