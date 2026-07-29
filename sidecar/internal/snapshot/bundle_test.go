package snapshot

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/vibetable/vibetable/sidecar/internal/objectrepo"
)

type oversizedObjectRepository struct {
	objectrepo.Repository
	content string
}

func (repository oversizedObjectRepository) Open(
	context.Context,
	objectrepo.ObjectID,
) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader(repository.content)), nil
}

func TestBundleReadBudgetRejectsObjectBeforeUnboundedMaterialization(
	t *testing.T,
) {
	budget := bundleReadBudget{remaining: 8}
	_, err := budget.readObject(
		context.Background(),
		oversizedObjectRepository{content: "123456789"},
		"obj_unused",
	)
	if !errors.Is(err, ErrBundleResourceLimit) {
		t.Fatalf("oversized object error = %v", err)
	}
}

func TestValidateSnapshotBundleDataRejectsExcessiveEntryCount(t *testing.T) {
	objectMap := make(
		map[string]objectrepo.ObjectID,
		maxBundleEntries+1,
	)
	for index := 0; index <= maxBundleEntries; index++ {
		objectMap[string(rune(index+1))] = "obj_unused"
	}
	err := ValidateSnapshotBundleData(
		context.Background(),
		SnapshotBundle{Record: Record{ObjectMap: objectMap}},
	)
	if !errors.Is(err, ErrBundleResourceLimit) {
		t.Fatalf("entry limit error = %v", err)
	}
}

func TestClassifyBundleLoadErrorPreservesOperationalFailures(t *testing.T) {
	err := classifyBundleLoadError("manifest", context.Canceled)
	if !errors.Is(err, context.Canceled) ||
		errors.Is(err, ErrBundleInvalid) {
		t.Fatalf("operational error = %v", err)
	}
	err = classifyBundleLoadError("manifest", objectrepo.ErrCorrupt)
	if !errors.Is(err, ErrBundleInvalid) ||
		!errors.Is(err, objectrepo.ErrCorrupt) {
		t.Fatalf("corrupt error = %v", err)
	}
	err = classifyBundleLoadError(
		"manifest",
		errors.Join(objectrepo.ErrCorrupt, context.Canceled),
	)
	if !errors.Is(err, context.Canceled) ||
		errors.Is(err, ErrBundleInvalid) {
		t.Fatalf("joined cancellation error = %v", err)
	}
}

func TestWorkspaceSettingsBundleSchemaIsStrictAndLegacyCompatible(t *testing.T) {
	valid := []byte(`{
		"formatVersion":1,
		"retention":{
			"snapshotDays":30,
			"snapshotCount":50,
			"snapshotBuckets":["hourly","daily"],
			"fileRevisionDays":30,
			"fileRevisionCount":100,
			"fileRevisionBuckets":["daily"],
			"repositoryLimitBytes":null
		}
	}`)
	if err := validateWorkspaceSettingsObject(valid); err != nil {
		t.Fatalf("valid settings = %v", err)
	}
	if err := validateWorkspaceSettingsObject(
		[]byte(`{"theme":"legacy"}`),
	); err != nil {
		t.Fatalf("legacy settings = %v", err)
	}
	invalid := []byte(`{
		"formatVersion":1,
		"retention":{
			"snapshotDays":30,
			"snapshotCount":50,
			"snapshotBuckets":["daily"],
			"fileRevisionDays":30,
			"fileRevisionCount":100,
			"fileRevisionBuckets":["daily"],
			"repositoryLimitBytes":null,
			"theme":"dark"
		}
	}`)
	if err := validateWorkspaceSettingsObject(invalid); !errors.Is(
		err,
		ErrBundleInvalid,
	) {
		t.Fatalf("unknown setting accepted: %v", err)
	}
}
