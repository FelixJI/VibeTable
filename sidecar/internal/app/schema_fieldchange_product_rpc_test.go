package app

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"testing"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/router"
	"github.com/vibetable/vibetable/sidecar/internal/fieldchange"
	"github.com/vibetable/vibetable/sidecar/internal/productrpc"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
	"github.com/vibetable/vibetable/sidecar/internal/schemaapi"
	"github.com/vibetable/vibetable/sidecar/internal/schemaerror"
)

func TestSchemaFieldChangeParamsMatchFrozenPython(t *testing.T) {
	raw, err := os.ReadFile("../../../contracts/v2/schema-fieldchange-python-oracle.json")
	if err != nil {
		t.Fatal(err)
	}
	var frozen struct {
		Cases []struct {
			Name    string `json:"name"`
			Request struct {
				Method string          `json:"method"`
				Params json.RawMessage `json:"params"`
			} `json:"request"`
			Response struct {
				Error *struct {
					Code int `json:"code"`
				} `json:"error"`
			} `json:"response"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(raw, &frozen); err != nil {
		t.Fatal(err)
	}
	for _, item := range frozen.Cases {
		t.Run(item.Name, func(t *testing.T) {
			_, _, err := decodeSchemaFieldChangeParams(item.Request.Method, item.Request.Params)
			rejected := item.Response.Error != nil && (item.Response.Error.Code == -32602 || item.Response.Error.Code == -32600)
			if (err != nil) != rejected {
				t.Fatalf("Python rejected=%v Go=%v", rejected, err)
			}
		})
	}
}

func TestSchemaFieldChangePlanDoesNotPrematurelyValidateDomain(t *testing.T) {
	raw := json.RawMessage(`{"action":"create","tableId":"orders","fieldId":"","expectedSchemaRevision":"schema_1","expectedDataRevision":null,"draft":{},"actor":{},"conversionRule":"","confirmation":"","backupReceipt":""}`)
	if _, _, err := decodeSchemaFieldChangeParams("field.change.plan", raw); err != nil {
		t.Fatal(err)
	}
	apply := json.RawMessage(`{"plan_id":"plan","plan_hash":"hash","operation_id":"op","actor":{"id":"local","kind":"user"},"confirmations":[],"protection_snapshot_id":null}`)
	if _, _, err := decodeSchemaFieldChangeParams("field.change.apply", apply); err != nil {
		t.Fatal(err)
	}
	registration := schemaFieldChangeRegistrations(schemaFieldChangeDomain{}, nil)[3]
	_, err := registration.Handler(context.Background(), apply)
	var public *productrpc.PublicError
	if !errors.As(err, &public) || public.Code != "field.contract.invalid" {
		t.Fatalf("alias must preserve old domain rejection: %v", err)
	}
}

func TestSchemaFieldChangeRealProducerAndWriteAdmission(t *testing.T) {
	pb := schemaProductStore(t)
	migration := fieldchange.NewMigrationService(pb, nil)
	t.Cleanup(migration.Shutdown)
	r := router.NewRouter(func(w http.ResponseWriter, request *http.Request) (*core.RequestEvent, router.EventCleanupFunc) {
		return &core.RequestEvent{Event: router.Event{Response: w, Request: request}}, nil
	})
	var blocked bool
	var calls []string
	denied := errors.New("test write admission denied")
	gate := func(ctx context.Context, kind, key string, apply func(context.Context) error) error {
		calls = append(calls, kind+":"+key)
		if blocked {
			return denied
		}
		return apply(ctx)
	}
	domain := registerFieldRoutes(r, pb, migration, nil, nil, nil, gate)
	registrations := schemaFieldChangeRegistrations(domain, schemaapi.New(pb))
	methods := map[string]productrpc.Registration{}
	for _, registration := range registrations {
		methods[registration.Method] = registration
	}
	invoke := func(method string, value any) (any, error) {
		t.Helper()
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		registration := methods[method]
		if err := registration.ValidateParams(raw); err != nil {
			t.Fatalf("%s params: %v", method, err)
		}
		return registration.Handler(context.Background(), raw)
	}
	actor := v2.Actor{ID: "local-user", Kind: "user"}
	create := v2.TableCreateIntent{DisplayName: "订单", OperationID: "schema-product-create", Actor: actor}
	value, err := invoke("schema.table.create", create)
	if err != nil {
		t.Fatal(err)
	}
	table := value.(v2.TableCreateReceipt)
	if table.TableID == "" || len(calls) != 1 || calls[0] != "schema.table.create:schema-product-create" {
		t.Fatalf("create: %#v %v", table, calls)
	}
	blocked = true
	replay, err := invoke("schema.table.create", create)
	if err != nil || replay.(v2.TableCreateReceipt) != table || len(calls) != 1 {
		t.Fatalf("replay crossed write gate: %#v %v %v", replay, err, calls)
	}
	blocked = false
	defaults, err := v2.RecommendedDefaults(v2.LogicalText)
	if err != nil {
		t.Fatal(err)
	}
	intent := v2.FieldChangeIntent{Action: v2.ActionCreate, TableID: table.TableID, ExpectedSchemaRev: table.SchemaRevision, Actor: actor,
		Draft: &v2.FieldDraft{DisplayName: "名称", LogicalType: v2.LogicalText, Value: defaults.Value, Constraints: defaults.Constraints, Storage: defaults.Storage, Display: defaults.Display}}
	value, err = invoke("field.change.plan", intent)
	if err != nil {
		t.Fatal(err)
	}
	plan := value.(v2.FieldChangePlan)
	if !plan.CanApply {
		t.Fatalf("plan: %#v", plan.Errors)
	}
	request := v2.ApplyRequest{PlanID: plan.PlanID, PlanHash: plan.PlanHash, OperationID: "schema-product-apply", Actor: actor, Confirmations: []string{}}
	blocked = true
	_, err = invoke("field.change.apply", request)
	var public *productrpc.PublicError
	if !errors.As(err, &public) || public.Code != "field.internal.failed" || !public.Retryable {
		t.Fatalf("write admission: %v", err)
	}
	fields, err := domain.catalog.Fields(context.Background(), table.TableID, true)
	if err != nil || len(fields) != 0 {
		t.Fatalf("denied apply wrote fields: %v %v", fields, err)
	}
	blocked = false
	value, err = invoke("field.change.apply", request)
	if err != nil {
		t.Fatal(err)
	}
	receipt := value.(v2.ApplyReceipt)
	if receipt.FieldID == "" {
		t.Fatal("no applied field")
	}
	value, err = invoke("field.recycleBin.list", map[string]string{"tableId": table.TableID})
	if err != nil {
		t.Fatal(err)
	}
	if fields := value.(recycledFieldsResult).Fields; fields == nil || len(fields) != 0 {
		t.Fatalf("recycle=%v", fields)
	}
	for _, method := range []string{"field.change.status", "field.change.cancel"} {
		_, err := invoke(method, map[string]string{"jobId": "missing_job"})
		if !errors.As(err, &public) || public.Code != "field.migration.not_found" {
			t.Fatalf("%s=%v", method, err)
		}
	}
	_, err = invoke("schema.delete", map[string]string{"tableId": table.TableID, "expectedRevision": table.SchemaRevision})
	if !errors.As(err, &public) || public.Code != "schema.revision_conflict" {
		t.Fatalf("stale revision not rejected: %v", err)
	}
	if _, err := domain.core.Describe(context.Background(), table.TableID); err != nil {
		t.Fatalf("stale delete removed table: %v", err)
	}
	value, err = invoke("schema.delete", map[string]string{"tableId": table.TableID, "expectedRevision": receipt.SchemaRevision})
	if err != nil || !value.(schemaapi.DeleteResult).Deleted {
		t.Fatalf("delete=%v %v", value, err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	raw, _ := json.Marshal(create)
	before := len(calls)
	if _, err := methods["schema.table.create"].Handler(cancelled, raw); !errors.Is(err, context.Canceled) || len(calls) != before {
		t.Fatalf("cancelled call: %v %v", err, calls)
	}
}

func TestSchemaFieldChangePublicErrorsPreserveRetryAndCancellation(t *testing.T) {
	source := &schemaerror.ProductError{Code: "schema.storage.failed", Path: "tableId", Message: "failed", Retryable: true}
	var projected *productrpc.PublicError
	if !errors.As(publicSchemaDeleteError(source), &projected) || !projected.Retryable {
		t.Fatalf("retryability lost: %#v", projected)
	}
	for _, project := range []func(error) error{publicSchemaDeleteError, publicFieldChangeError} {
		for _, cancelled := range []error{context.Canceled, context.DeadlineExceeded} {
			if !errors.Is(project(cancelled), cancelled) {
				t.Fatal("cancellation became a domain failure")
			}
		}
	}
}
