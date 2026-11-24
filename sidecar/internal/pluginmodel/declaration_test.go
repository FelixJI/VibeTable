package pluginmodel

import (
	"encoding/json"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
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

func TestOutputFileMapsOnlyExactProtectedPolicy(t *testing.T) {
	f := Field{Key: "issued", Label: "Issued", Type: v2.LogicalFile, File: &FilePolicy{MaxFiles: 1, MaxBytesPerFile: 32 << 20, AllowedMIMETypes: []string{"image/png"}}}
	draft, err := f.Draft(nil)
	if err != nil {
		t.Fatal(err)
	}
	actual := v2.FieldDefinition{LogicalType: v2.LogicalFile, File: draft.File}
	actual.Lifecycle.State = v2.LifecycleActive
	if code := f.Compatibility(actual, nil); code != "" {
		t.Fatal(code)
	}
	for name, change := range map[string]func(*v2.FileSpec){
		"size": func(p *v2.FileSpec) { p.MaxBytesPerFile++ }, "count": func(p *v2.FileSpec) { p.MaxFiles++ },
		"mime":   func(p *v2.FileSpec) { p.AllowedMIMETypes = []string{"application/zip"} },
		"public": func(p *v2.FileSpec) { p.Protected = false }, "thumbnail": func(p *v2.FileSpec) { p.Thumbs = []string{"24x24"} },
	} {
		s := *draft.File
		change(&s)
		actual.File = &s
		if code := f.Compatibility(actual, nil); code != "plugin_model_file_policy_mismatch" {
			t.Errorf("%s: %s", name, code)
		}
	}
	for _, policy := range []FilePolicy{
		{0, 1, []string{"image/png"}}, {2, 1, []string{"image/png"}}, {1, 0, []string{"image/png"}},
		{1, (32 << 20) + 1, []string{"image/png"}}, {1, 1, []string{"image/*"}}, {1, 1, nil}, {1, 1, []string{"image/png", "image/jpeg"}},
	} {
		if policy.valid() {
			t.Errorf("accepted invalid policy %+v", policy)
		}
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
