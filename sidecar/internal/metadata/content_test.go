package metadata

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"sync"
	"testing"

	"github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/core"
	"github.com/vibetable/vibetable/sidecar/internal/contracts/workbench"
	"github.com/vibetable/vibetable/sidecar/internal/query"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
	"github.com/vibetable/vibetable/sidecar/migrations"
)

type contentFixtureSource struct {
	t    *testing.T
	root core.App
}

func (s contentFixtureSource) DescribeQueryTable(ctx context.Context, app core.App, table string) (query.TableDescriptor, error) {
	s.t.Helper()
	if app == s.root {
		s.t.Fatal("record validation escaped the metadata transaction")
	}
	if table != "articles" {
		return query.TableDescriptor{}, errors.New("missing table")
	}
	return query.TableDescriptor{DatabaseID: "content-test", TableID: table, PhysicalName: "content_articles", PrimaryKey: "business_key", SchemaRevision: "schema-1", DataRevision: 1, Fields: map[string]query.FieldDescriptor{"business_key": {PhysicalName: "business_key", Type: query.FieldTypeText}}}, nil
}
func (s contentFixtureSource) DescribeSelectionTable(ctx context.Context, app core.App, table string) (query.TableDescriptor, v2.SchemaSnapshot, error) {
	d, err := s.DescribeQueryTable(ctx, app, table)
	return d, v2.SchemaSnapshot{}, err
}
func contentFixture(t *testing.T) (*pocketbase.PocketBase, *ContentService) {
	t.Helper()
	pb := pocketbase.NewWithConfig(pocketbase.Config{DefaultDataDir: t.TempDir(), HideStartBanner: true})
	migrations.Register(pb)
	if err := pb.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		event := &core.TerminateEvent{App: pb}
		if err := pb.OnTerminate().Trigger(event, func(e *core.TerminateEvent) error { return e.App.ResetBootstrapState() }); err != nil {
			t.Error(err)
		}
	})
	if err := pb.RunAllMigrations(); err != nil {
		t.Fatal(err)
	}
	collection := core.NewBaseCollection("content_articles")
	collection.Fields.Add(&core.TextField{Name: "business_key"})
	if err := pb.Save(collection); err != nil {
		t.Fatal(err)
	}
	record := core.NewRecord(collection)
	record.Set("business_key", "record-1")
	if err := pb.Save(record); err != nil {
		t.Fatal(err)
	}
	if record.Id == "record-1" {
		t.Fatal("fixture must distinguish physical and business identity")
	}
	service := NewContentService(pb, contentFixtureSource{t, pb})
	service.describe = func(ctx context.Context, tx core.App, table string) (v2.SchemaSnapshot, error) {
		if tx == pb {
			t.Fatal("profile validation escaped metadata transaction")
		}
		if table != "articles" {
			return v2.SchemaSnapshot{}, errors.New("missing table")
		}
		return v2.SchemaSnapshot{Fields: []v2.FieldDefinition{
			{Identity: v2.FieldIdentity{FieldID: "fld_title000"}, LogicalType: v2.LogicalText},
			{Identity: v2.FieldIdentity{FieldID: "fld_body0000"}, LogicalType: v2.LogicalEditor},
			{Identity: v2.FieldIdentity{FieldID: "fld_secret00"}, LogicalType: v2.LogicalJSON},
		}}, nil
	}
	return pb, service
}

func invokeContent(service *ContentService, ctx context.Context, request any) (any, error) {
	switch value := request.(type) {
	case workbench.ContentProfileLoadRequest:
		return service.LoadProfile(ctx, value.TableId)
	case workbench.ContentProfileCommitRequest:
		return service.CommitProfile(ctx, value)
	case workbench.ContentProfileDeleteRequest:
		return service.DeleteProfile(ctx, value)
	case workbench.RecordDocumentLinkListRequest:
		return service.ListLinks(ctx, value.TableId, value.RecordId)
	case workbench.RecordDocumentLinkCommitRequest:
		return service.CommitLink(ctx, value)
	case workbench.RecordDocumentLinkRepairRequest:
		return service.RepairLink(ctx, value)
	case workbench.RecordDocumentLinkDeleteRequest:
		return service.DeleteLink(ctx, value)
	}
	return nil, errors.New("unexpected content command")
}
func TestContentMetadataMatchesFrozenPythonCorpus(t *testing.T) {
	raw, err := os.ReadFile("../../../contracts/v2/content-metadata-python-oracle.json")
	if err != nil {
		t.Fatal(err)
	}
	var corpus struct {
		Producer string `json:"producerCommit"`
		Cases    []struct {
			Name, Method string
			Params       json.RawMessage
			Seed         []struct {
				Namespace Namespace
				LogicalID string `json:"logicalId"`
				Payload   json.RawMessage
			}
			Response struct {
				Result json.RawMessage
				Error  *struct {
					Code int
					Data map[string]any
				}
			}
		}
	}
	if err := json.Unmarshal(raw, &corpus); err != nil {
		t.Fatal(err)
	}
	if corpus.Producer != "79c2ce4faa53d65af1fa9ee297655739d5407a2e" || len(corpus.Cases) != 28 {
		t.Fatal("unexpected fixed producer corpus")
	}
	for _, sample := range corpus.Cases {
		t.Run(sample.Name, func(t *testing.T) {
			params, err := DecodeContentParams(sample.Method, sample.Params)
			if sample.Response.Error != nil && sample.Response.Error.Code == -32602 {
				if err == nil {
					t.Fatal("accepted invalid DTO")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			_, service := contentFixture(t)
			for _, seed := range sample.Seed {
				if _, err := service.store.Upsert(context.Background(), UpsertRequest{Namespace: seed.Namespace, LogicalID: seed.LogicalID, Payload: seed.Payload, IdempotencyKey: "seed-" + seed.LogicalID}); err != nil {
					t.Fatal(err)
				}
			}
			result, err := invokeContent(service, context.Background(), params)
			if sample.Response.Error != nil {
				var domain *ContentError
				if !errors.As(err, &domain) {
					t.Fatalf("expected ContentError, got %v", err)
				}
				want := sample.Response.Error.Data
				if domain.Code != want["code"] || domain.Message != want["message"] {
					t.Fatalf("error = %#v, want %#v", domain, want)
				}
				if path, ok := want["path"]; ok && (domain.Path == nil || *domain.Path != path) {
					t.Fatalf("path=%v want %s", domain.Path, path)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			actual, _ := json.Marshal(result)
			var got, want any
			if err := json.Unmarshal(actual, &got); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(sample.Response.Result, &want); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("got %s want %s", actual, sample.Response.Result)
			}
		})
	}
}

func contentProfileRequest() workbench.ContentProfileCommitRequest {
	return workbench.ContentProfileCommitRequest{Profile: workbench.ContentProfile{ContractVersion: "1.0", TableId: "articles", TitleFieldId: "fld_title000", BodyFieldId: "fld_body0000", SearchableFieldIds: []string{"fld_title000", "fld_body0000"}}, IdempotencyKey: "profile-create"}
}
func contentLinkRequest() workbench.RecordDocumentLinkCommitRequest {
	return workbench.RecordDocumentLinkCommitRequest{Link: workbench.RecordDocumentLink{ContractVersion: "1.0", LinkId: "link-1", TableId: "articles", RecordId: "record-1", DocumentId: "22222222-2222-4222-8222-222222222222", Role: "reference", Order: 1}, IdempotencyKey: "link-create"}
}
func countContentRows(t *testing.T, pb core.App, collection string) int {
	t.Helper()
	rows, err := pb.FindAllRecords(collection)
	if err != nil {
		t.Fatal(err)
	}
	return len(rows)
}

func TestContentCommandsReplayBeforeCurrentStateValidation(t *testing.T) {
	pb, service := contentFixture(t)
	ctx := context.Background()
	profile := contentProfileRequest()
	created, err := service.CommitProfile(ctx, profile)
	if err != nil {
		t.Fatal(err)
	}
	// A successful retry does not revalidate a schema which has since disappeared.
	service.describe = func(context.Context, core.App, string) (v2.SchemaSnapshot, error) {
		t.Fatal("schema lookup on replay")
		return v2.SchemaSnapshot{}, nil
	}
	replay, err := service.CommitProfile(ctx, profile)
	if err != nil || !reflect.DeepEqual(created, replay) {
		t.Fatalf("profile replay %#v %v", replay, err)
	}
	link := contentLinkRequest()
	first, err := service.CommitLink(ctx, link)
	if err != nil {
		t.Fatal(err)
	}
	repair := workbench.RecordDocumentLinkRepairRequest{LinkId: link.Link.LinkId, DocumentId: "33333333-3333-4333-8333-333333333333", ExpectedRevision: first.Revision, IdempotencyKey: "repair"}
	fixed, err := service.RepairLink(ctx, repair)
	if err != nil {
		t.Fatal(err)
	}
	if fixed.Link.RecordId != link.Link.RecordId || fixed.Link.Role != link.Link.Role {
		t.Fatal("repair replaced unrelated fields")
	}
	counts := []int{countContentRows(t, pb, "vibetable_outbox"), countContentRows(t, pb, "vibetable_audit_events")}
	// Close and reopen the database before replay: no in-memory receipt can survive.
	if err := pb.ResetBootstrapState(); err != nil {
		t.Fatal(err)
	}
	if err := pb.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	restarted := NewContentService(pb, nil)
	old, err := restarted.CommitLink(ctx, link)
	if err != nil || !reflect.DeepEqual(old, first) {
		t.Fatalf("commit replay after repair: %#v %v", old, err)
	}
	fixedReplay, err := restarted.RepairLink(ctx, repair)
	if err != nil || !reflect.DeepEqual(fixedReplay, fixed) {
		t.Fatalf("repair replay: %#v %v", fixedReplay, err)
	}
	if counts[0] != countContentRows(t, pb, "vibetable_outbox") || counts[1] != countContentRows(t, pb, "vibetable_audit_events") {
		t.Fatal("replay duplicated trace")
	}
	deletion := workbench.RecordDocumentLinkDeleteRequest{LinkId: link.Link.LinkId, ExpectedRevision: fixed.Revision, IdempotencyKey: "delete-link"}
	deleted, err := service.DeleteLink(ctx, deletion)
	if err != nil {
		t.Fatal(err)
	}
	again, err := restarted.DeleteLink(ctx, deletion)
	if err != nil || again != deleted {
		t.Fatalf("delete replay: %#v %v", again, err)
	}
	profileDelete := workbench.ContentProfileDeleteRequest{TableId: "articles", ExpectedRevision: created.Revision, IdempotencyKey: "delete-profile"}
	if _, err := service.DeleteProfile(ctx, profileDelete); err != nil {
		t.Fatal(err)
	}
	if _, err := restarted.DeleteProfile(ctx, profileDelete); err != nil {
		t.Fatalf("profile delete replay: %v", err)
	}
	changed := repair
	changed.DocumentId = "44444444-4444-4444-8444-444444444444"
	_, err = restarted.RepairLink(ctx, changed)
	var conflict *ContentError
	if !errors.As(err, &conflict) || conflict.Code != "content_model.idempotency_conflict" {
		t.Fatalf("changed replay: %v", err)
	}
}

func TestContentConcurrentCASAndTraceRollback(t *testing.T) {
	pb, service := contentFixture(t)
	ctx := context.Background()
	request := contentProfileRequest()
	created, err := service.CommitProfile(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	var wait sync.WaitGroup
	results := make(chan error, 2)
	for _, key := range []string{"writer-a", "writer-b"} {
		wait.Add(1)
		go func() {
			defer wait.Done()
			next := contentProfileRequest()
			next.ExpectedRevision = &created.Revision
			next.IdempotencyKey = key
			next.Profile.SearchableFieldIds = []string{"fld_title000"}
			if key == "writer-b" {
				next.Profile.SearchableFieldIds = []string{"fld_body0000"}
			}
			_, err := service.CommitProfile(ctx, next)
			results <- err
		}()
	}
	wait.Wait()
	close(results)
	success, conflicts := 0, 0
	for err := range results {
		if err == nil {
			success++
		} else {
			var domain *ContentError
			if !errors.As(err, &domain) || domain.Code != "content_profile.edit_conflict" {
				t.Fatal(err)
			}
			conflicts++
		}
	}
	if success != 1 || conflicts != 1 {
		t.Fatalf("CAS results %d/%d", success, conflicts)
	}
	rows := countContentRows(t, pb, "vibetable_idempotency_keys")
	events := countContentRows(t, pb, "vibetable_outbox")
	// Force the existing outbox uniqueness failure after the business row is written.
	var fixedID string
	outbox, _ := pb.FindAllRecords("vibetable_outbox")
	fixedID = outbox[0].GetString("event_id")
	originalGenerator := service.store.newID
	service.store.newID = func(kind string) string {
		if kind == "event" {
			return fixedID
		}
		return originalGenerator(kind)
	}
	_, err = service.CommitLink(ctx, contentLinkRequest())
	var failure *ContentError
	if !errors.As(err, &failure) || failure.Code != "content_model.persistence_failed" {
		t.Fatalf("trace failure=%v", err)
	}
	if countContentRows(t, pb, "vibetable_record_document_links") != 0 || rows != countContentRows(t, pb, "vibetable_idempotency_keys") || events != countContentRows(t, pb, "vibetable_outbox") {
		t.Fatal("trace failure did not roll back business and idempotency")
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := service.CommitLink(canceled, contentLinkRequest()); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation=%v", err)
	}
}

func TestContentEmptyRevisionDoesNotMeanCreate(t *testing.T) {
	pb, service := contentFixture(t)
	empty := ""
	profile := contentProfileRequest()
	profile.ExpectedRevision = &empty
	_, err := service.CommitProfile(context.Background(), profile)
	var domain *ContentError
	if !errors.As(err, &domain) || domain.Code != "content_profile.edit_conflict" {
		t.Fatalf("profile empty revision = %v", err)
	}
	link := contentLinkRequest()
	link.ExpectedRevision = &empty
	_, err = service.CommitLink(context.Background(), link)
	if !errors.As(err, &domain) || domain.Code != "record_document_link.edit_conflict" {
		t.Fatalf("link empty revision = %v", err)
	}
	if countContentRows(t, pb, "vibetable_idempotency_keys") != 0 {
		t.Fatal("rejected empty revision left an operation")
	}
}

func TestContentParamsPreserveAliasesAndIntegralOrder(t *testing.T) {
	for _, order := range []string{`1`, `1.0`, `"1"`, `"1.0"`, `"+01.00"`, `true`} {
		raw := json.RawMessage(`{"link":{"contract_version":"1.0","link_id":"link-1","table_id":"articles","record_id":"record-1","document_id":"22222222-2222-4222-8222-222222222222","role":"reference","order":` + order + `},"expected_revision":null,"idempotency_key":"create"}`)
		request, err := DecodeContentParams("recordDocumentLink.commit", raw)
		if err != nil || request.(workbench.RecordDocumentLinkCommitRequest).Link.Order != 1 {
			t.Fatalf("order=%s request=%#v error=%v", order, request, err)
		}
	}
	for _, raw := range []string{`{"tableId":null}`, `{"tableId":"articles","table_id":"articles"}`, `{"tableId":"articles","extra":null}`} {
		if _, err := DecodeContentParams("contentProfile.load", json.RawMessage(raw)); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
}

func TestContentOrderStringsRejectNonIntegralAndNonDecimalSyntax(t *testing.T) {
	for _, order := range []string{`"1e0"`, `"0x1p0"`, `"1.00000000000000001"`, `"1__0"`, `"1.0_0"`} {
		raw := json.RawMessage(`{"link":{"contractVersion":"1.0","linkId":"link-1","tableId":"articles","recordId":"record-1","documentId":"22222222-2222-4222-8222-222222222222","role":"reference","order":` + order + `},"expectedRevision":null,"idempotencyKey":"create"}`)
		if _, err := DecodeContentParams("recordDocumentLink.commit", raw); err == nil {
			t.Fatalf("accepted invalid order %s", order)
		}
	}
}
