package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"regexp"

	"github.com/vibetable/vibetable/sidecar/internal/contracts/productcapabilities"
	"github.com/vibetable/vibetable/sidecar/internal/pluginstore"
	"github.com/vibetable/vibetable/sidecar/internal/productrpc"
)

var pluginProjectKeyPattern = regexp.MustCompile(`^local:[0-9a-f]{32}$`)
var pluginIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{2,254}$`)

type pluginProjectParams struct {
	ProjectKey string `json:"projectKey"`
}

type pluginIdentityParams struct {
	ProjectKey string `json:"projectKey"`
	PluginID   string `json:"pluginId"`
}

type pluginSetEnabledParams struct {
	ProjectKey string `json:"projectKey"`
	PluginID   string `json:"pluginId"`
	Enabled    *bool  `json:"enabled"`
}

func decodePluginParams(raw json.RawMessage, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("trailing JSON")
	}
	return nil
}

func validatePluginProject(raw json.RawMessage) (pluginProjectParams, error) {
	var params pluginProjectParams
	if err := decodePluginParams(raw, &params); err != nil {
		return params, err
	}
	if !pluginProjectKeyPattern.MatchString(params.ProjectKey) {
		return params, errors.New("projectKey is invalid")
	}
	return params, nil
}

func validatePluginIdentity(raw json.RawMessage) (pluginIdentityParams, error) {
	var params pluginIdentityParams
	if err := decodePluginParams(raw, &params); err != nil {
		return params, err
	}
	if !pluginProjectKeyPattern.MatchString(params.ProjectKey) ||
		!pluginIDPattern.MatchString(params.PluginID) {
		return params, errors.New("project or plugin identity is invalid")
	}
	return params, nil
}

// pluginPublicError maps the store's frozen public contract onto the
// Product error envelope. Internal codes never become public and unmapped
// failures stay private.
func pluginPublicError(err error) error {
	view, ok := pluginstore.PublicError(err)
	if !ok {
		return err
	}
	if view.Code == "" {
		// The store CAS conflict keeps only its frozen message; surface it
		// under the stable plugin conflict code.
		return &productrpc.PublicError{
			Code:    "plugin.revision_conflict",
			Message: view.Message,
		}
	}
	return &productrpc.PublicError{
		Code:    view.Code,
		Message: view.Message,
	}
}

// PluginSharedStateRegistrations wires the Go-owned shared plugin catalog
// Product RPCs. The dispatcher fail-closes unless these registrations match
// the generated goSidecar policy exactly.
func PluginSharedStateRegistrations(service *pluginstore.Service, gates ...businessWriteGate) []productrpc.Registration {
	listCatalog := productrpc.Registration{
		Method: "plugin.listCatalog",
		Scope:  productcapabilities.WorkspaceScope,
		ValidateParams: func(raw json.RawMessage) error {
			params, err := validatePluginProject(raw)
			if err != nil {
				return err
			}
			return service.ValidateProject(params.ProjectKey)
		},
		Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
			params, err := validatePluginProject(raw)
			if err != nil {
				return nil, err
			}
			items, listErr := service.ListInstallations(ctx, params.ProjectKey)
			if listErr != nil {
				return nil, listErr
			}
			return pluginPayloadList(items), nil
		},
	}
	listAudit := productrpc.Registration{
		Method: "plugin.listAudit",
		Scope:  productcapabilities.WorkspaceScope,
		ValidateParams: func(raw json.RawMessage) error {
			params, err := validatePluginIdentity(raw)
			if err != nil {
				return err
			}
			return service.ValidateProject(params.ProjectKey)
		},
		Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
			params, err := validatePluginIdentity(raw)
			if err != nil {
				return nil, err
			}
			items, listErr := service.ListAudit(ctx, params.ProjectKey, params.PluginID)
			if listErr != nil {
				return nil, listErr
			}
			return pluginPayloadList(items), nil
		},
	}
	listPendingCleanup := productrpc.Registration{
		Method: "plugin.listPendingCleanup",
		Scope:  productcapabilities.WorkspaceScope,
		ValidateParams: func(raw json.RawMessage) error {
			params, err := validatePluginProject(raw)
			if err != nil {
				return err
			}
			return service.ValidateProject(params.ProjectKey)
		},
		Handler: func(context.Context, json.RawMessage) (any, error) {
			// The pending-cleanup projection is owned by the Go catalog and
			// is empty until a cleanup producer exists; the previous owner
			// returned the same empty list.
			return []json.RawMessage{}, nil
		},
	}
	setEnabled := productrpc.Registration{
		Method: "plugin.setEnabled",
		Scope:  productcapabilities.WorkspaceScope,
		ValidateParams: func(raw json.RawMessage) error {
			var params pluginSetEnabledParams
			if err := decodePluginParams(raw, &params); err != nil {
				return err
			}
			if !pluginProjectKeyPattern.MatchString(params.ProjectKey) ||
				!pluginIDPattern.MatchString(params.PluginID) || params.Enabled == nil {
				return errors.New("project or plugin identity is invalid")
			}
			return service.ValidateProject(params.ProjectKey)
		},
		Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
			var params pluginSetEnabledParams
			if err := decodePluginParams(raw, &params); err != nil {
				return nil, err
			}
			if params.Enabled == nil {
				return nil, errors.New("enabled is required")
			}
			operationID, ok := productrpc.OperationID(ctx)
			if !ok {
				operationID = "plugin:" + params.PluginID
			}
			var snapshot json.RawMessage
			enableErr := runBusinessWrite(ctx, gates, "plugin.setEnabled", operationID, func(writeCtx context.Context) error {
				var err error
				snapshot, err = service.SetEnabled(writeCtx, params.ProjectKey, params.PluginID, *params.Enabled)
				return err
			})
			if enableErr != nil {
				return nil, pluginPublicError(enableErr)
			}
			return json.RawMessage(snapshot), nil
		},
	}
	return []productrpc.Registration{
		listCatalog, listAudit, listPendingCleanup, setEnabled,
	}
}

func pluginPayloadList(items []json.RawMessage) []json.RawMessage {
	result := make([]json.RawMessage, 0, len(items))
	result = append(result, items...)
	return result
}
