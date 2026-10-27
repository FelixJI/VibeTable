package app

import (
	"context"
	"encoding/base64"
	"io"
	"sync"
	"time"

	"github.com/pocketbase/pocketbase/core"
	"github.com/vibetable/vibetable/sidecar/internal/attachments"
	"github.com/vibetable/vibetable/sidecar/internal/mutation"
	"github.com/vibetable/vibetable/sidecar/internal/schemaexecution"
)

type surfaceStagedAttachment struct {
	owner, table, record, field, fileGrant, run string
	expires                                     time.Time
}
type surfaceAttachmentStages struct {
	mu    sync.Mutex
	items map[string]surfaceStagedAttachment
}

func (ports *pluginSurfaceIO) dropAttachments(input pluginSurfaceAPIRequest) (any, error) {
	var request struct {
		Handles []string `json:"handles"`
	}
	if ports == nil || ports.attachments == nil || decodePluginParams(input.Request, &request) != nil || len(request.Handles) > 8 {
		return nil, pluginDataError("plugin_capability_invalid")
	}
	ports.dropOutputs(input, request.Handles)
	ports.stages.mu.Lock()
	defer ports.stages.mu.Unlock()
	for _, handle := range request.Handles {
		if stage, ok := ports.stages.items[handle]; ok && stage.owner == surfaceIOOwner(input) {
			ports.attachments.Drop(handle)
			delete(ports.stages.items, handle)
		}
	}
	return map[string]any{"dropped": true}, nil
}
func (ports *pluginSurfaceIO) authorizeAttachment(input pluginSurfaceAPIRequest, table, record, field string, handles []string) error {
	if ports == nil || ports.attachments == nil {
		return pluginDataError("plugin_write_denied")
	}
	ports.stages.mu.Lock()
	defer ports.stages.mu.Unlock()
	for _, handle := range handles {
		stage, ok := ports.stages.items[handle]
		if !ok || stage.owner != surfaceIOOwner(input) || stage.table != table || stage.record != record || stage.field != field || time.Now().After(stage.expires) {
			return pluginDataError("plugin_write_denied")
		}
	}
	return nil
}
func executeSurfaceAttachment(ctx context.Context, app core.App, workspace string, input pluginSurfaceAPIRequest, ports *pluginSurfaceIO) (any, error) {
	if ports == nil || ports.attachments == nil {
		return nil, pluginDataError("plugin_api_unsupported")
	}
	var request struct {
		Collection  string `json:"collection"`
		RecordID    string `json:"recordId"`
		FieldID     string `json:"fieldId"`
		StoredName  string `json:"storedName,omitempty"`
		Name        string `json:"name,omitempty"`
		Base64      string `json:"base64,omitempty"`
		Handle      string `json:"handle,omitempty"`
		FileGrantID string `json:"fileGrantId,omitempty"`
		RunID       string `json:"runId,omitempty"`
		Offset      int64  `json:"offset,omitempty"`
	}
	if decodePluginParams(input.Request, &request) != nil || request.Collection == "" || request.RecordID == "" || request.FieldID == "" || request.Offset < 0 {
		return nil, pluginDataError("plugin_capability_invalid")
	}
	var result any
	err := app.RunInTransaction(func(tx core.App) error {
		installed, err := surfaceInstallation(ctx, tx, workspace, input)
		if err != nil {
			return err
		}
		operation := "read"
		if input.Method == "attachment.stage" {
			operation = "update"
		}
		grant := surfaceIOGrant(installed, input, request.Collection, operation)
		if grant == nil || operation == "read" && installed.grant(request.Collection, input.ActiveCollection) == nil {
			return pluginDataError("plugin_read_denied")
		}
		table, err := schemaexecution.Describe(ctx, tx, request.Collection)
		if err != nil {
			return pluginDataError("plugin_read_denied")
		}
		field, ok := table.Field(request.FieldID)
		allowed := pluginAllowed(*grant, table.Snapshot)
		if !ok || field.File == nil || allowed[field.Identity.FieldID] == "" || allowed["id"] == "" {
			return pluginDataError("plugin_read_denied")
		}
		if input.Method == "attachment.stage" {
			if installed.ActionRisk == "read" || request.Handle == "" || request.FileGrantID == "" || request.RunID == "" {
				return pluginDataError("plugin_write_denied")
			}
			data, err := base64.StdEncoding.DecodeString(request.Base64)
			if err != nil || len(data) > 1<<20 || len(data) == 0 {
				return pluginDataError("plugin_file_limit")
			}
			ports.stages.mu.Lock()
			defer ports.stages.mu.Unlock()
			if ports.stages.items == nil {
				ports.stages.items = map[string]surfaceStagedAttachment{}
			}
			for handle, stage := range ports.stages.items {
				if time.Now().After(stage.expires) {
					ports.attachments.Drop(handle)
					delete(ports.stages.items, handle)
				}
			}
			if len(ports.stages.items) >= 128 {
				return pluginDataError("plugin_file_limit")
			}
			if _, exists := ports.stages.items[request.Handle]; exists {
				return pluginDataError("plugin_capability_invalid")
			}
			if err := ports.attachments.StageOwned(request.Handle, request.Name, data); err != nil {
				return err
			}
			ports.stages.items[request.Handle] = surfaceStagedAttachment{surfaceIOOwner(input), request.Collection, request.RecordID, field.Identity.FieldID, request.FileGrantID, request.RunID, time.Now().Add(5 * time.Minute)}
			result = map[string]any{"handle": request.Handle}
			return nil
		}
		refs, err := ports.attachments.RefsByID(ctx, tx, request.Collection, request.RecordID, field.Identity.FieldID)
		if err != nil {
			return err
		}
		if input.Method == "attachment.list" {
			items := []any{}
			for _, ref := range refs {
				items = append(items, map[string]any{"storedName": ref.StoredName, "displayName": ref.OriginalName, "mediaType": ref.MIMEType, "sizeBytes": ref.Size})
			}
			result = map[string]any{"items": items}
			return nil
		}
		if input.Method != "attachment.read" {
			return pluginDataError("plugin_api_unsupported")
		}
		var selected *attachments.Ref
		for i := range refs {
			if refs[i].StoredName == request.StoredName {
				selected = &refs[i]
				break
			}
		}
		if selected == nil {
			return pluginDataError("plugin_read_denied")
		}
		if selected.Size > 1<<20 {
			return pluginDataError("plugin_file_limit")
		}
		download, err := ports.attachments.Open(ctx, tx, selected.DownloadCapability)
		if err != nil {
			return err
		}
		defer download.Reader.Close()
		if _, err = download.Reader.Seek(request.Offset, io.SeekStart); err != nil {
			return err
		}
		data, err := io.ReadAll(io.LimitReader(download.Reader, 256*1024))
		if err != nil {
			return err
		}
		result = map[string]any{"base64": base64.StdEncoding.EncodeToString(data), "eof": request.Offset+int64(len(data)) >= download.Size}
		return nil
	})
	return result, err
}

// Keep the canonical attachment kernel's row guard and field authorization in the business transaction.
func surfaceAttachmentOperation(input pluginSurfaceAPIRequest, ports *pluginSurfaceIO, table string, operation mutation.Operation) error {
	if operation.RecordID == nil {
		return pluginDataError("plugin_guard_required")
	}
	return ports.authorizeAttachment(input, table, *operation.RecordID, operation.FieldID, operation.UploadHandles)
}
