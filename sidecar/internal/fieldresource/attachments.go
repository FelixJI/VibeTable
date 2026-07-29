package fieldresource

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/types"
)

const attachmentCleanupJobType = "field_resource_cleanup"

type AttachmentCleanupPlan struct {
	Objects  []string `json:"objects"`
	Prefixes []string `json:"prefixes"`
}

// StageAttachmentPurge removes attachment metadata and persists the exact
// filesystem cleanup set in the caller's database transaction. No blob is
// touched before that transaction commits.
func StageAttachmentPurge(
	ctx context.Context,
	app core.App,
	tableID string,
	fieldID string,
	operationID string,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	table, err := app.FindFirstRecordByFilter(
		"vibetable_tables", "table_id={:table}",
		dbx.Params{"table": tableID},
	)
	if err != nil {
		return fmt.Errorf("load attachment table metadata: %w", err)
	}
	collection, err := app.FindCollectionByNameOrId(
		table.GetString("collection_id"),
	)
	if err != nil {
		return fmt.Errorf("load attachment collection: %w", err)
	}
	metadata, err := app.FindRecordsByFilter(
		"vibetable_attachment_meta",
		"table_id={:table} && field_id={:field}",
		"id", 0, 0,
		dbx.Params{"table": tableID, "field": fieldID},
	)
	if err != nil {
		return fmt.Errorf("list attachment metadata for purge: %w", err)
	}
	versions, err := app.FindRecordsByFilter(
		"vibetable_attachment_versions",
		"table_id={:table} && field_id={:field}",
		"id", 0, 0,
		dbx.Params{"table": tableID, "field": fieldID},
	)
	if err != nil {
		return fmt.Errorf("list attachment versions for purge: %w", err)
	}
	cleanup := AttachmentCleanupPlan{
		Objects: []string{}, Prefixes: []string{},
	}
	for _, item := range metadata {
		base := collection.BaseFilesPath() + "/" + item.GetString("record_id")
		if storedName := item.GetString("stored_name"); storedName != "" {
			cleanup.Objects = append(cleanup.Objects, base+"/"+storedName)
			cleanup.Prefixes = append(
				cleanup.Prefixes, base+"/thumbs_"+storedName+"/",
			)
		}
	}
	for _, version := range versions {
		if blob := version.GetString("blob"); blob != "" {
			cleanup.Objects = append(
				cleanup.Objects, version.BaseFilesPath()+"/"+blob,
			)
		}
	}
	raw, err := json.Marshal(cleanup)
	if err != nil {
		return fmt.Errorf("encode attachment cleanup plan: %w", err)
	}
	jobs, err := app.FindCollectionByNameOrId("vibetable_jobs")
	if err != nil {
		return fmt.Errorf("load attachment cleanup jobs: %w", err)
	}
	job := core.NewRecord(jobs)
	job.Set("job_type", attachmentCleanupJobType)
	job.Set("state", "queued")
	job.Set("phase", "queued")
	job.Set("cleanup_state", "pending")
	job.Set("cursor_json", types.JSONRaw(raw))
	job.Set("schema_revision", 1)
	job.Set("source_event_id", operationID)
	job.Set("source_table_id", tableID)
	job.Set("relation_field_id", fieldID)
	if err := app.Save(job); err != nil {
		return fmt.Errorf("persist attachment cleanup job: %w", err)
	}
	for _, item := range metadata {
		if err := app.Delete(item); err != nil {
			return fmt.Errorf("delete attachment metadata: %w", err)
		}
	}
	for _, version := range versions {
		if err := app.Delete(version); err != nil {
			return fmt.Errorf("delete attachment version metadata: %w", err)
		}
	}
	return nil
}

// RunPendingAttachmentCleanup resumes committed cleanup jobs. A crash between
// the schema commit and blob deletion therefore leaves durable work.
func RunPendingAttachmentCleanup(ctx context.Context, app core.App) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	records, err := app.FindRecordsByFilter(
		"vibetable_jobs",
		"job_type={:type} && state!='complete'",
		"id", 0, 0,
		dbx.Params{"type": attachmentCleanupJobType},
	)
	if err != nil {
		return fmt.Errorf("load pending attachment cleanup jobs: %w", err)
	}
	var failures []error
	for _, record := range records {
		if err := runAttachmentCleanup(ctx, app, record); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

func runAttachmentCleanup(
	ctx context.Context,
	app core.App,
	record *core.Record,
) error {
	raw, err := json.Marshal(record.GetRaw("cursor_json"))
	if err != nil {
		return fmt.Errorf("encode attachment cleanup cursor: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var cleanup AttachmentCleanupPlan
	if err := decoder.Decode(&cleanup); err != nil {
		return markCleanupPending(
			app, record, fmt.Errorf("decode attachment cleanup cursor: %w", err),
		)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return markCleanupPending(
			app, record, errors.New("attachment cleanup cursor has trailing data"),
		)
	}
	record.Set("state", "running")
	record.Set("phase", "cleaning")
	if err := app.Save(record); err != nil {
		return err
	}
	fsys, err := app.NewFilesystem()
	if err != nil {
		return markCleanupPending(app, record, err)
	}
	defer fsys.Close()
	fsys.SetContext(ctx)
	for _, key := range cleanup.Objects {
		if err := deleteObjectIfPresent(fsys, key); err != nil {
			return markCleanupPending(app, record, err)
		}
	}
	for _, prefix := range cleanup.Prefixes {
		if failures := fsys.DeletePrefix(prefix); len(failures) != 0 {
			return markCleanupPending(
				app, record,
				fmt.Errorf(
					"delete attachment thumbnails: %w",
					errors.Join(failures...),
				),
			)
		}
	}
	record.Set("state", "complete")
	record.Set("phase", "completed")
	record.Set("cleanup_state", "complete")
	record.Set("error_json", nil)
	return app.Save(record)
}

func markCleanupPending(
	app core.App,
	record *core.Record,
	cause error,
) error {
	raw, _ := json.Marshal(map[string]any{
		"code":    "field.resource.cleanup_failed",
		"message": cause.Error(),
	})
	record.Set("state", "queued")
	record.Set("phase", "cleaning")
	record.Set("cleanup_state", "pending")
	record.Set("error_json", types.JSONRaw(raw))
	if err := app.Save(record); err != nil {
		return errors.Join(cause, err)
	}
	return cause
}

type objectFilesystem interface {
	Exists(string) (bool, error)
	Delete(string) error
}

func deleteObjectIfPresent(
	fsys objectFilesystem,
	key string,
) error {
	exists, err := fsys.Exists(key)
	if err != nil {
		return fmt.Errorf("inspect attachment object %q: %w", key, err)
	}
	if !exists {
		return nil
	}
	if err := fsys.Delete(key); err != nil {
		return fmt.Errorf("delete attachment object %q: %w", key, err)
	}
	return nil
}
