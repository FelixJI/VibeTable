package filehistory

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

// TestEncodeRootPayloadMatchesLegacyMarshalBytes proves the two-pass streamed
// root encoding produces byte-identical output to the previous
// json.Marshal(rootPayload) across the payload shapes the durable roots
// actually contain: representative document/revision histories with Chinese
// and HTML escaping, optional pointer fields both nil and set, restore and
// provisional metadata, plus nil and empty document slices.
func TestEncodeRootPayloadMatchesLegacyMarshalBytes(t *testing.T) {
	createdAt := time.Date(
		2026, time.October, 1, 12, 0, 0, 123456789, time.UTC,
	)
	parentID := "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	restoredFrom := "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	comment := "<script>中文&\"引号\"😀</script>"
	localSequence := uint64(7)
	formalVersion := uint64(3)
	representative := rootPayload{
		FormatVersion: rootFormatVersion,
		WorkspaceID:   testWorkspaceID,
		Documents: []Document{{
			ContractVersion:     contractVersion,
			WorkspaceID:         testWorkspaceID,
			DocumentID:          testDocumentOne,
			RelativePath:        "报表/2026/q3✓.txt",
			Status:              DocumentActive,
			TopologyRevision:    2,
			EffectiveRevisionID: testDocumentTwo,
			NextRevisionOrdinal: 4,
			NextFormalVersion:   3,
			Revisions: []Revision{
				{
					ContractVersion:        contractVersion,
					RevisionID:             "cccccccc-cccc-4ccc-8ccc-cccccccccccc",
					DocumentID:             testDocumentOne,
					Kind:                   RevisionRestore,
					RestoredFromRevisionID: &restoredFrom,
					ParentRevisionID:       &parentID,
					RevisionOrdinal:        2,
					FormalVersion:          &formalVersion,
					LocalSequence:          &localSequence,
					ObjectID:               contentObjectID([]byte("恢复")),
					ContentHash:            contentHash([]byte("恢复")),
					Size:                   int64(len("恢复")),
					MimeType:               "text/plain",
					CreatedAt:              createdAt,
					CreatedBy:              "测试用户<admin>&",
					DeviceID:               testDeviceID,
					Comment:                &comment,
				},
				{
					ContractVersion:  contractVersion,
					RevisionID:       "dddddddd-dddd-4ddd-8ddd-dddddddddddd",
					DocumentID:       testDocumentOne,
					ParentRevisionID: &restoredFrom,
					Kind:             RevisionAutosave,
					RevisionOrdinal:  3,
					ObjectID:         contentObjectID(nil),
					ContentHash:      contentHash(nil),
					MimeType:         "text/plain",
					CreatedAt:        createdAt.Add(time.Nanosecond),
					CreatedBy:        "tester",
					DeviceID:         testDeviceID,
				},
			},
		}},
	}
	for name, payload := range map[string]rootPayload{
		"nil documents": {
			FormatVersion: rootFormatVersion,
			WorkspaceID:   testWorkspaceID,
		},
		"empty documents": {
			FormatVersion: rootFormatVersion,
			WorkspaceID:   testWorkspaceID,
			Documents:     []Document{},
		},
		"representative history": representative,
	} {
		t.Run(name, func(t *testing.T) {
			legacy, legacyErr := json.Marshal(payload)
			if legacyErr != nil {
				t.Fatal(legacyErr)
			}
			streamed, streamedErr := encodeRootPayload(payload)
			if streamedErr != nil {
				t.Fatal(streamedErr)
			}
			if !bytes.Equal(legacy, streamed) {
				t.Fatalf(
					"streamed root encoding differs:\nlegacy:  %s\nstreamed: %s",
					legacy, streamed,
				)
			}
			// Old-record reading must consume the streamed bytes identically.
			var decoded rootPayload
			if err := decodeStrict(streamed, &decoded); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(payload, decoded) {
				t.Fatalf(
					"round trip mismatch: %#v != %#v",
					payload, decoded,
				)
			}
		})
	}
}
