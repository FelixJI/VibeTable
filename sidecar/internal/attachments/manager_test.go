package attachments

import (
	"errors"
	"strings"
	"testing"

	"github.com/vibetable/vibetable/sidecar/internal/mutation"
)

func TestStageReferenceCountsIdenticalConcurrentUploads(t *testing.T) {
	manager, err := New([]byte(strings.Repeat("s", 32)))
	if err != nil {
		t.Fatal(err)
	}
	content := []byte("same content")
	if err := manager.Stage("request_handle", "same.txt", content); err != nil {
		t.Fatal(err)
	}
	if err := manager.Stage("request_handle", "same.txt", content); err != nil {
		t.Fatalf("identical concurrent stage: %v", err)
	}
	if got := manager.staged["request_handle"].references; got != 2 {
		t.Fatalf("references = %d, want 2", got)
	}
	manager.Drop("request_handle")
	if got := manager.staged["request_handle"].references; got != 1 {
		t.Fatalf("references after first drop = %d, want 1", got)
	}
	manager.Drop("request_handle")
	if len(manager.staged) != 0 || manager.bytes != 0 {
		t.Fatalf("stage was not released: %#v bytes=%d", manager.staged, manager.bytes)
	}
}

func TestStageRejectsConflictsAndUnsafeNames(t *testing.T) {
	manager, err := New([]byte(strings.Repeat("s", 32)))
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Stage("request_handle", "same.txt", []byte("first")); err != nil {
		t.Fatal(err)
	}
	err = manager.Stage("request_handle", "same.txt", []byte("second"))
	var productErr *mutation.ProductError
	if !errors.As(err, &productErr) ||
		productErr.Code != "mutation.attachment.handle_conflict" {
		t.Fatalf("conflict error = %#v", err)
	}
	for _, name := range []string{".", "bad\r\nname.txt", strings.Repeat("x", 256)} {
		if err := manager.Stage("unsafe_"+strings.Repeat("x", len(name)%3), name, []byte("x")); err == nil {
			t.Fatalf("unsafe filename %q was accepted", name)
		}
	}
}
