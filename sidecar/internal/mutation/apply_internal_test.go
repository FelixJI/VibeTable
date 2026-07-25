package mutation

import (
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"testing"

	validation "github.com/pocketbase/ozzo-validation/v4"
)

func TestDefaultRecordIDsAlwaysMatchPocketBaseFormatAndRemainUnique(t *testing.T) {
	pattern := regexp.MustCompile(`^[a-z0-9]{15}$`)
	seen := make(map[string]struct{}, 4096)
	for range 4096 {
		id := newMutationID("record")
		if !pattern.MatchString(id) {
			t.Fatalf("generated record id %q does not match PocketBase format", id)
		}
		if _, duplicate := seen[id]; duplicate {
			t.Fatalf("generated duplicate record id %q", id)
		}
		seen[id] = struct{}{}
	}
}

func TestOperationGuardsRoundTripAndRejectStaleJunctionRows(t *testing.T) {
	revision := "row_0002"
	digest := "sha256:523c5bdec0a9b1e85ba88ff83544f21cef4aaf074bb84b77e3b53e3289adc06d"
	operation := Operation{
		Kind: OperationUpdate, RecordID: stringPointer("junction-row"),
		Values:           map[string]any{"quantity": 2},
		ExpectedRevision: &revision, ExpectedDigest: &digest,
	}
	raw, err := json.Marshal(operation)
	if err != nil {
		t.Fatal(err)
	}
	var decoded Operation
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.ExpectedRevision == nil || *decoded.ExpectedRevision != revision ||
		decoded.ExpectedDigest == nil || *decoded.ExpectedDigest != digest {
		t.Fatalf("operation guards changed after round trip: %#v", decoded)
	}

	stale := "row_0001"
	err = validateOperationGuard(
		map[string]any{"title": "Hello", "id": "rec"},
		2,
		NormalizedOperation{
			Kind: OperationUpdate, RecordID: stringPointer("rec"),
			ExpectedRevision: &stale,
		},
		0,
	)
	var productErr *ProductError
	if !errors.As(err, &productErr) ||
		productErr.Code != "mutation.revision_conflict" ||
		productErr.Path == nil ||
		*productErr.Path != "operations[0].expectedRevision" {
		t.Fatalf("stale operation guard = %#v", err)
	}
}

func TestStoredCountersRejectMissingNegativeAndFractionalValues(t *testing.T) {
	for _, value := range []any{
		nil, -1, -1.0, 1.5, "0", float64(maxSafeCounter) + 2,
	} {
		_, err := storedNonNegativeInteger(value, "mutation.outbox.invalid_attempts")
		var productErr *ProductError
		if !errors.As(err, &productErr) ||
			productErr.Code != "mutation.outbox.invalid_attempts" {
			t.Fatalf("storedNonNegativeInteger(%#v) = %#v", value, err)
		}
	}
	for _, value := range []any{0, 0.0, int64(2)} {
		got, err := storedNonNegativeInteger(value, "mutation.outbox.invalid_attempts")
		if err != nil || got < 0 {
			t.Fatalf("storedNonNegativeInteger(%#v) = %d, %v", value, got, err)
		}
	}
}

func TestCanonicalProductRowDigestUsesStableProviderNeutralJSON(t *testing.T) {
	digest, err := canonicalDigest(map[string]any{"title": "Hello", "id": "rec"})
	if err != nil {
		t.Fatal(err)
	}
	const expected = "sha256:523c5bdec0a9b1e85ba88ff83544f21cef4aaf074bb84b77e3b53e3289adc06d"
	if digest != expected {
		t.Fatalf("digest = %q, want %q", digest, expected)
	}
	if got := formatRevision("row", 12); got != "row_0012" {
		t.Fatalf("row revision = %q", got)
	}
}

func TestStorageValidationFailureExposesOnlyStableFieldPathAndTemplate(t *testing.T) {
	err := validation.Errors{
		"payload": validation.NewError(
			"validation_invalid_json",
			"Invalid JSON value.",
		).SetParams(map[string]interface{}{"value": "must-not-leak"}),
	}
	productErr := storageValidationFailure(err)
	if productErr.Code != "mutation.validation.failed" ||
		productErr.Path == nil || *productErr.Path != "payload" ||
		productErr.Message != "Invalid JSON value." ||
		len(productErr.Details) != 0 {
		t.Fatalf("storage validation error = %#v", productErr)
	}
}

func TestRequestResourceLimitsFailBeforeStorage(t *testing.T) {
	request := Request{
		ContractVersion: ContractVersion,
		RequestID:       "req", IdempotencyKey: "idem", TableID: "table",
		SchemaRevision: "schema_0001",
		Actor:          Actor{Type: "user", ID: "local"},
		Operations: []Operation{{
			Kind:   OperationInsert,
			Values: map[string]any{"value": strings.Repeat("x", maxRequestBytes)},
		}},
	}
	err := validateRequestShape(request)
	var productErr *ProductError
	if !errors.As(err, &productErr) || productErr.Code != "mutation.body.limit" {
		t.Fatalf("body limit = %#v", err)
	}
	request.Operations = make([]Operation, maxOperations+1)
	for index := range request.Operations {
		request.Operations[index] = Operation{Kind: OperationDelete, RecordID: stringPointer("rec")}
	}
	err = validateRequestShape(request)
	if !errors.As(err, &productErr) || productErr.Code != "mutation.batch.limit" {
		t.Fatalf("batch limit = %#v", err)
	}
}

func TestRequestRejectsMalformedRevisionDigestAndDirectOperationShapes(t *testing.T) {
	valid := Request{
		ContractVersion: ContractVersion,
		RequestID:       "req", IdempotencyKey: "idem", TableID: "table",
		SchemaRevision: "schema_0001",
		Actor:          Actor{Type: "user", ID: "local"},
		Operations: []Operation{{
			Kind: OperationUpdate, RecordID: stringPointer("record"),
			Values: map[string]any{"title": "updated"},
		}},
	}
	for name, mutate := range map[string]func(*Request){
		"schema revision": func(request *Request) {
			request.SchemaRevision = "latest"
		},
		"row revision": func(request *Request) {
			request.ExpectedRevision = stringPointer("row_latest")
		},
		"digest": func(request *Request) {
			request.ExpectedDigest = stringPointer("sha256:not-a-digest")
		},
		"empty update": func(request *Request) {
			request.Operations[0].Values = map[string]any{}
		},
		"irrelevant attachment fields": func(request *Request) {
			request.Operations[0].UploadHandles = []string{"hidden"}
		},
	} {
		t.Run(name, func(t *testing.T) {
			request := valid
			request.Operations = append([]Operation(nil), valid.Operations...)
			mutate(&request)
			var productErr *ProductError
			if err := validateRequestShape(request); !errors.As(err, &productErr) {
				t.Fatalf("error = %#v", err)
			}
		})
	}
}

func TestFormulaBackfillActorMaySubmitEmptyUpdates(t *testing.T) {
	for _, actorID := range []string{
		"formula-backfill", "formula-fanout",
	} {
		request := Request{
			ContractVersion: ContractVersion,
			RequestID:       "job-request-" + actorID,
			IdempotencyKey:  "job-key-" + actorID,
			TableID:         "table",
			SchemaRevision:  "schema_0001",
			Actor:           Actor{Type: "system", ID: actorID},
			Operations: []Operation{{
				Kind:     OperationUpdate,
				RecordID: stringPointer("record"),
				Values:   map[string]any{},
			}},
		}
		if err := validateRequestShape(request); err != nil {
			t.Fatalf("%s empty update = %#v", actorID, err)
		}
	}
}

func TestEventOperationNormalizesAttachmentAndMixedBatches(t *testing.T) {
	for name, test := range map[string]struct {
		results []operationResult
		want    DataChangeOperation
	}{
		"attachment": {
			results: []operationResult{{kind: OperationSetAttachments}},
			want:    DataChangeUpdate,
		},
		"mixed": {
			results: []operationResult{{kind: OperationInsert}, {kind: OperationUpdate}},
			want:    DataChangeUpdate,
		},
		"archive": {
			results: []operationResult{{kind: OperationArchive}, {kind: OperationArchive}},
			want:    DataChangeArchive,
		},
	} {
		t.Run(name, func(t *testing.T) {
			if got := eventOperation(test.results); got != test.want {
				t.Fatalf("event operation = %q, want %q", got, test.want)
			}
		})
	}
}
