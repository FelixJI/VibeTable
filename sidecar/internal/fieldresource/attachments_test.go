package fieldresource

import (
	"errors"
	"fmt"
	"testing"

	"github.com/pocketbase/pocketbase/tools/filesystem/blob"
)

type fakeObjectFilesystem struct {
	exists    bool
	existsErr error
	deleteErr error
}

func (fsys fakeObjectFilesystem) Exists(string) (bool, error) {
	return fsys.exists, fsys.existsErr
}

func (fsys fakeObjectFilesystem) Delete(string) error {
	return fsys.deleteErr
}

func TestDeleteObjectIfPresentTreatsConcurrentRemovalAsComplete(t *testing.T) {
	fsys := fakeObjectFilesystem{
		exists:    true,
		deleteErr: fmt.Errorf("object removed concurrently: %w", blob.ErrNotFound),
	}

	if err := deleteObjectIfPresent(fsys, "attachments/report.pdf"); err != nil {
		t.Fatalf("expected concurrent removal to complete cleanup, got %v", err)
	}
}

func TestDeleteObjectIfPresentReturnsExistsError(t *testing.T) {
	inspectErr := errors.New("storage unavailable")
	fsys := fakeObjectFilesystem{existsErr: inspectErr}

	err := deleteObjectIfPresent(fsys, "attachments/report.pdf")
	if !errors.Is(err, inspectErr) {
		t.Fatalf("expected exists error, got %v", err)
	}
}

func TestDeleteObjectIfPresentReturnsOtherDeleteError(t *testing.T) {
	deleteErr := errors.New("permission denied")
	fsys := fakeObjectFilesystem{exists: true, deleteErr: deleteErr}

	err := deleteObjectIfPresent(fsys, "attachments/report.pdf")
	if !errors.Is(err, deleteErr) {
		t.Fatalf("expected delete error, got %v", err)
	}
}

func TestDeleteObjectIfPresentAcceptsAlreadyAbsentObject(t *testing.T) {
	fsys := fakeObjectFilesystem{
		deleteErr: errors.New("delete must not run for an absent object"),
	}

	if err := deleteObjectIfPresent(fsys, "attachments/report.pdf"); err != nil {
		t.Fatalf("expected absent object to complete cleanup, got %v", err)
	}
}

func TestDeleteObjectIfPresentDeletesExistingObject(t *testing.T) {
	fsys := fakeObjectFilesystem{exists: true}

	if err := deleteObjectIfPresent(fsys, "attachments/report.pdf"); err != nil {
		t.Fatalf("expected existing object deletion to complete cleanup, got %v", err)
	}
}
