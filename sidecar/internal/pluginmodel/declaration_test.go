package pluginmodel

import (
	"encoding/json"
	"os"
	"testing"
)

func TestDeclarationRejectsUnknownPhysicalAuthorityAndPrecision(t *testing.T) {
	raw, err := os.ReadFile("../../../examples/plugins/model-conformance/manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Permissions struct {
			Model json.RawMessage `json:"model"`
		} `json:"permissions"`
	}
	if err = json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	d, err := Decode(manifest.Permissions.Model)
	if err != nil {
		t.Fatal(err)
	}
	d.Entities[2].Fields[1].Precision = "decimal"
	if d.Validate() == nil {
		t.Fatal("unsupported decimal accepted")
	}
	var object map[string]any
	_ = json.Unmarshal(manifest.Permissions.Model, &object)
	object["sql"] = "CREATE TABLE x"
	raw, _ = json.Marshal(object)
	if _, err := Decode(raw); err == nil {
		t.Fatal("arbitrary schema accepted")
	}
}

func TestDeclarationOperationsAreExactClosedTokens(t *testing.T) {
	for _, op := range []string{"read query", "query create", "create update delete", " read", "query ", "", "READ", "*"} {
		t.Run(op, func(t *testing.T) {
			d := Declaration{Contract: Contract, ModelID: "example", Version: 1, Entities: []Entity{{Key: "items", Label: "Items", Operations: []string{"read", "query", op}, Fields: []Field{{Key: "name", Label: "Name", Type: "text"}}}}}
			if d.Validate() == nil {
				t.Fatalf("operation token %q accepted", op)
			}
		})
	}
	d := Declaration{Contract: Contract, ModelID: "example", Version: 1, Entities: []Entity{{Key: "items", Label: "Items", Operations: []string{"read", "query", "create", "update", "delete"}, Fields: []Field{{Key: "name", Label: "Name", Type: "text"}}}}}
	if err := d.Validate(); err != nil {
		t.Fatalf("closed operations rejected: %v", err)
	}
}
