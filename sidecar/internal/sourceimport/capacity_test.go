package sourceimport

import (
	"context"
	"strings"
	"testing"

	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
)

func TestPreviewBlocksUnsplitableRowBeforeCreatingTargets(t *testing.T) {
	source := relationSnapshot()
	source.Tables = source.Tables[:1]
	source.Tables[0].Fields = []Field{textField("title"), {ID: "payload", Name: "Payload", Kind: "json", ValueKind: v2.LogicalJSON}}
	source.Tables[0].PrimaryFieldID = "title"
	source.Tables[0].Records = []Record{{ID: "r1", Values: map[string]any{"title": "Large row", "payload": map[string]any{"body": strings.Repeat("x", batchBytes)}}}}
	plan, err := Preview(context.Background(), source, Options{SelectedTableIDs: []string{source.Tables[0].ID}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, diagnostic := range plan.Diagnostics {
		if diagnostic.Code == "source_import.record.capacity" && diagnostic.Blocking && !plan.CanApply {
			return
		}
	}
	t.Fatalf("oversized atomic row was not blocked by preflight: %#v", plan.Diagnostics)
}
