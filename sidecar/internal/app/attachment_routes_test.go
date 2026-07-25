package app

import (
	"net/url"
	"testing"
)

func TestStrictAttachmentQuery(t *testing.T) {
	valid, err := strictAttachmentQuery(
		url.Values{
			"tableId":    {"table-1"},
			"recordId":   {"record-1"},
			"fieldId":    {"field-1"},
			"storedName": {"stored.txt"},
			"variant":    {"100x100"},
		},
		[]string{"tableId", "recordId", "fieldId", "storedName"},
		[]string{"variant"},
	)
	if err != nil || valid["variant"] != "100x100" {
		t.Fatalf("valid query = %#v, err=%v", valid, err)
	}

	for name, values := range map[string]url.Values{
		"missing": {
			"tableId": {"table-1"},
		},
		"duplicate": {
			"tableId":    {"table-1"},
			"recordId":   {"record-1"},
			"fieldId":    {"field-1"},
			"storedName": {"a.txt", "b.txt"},
		},
		"unknown": {
			"tableId":    {"table-1"},
			"recordId":   {"record-1"},
			"fieldId":    {"field-1"},
			"storedName": {"a.txt"},
			"path":       {"storage/private"},
		},
		"empty optional": {
			"tableId":    {"table-1"},
			"recordId":   {"record-1"},
			"fieldId":    {"field-1"},
			"storedName": {"a.txt"},
			"variant":    {""},
		},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := strictAttachmentQuery(
				values,
				[]string{"tableId", "recordId", "fieldId", "storedName"},
				[]string{"variant"},
			); err == nil {
				t.Fatal("invalid query was accepted")
			}
		})
	}
}
