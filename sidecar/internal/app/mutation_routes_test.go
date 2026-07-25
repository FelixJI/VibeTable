package app

import (
	"bytes"
	"errors"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/vibetable/vibetable/sidecar/internal/mutation"
)

type recordingUploadStager struct {
	files   map[string][]byte
	names   map[string]string
	dropped []string
	err     error
}

func (stager *recordingUploadStager) Stage(
	handle, originalName string,
	content []byte,
) error {
	if stager.err != nil {
		return stager.err
	}
	if stager.files == nil {
		stager.files = map[string][]byte{}
		stager.names = map[string]string{}
	}
	stager.files[handle] = append([]byte(nil), content...)
	stager.names[handle] = originalName
	return nil
}

func (stager *recordingUploadStager) Drop(handles ...string) {
	stager.dropped = append(stager.dropped, handles...)
}

func TestDecodeMutationApplyRequestAcceptsMultipartUploads(t *testing.T) {
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	requestPart, err := writer.CreateFormField("request")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = requestPart.Write([]byte(
		`{"contractVersion":"1.0","requestId":"request-1","idempotencyKey":"key-1",` +
			`"tableId":"notes","schemaRevision":"revision-1","operations":[` +
			`{"kind":"setAttachments","recordId":"record-1","fieldId":"files",` +
			`"uploadHandles":["upload-1"],"removeStoredNames":[]}],` +
			`"actor":{"type":"user","id":"local","displayName":null},` +
			`"expectedRevision":null,"expectedDigest":null}`,
	))
	filePart, err := writer.CreateFormFile("upload:upload-1", "notes.txt")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = filePart.Write([]byte("managed attachment"))
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest("POST", "/mutations/apply", body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	stager := &recordingUploadStager{}

	decoded, handles, err := decodeMutationApplyRequest(request, stager)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.TableID != "notes" ||
		len(decoded.Operations) != 1 ||
		decoded.Operations[0].Kind != mutation.OperationSetAttachments {
		t.Fatalf("decoded request = %#v", decoded)
	}
	if len(handles) != 1 ||
		decoded.Operations[0].UploadHandles[0] != handles[0] ||
		handles[0] == "upload-1" {
		t.Fatalf("staged handles = %#v", handles)
	}
	if string(stager.files[handles[0]]) != "managed attachment" ||
		stager.names[handles[0]] != "notes.txt" {
		t.Fatalf("staged upload = %#v / %#v", stager.files, stager.names)
	}
}

func TestDecodeMultipartMutationCleansUpOnInvalidRequest(t *testing.T) {
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	filePart, err := writer.CreateFormFile("upload:upload-1", "notes.txt")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = filePart.Write([]byte("managed attachment"))
	requestPart, err := writer.CreateFormField("request")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = requestPart.Write([]byte(`{"unknown":true}`))
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	stager := &recordingUploadStager{}

	_, _, err = decodeMultipartMutation(body, writer.Boundary(), stager)
	var productErr *mutation.ProductError
	if !errors.As(err, &productErr) ||
		productErr.Code != "mutation.request.invalid" {
		t.Fatalf("error = %#v", err)
	}
	if len(stager.dropped) != 0 {
		t.Fatalf("dropped handles = %#v", stager.dropped)
	}
}

func TestDecodeMutationApplyRequestRequiresAttachmentServiceForMultipart(
	t *testing.T,
) {
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest("POST", "/mutations/apply", body)
	request.Header.Set("Content-Type", writer.FormDataContentType())

	_, _, err := decodeMutationApplyRequest(request, nil)
	var productErr *mutation.ProductError
	if !errors.As(err, &productErr) ||
		productErr.Code != "mutation.request.invalid" {
		t.Fatalf("error = %#v", err)
	}
}

func TestDecodeMultipartMutationRejectsMissingAndUnreferencedUploads(t *testing.T) {
	validRequest := `{"contractVersion":"1.0","requestId":"request-1",` +
		`"idempotencyKey":"key-1","tableId":"notes","schemaRevision":"revision-1",` +
		`"operations":[{"kind":"setAttachments","recordId":"record-1",` +
		`"fieldId":"files","uploadHandles":["upload-1"],"removeStoredNames":[]}],` +
		`"actor":{"type":"user","id":"local","displayName":null},` +
		`"expectedRevision":null,"expectedDigest":null}`
	for _, test := range []struct {
		name       string
		fileHandle string
	}{
		{name: "missing upload"},
		{name: "unreferenced upload", fileHandle: "other"},
	} {
		t.Run(test.name, func(t *testing.T) {
			body := &bytes.Buffer{}
			writer := multipart.NewWriter(body)
			requestPart, err := writer.CreateFormField("request")
			if err != nil {
				t.Fatal(err)
			}
			_, _ = requestPart.Write([]byte(validRequest))
			if test.fileHandle != "" {
				filePart, err := writer.CreateFormFile(
					"upload:"+test.fileHandle, "notes.txt",
				)
				if err != nil {
					t.Fatal(err)
				}
				_, _ = filePart.Write([]byte("managed attachment"))
			}
			if err := writer.Close(); err != nil {
				t.Fatal(err)
			}
			stager := &recordingUploadStager{}
			_, _, err = decodeMultipartMutation(body, writer.Boundary(), stager)
			var productErr *mutation.ProductError
			if !errors.As(err, &productErr) ||
				productErr.Code != "mutation.request.invalid" ||
				len(stager.files) != 0 {
				t.Fatalf("error=%#v staged=%#v", err, stager.files)
			}
		})
	}
}

func TestMutationErrorStatusMapsAttachmentErrors(t *testing.T) {
	for code, want := range map[string]int{
		"attachment.request.invalid":  http.StatusBadRequest,
		"relation.request.invalid":    http.StatusBadRequest,
		"attachment.file_missing":     http.StatusNotFound,
		"attachment.metadata_missing": http.StatusNotFound,
		"attachment.storage_failed":   http.StatusInternalServerError,
	} {
		t.Run(code, func(t *testing.T) {
			if got := mutationErrorStatus(&mutation.ProductError{Code: code}); got != want {
				t.Fatalf("status = %d, want %d", got, want)
			}
		})
	}
}

func TestInternalUploadHandleIsStableAndContentBound(t *testing.T) {
	upload := multipartUpload{
		handle: "client-handle", originalName: "notes.txt",
		content: []byte("managed attachment"),
	}
	first := internalUploadHandle("request-1", upload)
	if second := internalUploadHandle("request-1", upload); second != first {
		t.Fatalf("handle was not stable: %q != %q", second, first)
	}
	upload.content = []byte("different content")
	if changed := internalUploadHandle("request-1", upload); changed == first {
		t.Fatal("content change did not change internal upload handle")
	}
}
