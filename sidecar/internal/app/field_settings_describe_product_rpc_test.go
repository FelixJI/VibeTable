package app

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

type fieldSettingsDescribeProbe struct {
	result       fieldSettingsDescribeResult
	err          error
	calls        int
	table, field string
	after        func()
}

func (p *fieldSettingsDescribeProbe) DescribeFieldSettings(_ context.Context, table, field string) (fieldSettingsDescribeResult, error) {
	p.calls++
	p.table = table
	p.field = field
	if p.after != nil {
		p.after()
	}
	return p.result, p.err
}
func TestFieldSettingsDescribeProductParamsBudgetsUnicodeAndCancellation(t *testing.T) {
	for _, raw := range []string{`{"tableId":"ok","fieldId":"\ud800"}`, `{"tableId":"ok","password":"x"}`, `{"tableId":"ok","fieldId":null}`, `{"tableId":"ok","extra":{}}`, `{"tableId":"ok","fieldId":"` + strings.Repeat("x", maxFieldRequestBytes) + `"}`} {
		if _, _, err := decodeFieldSettingsDescribeParams(json.RawMessage(raw)); err == nil {
			t.Fatal("invalid parameters accepted")
		}
	}
	prefix := `{"tableId":"ok","fieldId":"`
	suffix := `"}`
	for _, size := range []int{maxFieldRequestBytes, maxFieldRequestBytes + 1} {
		raw := prefix + strings.Repeat("x", size-len(prefix)-len(suffix)) + suffix
		_, _, err := decodeFieldSettingsDescribeParams(json.RawMessage(raw))
		if (err == nil) != (size == maxFieldRequestBytes) {
			t.Fatalf("compact budget %d: %v", size, err)
		}
	}
	for _, after := range []bool{false, true} {
		ctx, cancel := context.WithCancel(context.Background())
		probe := &fieldSettingsDescribeProbe{}
		if after {
			probe.after = cancel
		} else {
			cancel()
		}
		_, err := fieldSettingsDescribeRegistration(probe).Handler(ctx, json.RawMessage(`{"tableId":"orders"}`))
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation=%v", err)
		}
		want := 0
		if after {
			want = 1
		}
		if probe.calls != want {
			t.Fatalf("calls=%d", probe.calls)
		}
		cancel()
	}
}
