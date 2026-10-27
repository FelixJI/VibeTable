package app

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"path"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/pocketbase/pocketbase/core"
	"github.com/vibetable/vibetable/sidecar/internal/attachments"
	"github.com/vibetable/vibetable/sidecar/internal/pluginstore"
	"github.com/vibetable/vibetable/sidecar/internal/schemaexecution"
)

const surfaceOutputMaxBytes = 32 << 20
const surfaceOutputBlockBytes = 256 << 10

type surfaceOutputSource struct {
	TemplateVersion string `json:"templateVersion"`
	DataVersion     string `json:"dataVersion"`
	Purpose         string `json:"purpose"`
}

func (source surfaceOutputSource) valid() bool {
	for _, value := range []string{source.TemplateVersion, source.DataVersion, source.Purpose} {
		if strings.TrimSpace(value) == "" || utf8.RuneCountInString(value) > 256 || strings.IndexFunc(value, unicode.IsControl) >= 0 {
			return false
		}
	}
	return true
}

type surfaceOutputStage struct {
	owner, artifact, table, record, field, name, mediaType string
	size                                                   int
	source                                                 surfaceOutputSource
	before                                                 map[string]bool
	data                                                   []byte
	complete                                               bool
	expires                                                time.Time
}
type surfaceOutputStages struct {
	mu    sync.Mutex
	items map[string]*surfaceOutputStage
}

func (ports *pluginSurfaceIO) pruneOutputsLocked() {
	for handle, output := range ports.outputs.items {
		if time.Now().After(output.expires) {
			ports.attachments.Drop(handle)
			delete(ports.outputs.items, handle)
		}
	}
}

func (ports *pluginSurfaceIO) dropOutputs(input pluginSurfaceAPIRequest, handles []string) {
	if ports == nil {
		return
	}
	ports.outputs.mu.Lock()
	defer ports.outputs.mu.Unlock()
	for _, handle := range handles {
		if stage := ports.outputs.items[handle]; stage != nil && stage.owner == surfaceIOOwner(input) {
			delete(ports.outputs.items, handle)
		}
	}
}

// Host-only staging keeps the ordinary 1 MiB request limit and never accepts a local path.
func executeSurfaceOutput(ctx context.Context, app core.App, workspace string, input pluginSurfaceAPIRequest, ports *pluginSurfaceIO) (any, error) {
	if ports == nil || ports.attachments == nil || input.SurfaceToken == "" {
		return nil, pluginDataError("plugin_api_unsupported")
	}
	if input.Method == "output.open" || input.Method == "output.read" {
		return readSurfaceOutput(ctx, app, workspace, input, ports)
	}
	var request struct {
		Handle     string              `json:"handle"`
		ArtifactID string              `json:"artifactId,omitempty"`
		Collection string              `json:"collection,omitempty"`
		RecordID   string              `json:"recordId,omitempty"`
		FieldID    string              `json:"fieldId,omitempty"`
		Name       string              `json:"name,omitempty"`
		MediaType  string              `json:"mediaType,omitempty"`
		SizeBytes  int                 `json:"sizeBytes,omitempty"`
		Source     surfaceOutputSource `json:"source,omitempty"`
		Offset     int                 `json:"offset,omitempty"`
		Base64     string              `json:"base64,omitempty"`
	}
	if decodePluginParams(input.Request, &request) != nil || request.Handle == "" || len(request.Handle) > 128 {
		return nil, pluginDataError("plugin_capability_invalid")
	}
	var result any
	err := app.RunInTransaction(func(tx core.App) error {
		installed, err := surfaceInstallation(ctx, tx, workspace, input)
		if err != nil {
			return err
		}
		if installed.ActionRisk == "read" {
			return pluginDataError("plugin_write_denied")
		}
		ports.outputs.mu.Lock()
		defer ports.outputs.mu.Unlock()
		ports.pruneOutputsLocked()
		if input.Method == "output.stage.begin" {
			if request.SizeBytes <= 0 || request.SizeBytes > surfaceOutputMaxBytes {
				return pluginDataError("plugin_file_limit")
			}
			if request.ArtifactID == "" || len(request.ArtifactID) > 128 || !request.Source.valid() || request.MediaType == "" || len(request.MediaType) > 128 ||
				request.Name == "" || len(request.Name) > 255 || path.Base(request.Name) != request.Name || strings.ContainsAny(request.Name, "\\\x00") {
				return pluginDataError("plugin_capability_invalid")
			}
			if len(ports.outputs.items) >= 2 {
				return pluginDataError("plugin_file_limit")
			}
			for _, existing := range ports.outputs.items {
				if existing.artifact == request.ArtifactID {
					return pluginDataError("plugin_output_already_archived")
				}
			}
			if _, exists := ports.outputs.items[request.Handle]; exists {
				return pluginDataError("plugin_capability_invalid")
			}
			stored, err := pluginstore.New(tx, workspace).GetAudit(ctx, input.ProjectKey, input.PluginID, request.ArtifactID)
			if err != nil {
				return err
			}
			if stored != nil {
				return pluginDataError("plugin_output_already_archived")
			}
			field, err := authorizeOutputTarget(ctx, tx, input, installed, request.Collection, request.RecordID, request.FieldID, true)
			if err != nil {
				return err
			}
			refs, err := ports.attachments.RefsByID(ctx, tx, request.Collection, request.RecordID, field)
			if err != nil {
				return err
			}
			before := map[string]bool{}
			for _, ref := range refs {
				before[ref.StoredName] = true
			}
			if ports.outputs.items == nil {
				ports.outputs.items = map[string]*surfaceOutputStage{}
			}
			ports.outputs.items[request.Handle] = &surfaceOutputStage{owner: surfaceIOOwner(input), artifact: request.ArtifactID,
				table: request.Collection, record: request.RecordID, field: field, name: request.Name, mediaType: request.MediaType,
				size: request.SizeBytes, source: request.Source, before: before, data: make([]byte, 0, request.SizeBytes), expires: time.Now().Add(5 * time.Minute)}
			result = map[string]any{"staged": true}
			return nil
		}
		stage := ports.outputs.items[request.Handle]
		if stage == nil || stage.owner != surfaceIOOwner(input) {
			return pluginDataError("plugin_write_denied")
		}
		if _, err := authorizeOutputTarget(ctx, tx, input, installed, stage.table, stage.record, stage.field, true); err != nil {
			return err
		}
		switch input.Method {
		case "output.stage.write":
			if stage.complete || request.Offset != len(stage.data) || len(request.Base64) > (surfaceOutputBlockBytes+2)/3*4 {
				return pluginDataError("plugin_capability_invalid")
			}
			data, err := base64.StdEncoding.DecodeString(request.Base64)
			if err != nil || len(data) == 0 || len(data) > surfaceOutputBlockBytes || len(stage.data)+len(data) > stage.size {
				return pluginDataError("plugin_file_limit")
			}
			stage.data = append(stage.data, data...)
			result = map[string]any{"offset": len(stage.data)}
		case "output.stage.finish":
			if stage.complete || len(stage.data) != stage.size {
				return pluginDataError("plugin_capability_invalid")
			}
			ports.stages.mu.Lock()
			defer ports.stages.mu.Unlock()
			if _, exists := ports.stages.items[request.Handle]; exists {
				return pluginDataError("plugin_capability_invalid")
			}
			if err := ports.attachments.StageOwned(request.Handle, stage.name, stage.data); err != nil {
				return err
			}
			if ports.stages.items == nil {
				ports.stages.items = map[string]surfaceStagedAttachment{}
			}
			ports.stages.items[request.Handle] = surfaceStagedAttachment{owner: stage.owner, table: stage.table, record: stage.record, field: stage.field,
				fileGrant: stage.artifact, run: input.SurfaceToken, expires: stage.expires}
			stage.data = nil // The canonical attachment manager owns the backing slice from here.
			stage.complete = true
			result = map[string]any{"handle": request.Handle, "artifactId": stage.artifact}
		default:
			return pluginDataError("plugin_api_unsupported")
		}
		return ctx.Err()
	})
	return result, err
}

func authorizeOutputTarget(ctx context.Context, app core.App, input pluginSurfaceAPIRequest, installed pluginSurfaceInstallation,
	tableID, recordID, fieldID string, write bool) (string, error) {
	operation := "read"
	if write {
		operation = "update"
	}
	grant := surfaceIOGrant(installed, input, tableID, operation)
	if grant == nil || recordID == "" || !write && installed.grant(tableID, input.ActiveCollection) == nil {
		return "", pluginDataError("plugin_write_denied")
	}
	table, err := schemaexecution.Describe(ctx, app, tableID)
	if err != nil {
		return "", err
	}
	field, ok := table.Field(fieldID)
	allowed := pluginAllowed(*grant, table.Snapshot)
	if !ok || field.File == nil || allowed[field.Identity.FieldID] == "" || allowed["id"] == "" {
		return "", pluginDataError("plugin_write_denied")
	}
	return field.Identity.FieldID, nil
}

// Called by the existing mutation ApplyWithCommit callback: attachment and provenance roll back together.
func surfaceOutputArchiveFinish(ctx context.Context, tx core.App, workspace string, input pluginSurfaceAPIRequest, ioPorts ...*pluginSurfaceIO) error {
	if len(ioPorts) == 0 || ioPorts[0] == nil {
		return nil
	}
	ports := ioPorts[0]
	request, err := surfaceMutationRequest(ctx, tx, workspace, input, ports)
	if err != nil {
		return err
	}
	ports.outputs.mu.Lock()
	defer ports.outputs.mu.Unlock()
	for _, operation := range request.Operations {
		for _, handle := range operation.UploadHandles {
			stage := ports.outputs.items[handle]
			if stage == nil {
				continue
			}
			if !stage.complete || stage.owner != surfaceIOOwner(input) || time.Now().After(stage.expires) {
				return pluginDataError("plugin_write_denied")
			}
			refs, err := ports.attachments.RefsByID(ctx, tx, stage.table, stage.record, stage.field)
			if err != nil {
				return err
			}
			var issued *attachments.Ref
			for i := range refs {
				if !stage.before[refs[i].StoredName] {
					if issued != nil {
						return pluginDataError("plugin_capability_invalid")
					}
					issued = &refs[i]
				}
			}
			if issued == nil || issued.Size != int64(stage.size) || issued.OriginalName != stage.name {
				return pluginDataError("plugin_capability_invalid")
			}
			prior, err := pluginstore.New(tx, workspace).GetAudit(ctx, input.ProjectKey, input.PluginID, stage.artifact)
			if err != nil {
				return err
			}
			if prior != nil {
				return pluginDataError("plugin_output_already_archived")
			}
			installed, err := surfaceInstallation(ctx, tx, workspace, input)
			if err != nil {
				return err
			}
			now := time.Now().UTC().Truncate(time.Second).Format(time.RFC3339)
			event, _ := json.Marshal(map[string]any{"eventId": stage.artifact, "projectKey": input.ProjectKey, "pluginId": input.PluginID,
				"pluginVersion": installed.Version, "packageHash": input.PackageHash, "eventType": "output.archived", "outcome": "succeeded",
				"actionId": input.ActionID, "runId": input.RequestID, "actor": "local-user", "risk": "write", "targetCollection": stage.table,
				"targetCount": 1, "startedAt": now, "finishedAt": now, "durationMs": 0, "errorCode": nil,
				"details": map[string]any{"installationId": input.InstallationID, "collection": stage.table, "recordId": stage.record,
					"fieldId": stage.field, "storedName": issued.StoredName, "displayName": stage.name, "mediaType": issued.MIMEType,
					"sizeBytes": stage.size, "source": stage.source, "sourceDeclared": true}})
			if _, err := pluginstore.New(tx, workspace).RecordAuditInTransaction(ctx, event); err != nil {
				return err
			}
		}
	}
	return nil
}

func readSurfaceOutput(ctx context.Context, app core.App, workspace string, input pluginSurfaceAPIRequest, ports *pluginSurfaceIO) (any, error) {
	var request struct {
		ArtifactID string `json:"artifactId"`
		Offset     int64  `json:"offset,omitempty"`
	}
	if decodePluginParams(input.Request, &request) != nil || request.ArtifactID == "" || len(request.ArtifactID) > 128 || request.Offset < 0 {
		return nil, pluginDataError("plugin_capability_invalid")
	}
	var result any
	err := app.RunInTransaction(func(tx core.App) error {
		installed, err := surfaceInstallation(ctx, tx, workspace, input)
		if err != nil {
			return err
		}
		raw, err := pluginstore.New(tx, workspace).GetAudit(ctx, input.ProjectKey, input.PluginID, request.ArtifactID)
		if err != nil {
			return err
		}
		var event struct {
			EventType string `json:"eventType"`
			Details   struct {
				InstallationID string              `json:"installationId"`
				Collection     string              `json:"collection"`
				RecordID       string              `json:"recordId"`
				FieldID        string              `json:"fieldId"`
				StoredName     string              `json:"storedName"`
				Source         surfaceOutputSource `json:"source"`
			} `json:"details"`
		}
		if json.Unmarshal(raw, &event) != nil || event.EventType != "output.archived" || event.Details.InstallationID != input.InstallationID {
			return pluginDataError("plugin_file_denied")
		}
		details := event.Details
		if _, err = authorizeOutputTarget(ctx, tx, input, installed, details.Collection, details.RecordID, details.FieldID, false); err != nil {
			return err
		}
		refs, err := ports.attachments.RefsByID(ctx, tx, details.Collection, details.RecordID, details.FieldID)
		if err != nil {
			return err
		}
		for _, ref := range refs {
			if ref.StoredName != details.StoredName {
				continue
			}
			if ref.Size <= 0 || ref.Size > surfaceOutputMaxBytes || request.Offset > ref.Size {
				return pluginDataError("plugin_file_limit")
			}
			if input.Method == "output.open" {
				result = map[string]any{"artifactId": request.ArtifactID, "displayName": ref.OriginalName, "mediaType": ref.MIMEType,
					"sizeBytes": ref.Size, "source": details.Source, "sourceDeclared": true}
				return nil
			}
			download, err := ports.attachments.Open(ctx, tx, ref.DownloadCapability)
			if err != nil {
				return err
			}
			defer download.Reader.Close()
			if _, err = download.Reader.Seek(request.Offset, io.SeekStart); err != nil {
				return err
			}
			data, err := io.ReadAll(io.LimitReader(download.Reader, surfaceOutputBlockBytes))
			if err != nil {
				return err
			}
			result = map[string]any{"base64": base64.StdEncoding.EncodeToString(data), "eof": request.Offset+int64(len(data)) >= ref.Size}
			return nil
		}
		return pluginDataError("plugin_file_denied")
	})
	return result, err
}
