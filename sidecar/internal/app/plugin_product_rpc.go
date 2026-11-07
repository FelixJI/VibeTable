package app

import (
	"bytes"
	"context"
	"encoding/base64"
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

// pluginAuditPageParams carries the plugin center audit summary request. The
// optional cursor is the opaque continuation token returned by the previous
// page of the same project/plugin scope.
type pluginAuditPageParams struct {
	ProjectKey string `json:"projectKey"`
	PluginID   string `json:"pluginId"`
	Cursor     string `json:"cursor"`
}

// pluginAuditPageCursor binds one continuation token to its issuing scope
// and the stable seq keyset position after the last delivered event.
type pluginAuditPageCursor struct {
	ProjectKey string `json:"projectKey"`
	PluginID   string `json:"pluginId"`
	AfterSeq   int64  `json:"afterSeq"`
}

type pluginAuditPageResult struct {
	Events     []pluginstore.AuditSummaryItem `json:"events"`
	NextCursor *string                        `json:"nextCursor"`
}

const pluginAuditCursorMaxTextBytes = 512

func encodePluginAuditPageCursor(cursor pluginAuditPageCursor) string {
	raw, err := json.Marshal(cursor)
	if err != nil {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(raw)
}

func decodePluginAuditPageCursor(text string) (pluginAuditPageCursor, error) {
	if text == "" || len(text) > pluginAuditCursorMaxTextBytes {
		return pluginAuditPageCursor{}, errors.New("audit cursor is invalid")
	}
	raw, err := base64.RawURLEncoding.DecodeString(text)
	if err != nil || len(raw) == 0 {
		return pluginAuditPageCursor{}, errors.New("audit cursor is invalid")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var cursor pluginAuditPageCursor
	if err := decoder.Decode(&cursor); err != nil {
		return pluginAuditPageCursor{}, errors.New("audit cursor is invalid")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return pluginAuditPageCursor{}, errors.New("audit cursor is invalid")
	}
	if !pluginProjectKeyPattern.MatchString(cursor.ProjectKey) ||
		!pluginIDPattern.MatchString(cursor.PluginID) || cursor.AfterSeq < 1 {
		return pluginAuditPageCursor{}, errors.New("audit cursor is invalid")
	}
	return cursor, nil
}

func validatePluginAuditPageParams(raw json.RawMessage) (pluginAuditPageParams, error) {
	var params pluginAuditPageParams
	if err := decodePluginParams(raw, &params); err != nil {
		return params, err
	}
	if !pluginProjectKeyPattern.MatchString(params.ProjectKey) ||
		!pluginIDPattern.MatchString(params.PluginID) {
		return params, errors.New("project or plugin identity is invalid")
	}
	if params.Cursor != "" {
		cursor, err := decodePluginAuditPageCursor(params.Cursor)
		if err != nil {
			return params, err
		}
		// A cursor only continues its issuing scope; any other project or
		// plugin continuation is rejected before storage is touched.
		if cursor.ProjectKey != params.ProjectKey || cursor.PluginID != params.PluginID {
			return params, errors.New("audit cursor scope is invalid")
		}
	}
	return params, nil
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
		Details: view.Details,
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
	listAuditPage := productrpc.Registration{
		Method: "plugin.listAuditPage",
		Scope:  productcapabilities.WorkspaceScope,
		ValidateParams: func(raw json.RawMessage) error {
			params, err := validatePluginAuditPageParams(raw)
			if err != nil {
				return err
			}
			return service.ValidateProject(params.ProjectKey)
		},
		Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
			params, err := validatePluginAuditPageParams(raw)
			if err != nil {
				return nil, err
			}
			var afterSeq int64
			if params.Cursor != "" {
				cursor, cursorErr := decodePluginAuditPageCursor(params.Cursor)
				if cursorErr != nil {
					return nil, cursorErr
				}
				afterSeq = cursor.AfterSeq
			}
			items, nextAfterSeq, hasMore, listErr := service.ListAuditSummaryPage(
				ctx, params.ProjectKey, params.PluginID, afterSeq,
				pluginstore.AuditSummaryPageSize, pluginstore.AuditSummaryPageBudgetBytes,
			)
			if listErr != nil {
				return nil, pluginPublicError(listErr)
			}
			result := pluginAuditPageResult{Events: items}
			if hasMore && len(items) > 0 {
				next := encodePluginAuditPageCursor(pluginAuditPageCursor{
					ProjectKey: params.ProjectKey,
					PluginID:   params.PluginID,
					AfterSeq:   nextAfterSeq,
				})
				result.NextCursor = &next
			}
			return result, nil
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
		listCatalog, listAudit, listAuditPage, listPendingCleanup, setEnabled,
	}
}

func pluginPayloadList(items []json.RawMessage) []json.RawMessage {
	result := make([]json.RawMessage, 0, len(items))
	result = append(result, items...)
	return result
}
