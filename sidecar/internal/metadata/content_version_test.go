package metadata

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/core"
	"github.com/vibetable/vibetable/sidecar/migrations"
)

func TestVersionReceiptKeepsOriginalLargeJSONNumbersAcrossPocketBaseReopen(t *testing.T) {
	root := t.TempDir()
	open := func() *pocketbase.PocketBase {
		pb := pocketbase.NewWithConfig(pocketbase.Config{DefaultDataDir: root, HideStartBanner: true})
		migrations.Register(pb)
		if err := pb.Bootstrap(); err != nil {
			t.Fatal(err)
		}
		if err := pb.RunAllMigrations(); err != nil {
			t.Fatal(err)
		}
		return pb
	}
	close := func(pb *pocketbase.PocketBase) {
		event := &core.TerminateEvent{App: pb}
		if err := pb.OnTerminate().Trigger(event, func(e *core.TerminateEvent) error { return e.App.ResetBootstrapState() }); err != nil {
			t.Fatal(err)
		}
	}
	pb := open()
	owner := NewContentVersions(pb, nil, nil)
	raw := json.RawMessage(`{"promoted":"v1","result":{"item":{"n":9007199254740993,"nested":[9223372036854775807,false,null]}}}`)
	if err := owner.storeReceipt(pb, "metadata:version.promote:large", "digest", raw); err != nil {
		t.Fatal(err)
	}
	close(pb)
	pb = open()
	defer close(pb)
	owner = NewContentVersions(pb, nil, nil)
	result, found, err := owner.loadReceipt(pb, "metadata:version.promote:large", "digest")
	if err != nil || !found || string(result) != string(raw) {
		t.Fatalf("receipt precision: %s %v %v", result, found, err)
	}
	if _, _, err := owner.loadReceipt(pb, "metadata:version.promote:large", "different"); err == nil {
		t.Fatal("changed payload accepted")
	}
	if _, found, err := owner.loadReceipt(pb, "absent", "digest"); err != nil || found {
		t.Fatalf("missing: %v %v", found, err)
	}
	_, err = owner.Write(context.Background(), "version.save", VersionParams{Values: map[string]json.RawMessage{"draft": json.RawMessage(`false`)}})
	if err == nil {
		t.Fatal("arbitrary draft accepted")
	}
}

func TestVersionClosedDTOUsesCanonicalAliasesAndRequiredCAS(t *testing.T) {
	for _, method := range []string{"version.list", "version.create", "version.compare", "version.save", "version.promote", "version.delete"} {
		p := map[string]any{"collection": "table", "itemId": "row"}
		if method != "version.list" && method != "version.create" {
			p["versionId"] = "version"
		}
		if method != "version.list" && method != "version.compare" {
			p["operationId"] = "operation"
		}
		if method == "version.save" || method == "version.promote" || method == "version.delete" {
			p["expectedRevision"] = "revision"
		}
		if method == "version.promote" {
			p["mainHash"] = "audit-cas"
		}
		raw, _ := json.Marshal(p)
		if _, err := DecodeVersionParams(method, raw); err != nil {
			t.Fatalf("%s: %v", method, err)
		}
		p["unknown"] = false
		raw, _ = json.Marshal(p)
		if _, err := DecodeVersionParams(method, raw); err == nil {
			t.Fatalf("%s accepted unknown", method)
		}
		delete(p, "unknown")
		for _, field := range []string{"collection", "itemId", "versionId", "operationId", "expectedRevision", "mainHash"} {
			if _, present := p[field]; !present {
				continue
			}
			copy := map[string]any{}
			for k, v := range p {
				copy[k] = v
			}
			delete(copy, field)
			raw, _ := json.Marshal(copy)
			if _, err := DecodeVersionParams(method, raw); err == nil {
				t.Fatalf("%s accepted missing %s", method, field)
			}
		}
	}
	aliases := json.RawMessage(`{"collection":"table","item_id":"row","version_id":"v","expected_revision":"r","operation_id":"op"}`)
	if p, err := DecodeVersionParams("version.save", aliases); err != nil || p.ExpectedRevision != "r" || p.ItemID != "row" {
		t.Fatalf("aliases: %+v %v", p, err)
	}
	for _, raw := range []string{`null`, `[]`, `{"collection":false,"itemId":"row"}`, `{"collection":"table","itemId":null}`, `{"collection":"table","itemId":"row","item_id":"other"}`, `{"collection":"` + strings.Repeat("x", 129) + `","itemId":"row"}`} {
		if _, err := DecodeVersionParams("version.list", json.RawMessage(raw)); err == nil {
			t.Fatalf("invalid DTO accepted: %s", raw)
		}
	}
	if _, err := DecodeVersionParams("version.unknown", json.RawMessage(`{}`)); err == nil {
		t.Fatal("unknown method")
	}
	for _, value := range []string{"null", "[]", "false", "1"} {
		raw := `{"collection":"t","itemId":"r","versionId":"v","operationId":"o","expectedRevision":"e","values":` + value + `}`
		if _, err := DecodeVersionParams("version.save", json.RawMessage(raw)); err == nil {
			t.Fatal("nonobject values")
		}
	}
}

func TestVersionDTOReplaysFrozenPythonAdmissionWithExplicitCASAddition(t *testing.T) {
	raw, err := os.ReadFile("../../../contracts/v2/content-version-python-oracle.json")
	if err != nil {
		t.Fatal(err)
	}
	var corpus struct {
		Producer string
		Cases    []struct {
			Name, Method string
			Params       json.RawMessage
			Responses    []struct {
				Error  *struct{ Code int }
				Result json.RawMessage
			}
		}
	}
	if err := json.Unmarshal(raw, &corpus); err != nil {
		t.Fatal(err)
	}
	if corpus.Producer != "4a078f84156ece7f48a1fe385cb9c5740fce654c" || len(corpus.Cases) != 55 {
		t.Fatal("frozen producer or count changed")
	}
	for _, sample := range corpus.Cases {
		t.Run(sample.Name, func(t *testing.T) {
			input := sample.Params
			// The legacy DTO had no save/promote CAS. Freeze old admission separately
			// from the explicitly approved required revision contract.
			if sample.Method == "version.save" || sample.Method == "version.promote" {
				var fields map[string]json.RawMessage
				if json.Unmarshal(input, &fields) == nil && fields != nil {
					fields["expectedRevision"] = json.RawMessage(`"frozen-version-revision"`)
					input, _ = json.Marshal(fields)
				}
			}
			_, err := DecodeVersionParams(sample.Method, input)
			invalid := sample.Responses[0].Error != nil && (sample.Responses[0].Error.Code == -32602 || sample.Responses[0].Error.Code == -32600)
			if (err != nil) != invalid {
				t.Fatalf("frozen admission drift: invalid=%v err=%v params=%s", invalid, err, input)
			}
		})
	}
}
