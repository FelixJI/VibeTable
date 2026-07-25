package metadata

import (
	"encoding/json"
	"testing"
)

func TestCanonicalPayloadRevisionIsStableAcrossObjectKeyOrder(t *testing.T) {
	left, leftRevision, err := canonicalPayload(
		json.RawMessage(`{"b":[2,3],"a":1}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	right, rightRevision, err := canonicalPayload(
		json.RawMessage(`{"a":1,"b":[2,3]}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	if string(left) != string(right) ||
		leftRevision != rightRevision {
		t.Fatalf(
			"canonical payload mismatch: %s/%s != %s/%s",
			left, leftRevision, right, rightRevision,
		)
	}
}

func TestCanonicalPayloadAcceptsEmptyJSONContainers(t *testing.T) {
	for _, raw := range []json.RawMessage{
		json.RawMessage(`{}`),
		json.RawMessage(`[]`),
		json.RawMessage(`null`),
	} {
		if _, revision, err := canonicalPayload(raw); err != nil ||
			revision == "" {
			t.Fatalf("canonicalPayload(%s) = %q, %v", raw, revision, err)
		}
	}
}

func TestMutationReceiptTraceFieldsAreAdditiveToLegacyJSON(t *testing.T) {
	oldJSON := json.RawMessage(
		`{"status":"applied","item":{"namespace":"presets",` +
			`"logicalId":"compact","payload":{"columns":["name"]},` +
			`"revision":"sha256:legacy"}}`,
	)
	var current MutationReceipt
	if err := json.Unmarshal(oldJSON, &current); err != nil {
		t.Fatal(err)
	}
	if current.Status != StatusApplied ||
		current.Item.LogicalID != "compact" ||
		current.ChangeSetID != "" ||
		current.EmittedEvents != nil {
		t.Fatalf("legacy receipt = %#v", current)
	}

	current.ChangeSetID = "changeSet_1"
	current.EmittedEvents = []string{"event_1"}
	raw, err := json.Marshal(current)
	if err != nil {
		t.Fatal(err)
	}
	var legacy struct {
		Status string `json:"status"`
		Item   Item   `json:"item"`
	}
	if err := json.Unmarshal(raw, &legacy); err != nil {
		t.Fatal(err)
	}
	if legacy.Status != StatusApplied ||
		legacy.Item.LogicalID != "compact" {
		t.Fatalf("legacy projection = %#v", legacy)
	}
}
