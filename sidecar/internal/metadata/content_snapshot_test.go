package metadata

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/vibetable/vibetable/sidecar/internal/contracts/workbench"
)

func TestContentStoredSnapshotsKeepPayloadInsideOneJSONValue(t *testing.T) {
	profile := contentProfileRequest().Profile
	profile.TableId = "articles\" ,\"expectedRevision\":\"quoted\\table"
	link := contentLinkRequest().Link
	link.LinkId = "link\" ,\"expectedRevision\":\"quoted\\link"
	for _, test := range []struct {
		name     string
		payload  any
		snapshot func(Item) (any, error)
		want     any
	}{
		{"profile", profile, func(item Item) (any, error) { return profileSnapshot(item) }, workbench.ContentProfileSnapshot{Profile: profile, Revision: "stored-revision"}},
		{"link", link, func(item Item) (any, error) { return linkSnapshot(item) }, workbench.RecordDocumentLinkSnapshot{Link: link, Revision: "stored-revision"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			raw, err := json.Marshal(test.payload)
			if err != nil {
				t.Fatal(err)
			}
			got, err := test.snapshot(Item{Payload: raw, Revision: "stored-revision"})
			if err != nil || !reflect.DeepEqual(got, test.want) {
				t.Fatalf("valid quoted payload changed: %#v %v", got, err)
			}
			for _, malformed := range []string{string(raw) + `,"expectedRevision":"injected"`, string(raw) + `,"idempotencyKey":"injected"`, `"unterminated`, `null`, `[]`, `{}`} {
				_, err := test.snapshot(Item{Payload: json.RawMessage(malformed), Revision: "stored-revision"})
				var content *ContentError
				if !errors.As(err, &content) || content.Code != "content_model.storage_invalid" {
					t.Errorf("invalid standalone payload %q: want storage_invalid, got %v", malformed, err)
				}
			}
		})
	}
}
