package app

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"regexp"

	"github.com/vibetable/vibetable/sidecar/internal/contracts/productcapabilities"
	"github.com/vibetable/vibetable/sidecar/internal/pluginstore"
	"github.com/vibetable/vibetable/sidecar/internal/productrpc"
)

// pluginPreservationRecordIDPattern matches the stable 15-character
// PocketBase record ids used as the preservation keyset cursor.
var pluginPreservationRecordIDPattern = regexp.MustCompile(`^[a-zA-Z0-9]{15}$`)

type pluginPreservationParams struct {
	ProjectKey           string `json:"projectKey"`
	PluginID             string `json:"pluginId"`
	InstallationID       string `json:"installationId"`
	InstallationRevision int64  `json:"installationRevision"`
	AfterRecordID        string `json:"afterRecordId"`
	RecordOffset         int64  `json:"recordOffset"`
}

// decodePluginPreservationParams enforces the closed request set: the four
// identity fields are required, afterRecordId is empty or a 15-character
// alphanumeric id, recordOffset is a non-negative 256 KiB multiple within
// the 1 GiB cursor budget, and explicit nulls, unknown fields, wrong types
// and trailing JSON are all rejected.
func decodePluginPreservationParams(raw json.RawMessage) (pluginPreservationParams, error) {
	var params pluginPreservationParams
	invalid := func() (pluginPreservationParams, error) {
		return params, &productrpc.PublicError{
			Code:    "pluginPreservation.params.invalid",
			Message: "Preservation requires the closed identity and keyset/fragment cursor request.",
		}
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		return invalid()
	}
	for key, value := range fields {
		switch key {
		case "projectKey", "pluginId", "installationId", "installationRevision",
			"afterRecordId", "recordOffset":
		default:
			return invalid()
		}
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return invalid()
		}
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&params) != nil {
		return invalid()
	}
	if decoder.Decode(new(any)) != io.EOF {
		return invalid()
	}
	if !pluginProjectKeyPattern.MatchString(params.ProjectKey) ||
		!pluginIDPattern.MatchString(params.PluginID) ||
		params.InstallationID == "" || params.InstallationRevision < 1 {
		return invalid()
	}
	if params.AfterRecordID != "" &&
		!pluginPreservationRecordIDPattern.MatchString(params.AfterRecordID) {
		return invalid()
	}
	if params.RecordOffset < 0 ||
		params.RecordOffset%pluginstore.PreservationChunkBytes != 0 ||
		params.RecordOffset > 1<<30 {
		return invalid()
	}
	return params, nil
}

// pluginPreservationSnapshotRegistration exposes the #486 Host-only
// read-only preservation snapshot over the Product RPC seam. The generated
// catalog configures it as workspace/hostOnly (capability
// plugin.preservation, owner goSidecar, effect read); no renderer path
// exists and the service never writes.
func pluginPreservationSnapshotRegistration(service *pluginstore.Service) productrpc.Registration {
	return productrpc.Registration{
		Method: "pluginPreservation.snapshot",
		Scope:  productcapabilities.WorkspaceScope,
		ValidateParams: func(raw json.RawMessage) error {
			params, err := decodePluginPreservationParams(raw)
			if err != nil {
				return err
			}
			return service.ValidateProject(params.ProjectKey)
		},
		Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
			params, err := decodePluginPreservationParams(raw)
			if err != nil {
				return nil, err
			}
			page, pageErr := service.PreservationSnapshot(ctx, pluginstore.PreservationSnapshotRequest{
				ProjectKey:           params.ProjectKey,
				PluginID:             params.PluginID,
				InstallationID:       params.InstallationID,
				InstallationRevision: params.InstallationRevision,
				AfterRecordID:        params.AfterRecordID,
				RecordOffset:         params.RecordOffset,
			})
			if pageErr != nil {
				return nil, pluginPublicError(pageErr)
			}
			return page, nil
		},
	}
}
