package app

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/vibetable/vibetable/sidecar/internal/contracts/productcapabilities"
	"github.com/vibetable/vibetable/sidecar/internal/contracts/workbench"
	"github.com/vibetable/vibetable/sidecar/internal/metadata"
	"github.com/vibetable/vibetable/sidecar/internal/productrpc"
)

func contentMetadataRegistration(method string, service *metadata.ContentService, gates ...businessWriteGate) productrpc.Registration {
	return productrpc.Registration{Method: method, Scope: productcapabilities.WorkspaceScope,
		ValidateParams: func(raw json.RawMessage) error { _, err := metadata.DecodeContentParams(method, raw); return err },
		Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
			params, err := metadata.DecodeContentParams(method, raw)
			if err != nil {
				return nil, err
			}
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			key := ""
			switch value := params.(type) {
			case workbench.ContentProfileCommitRequest:
				key = value.IdempotencyKey
			case workbench.ContentProfileDeleteRequest:
				key = value.IdempotencyKey
			case workbench.RecordDocumentLinkCommitRequest:
				key = value.IdempotencyKey
			case workbench.RecordDocumentLinkRepairRequest:
				key = value.IdempotencyKey
			case workbench.RecordDocumentLinkDeleteRequest:
				key = value.IdempotencyKey
			}
			var result any
			apply := func(call context.Context) error {
				var err error
				switch value := params.(type) {
				case workbench.ContentProfileLoadRequest:
					result, err = service.LoadProfile(call, value.TableId)
				case workbench.ContentProfileCommitRequest:
					result, err = service.CommitProfile(call, value)
				case workbench.ContentProfileDeleteRequest:
					result, err = service.DeleteProfile(call, value)
				case workbench.RecordDocumentLinkListRequest:
					result, err = service.ListLinks(call, value.TableId, value.RecordId)
				case workbench.RecordDocumentLinkCommitRequest:
					result, err = service.CommitLink(call, value)
				case workbench.RecordDocumentLinkRepairRequest:
					result, err = service.RepairLink(call, value)
				case workbench.RecordDocumentLinkDeleteRequest:
					result, err = service.DeleteLink(call, value)
				}
				return err
			}
			if key == "" {
				err = apply(ctx)
			} else {
				namespace := "content_profiles"
				if strings.HasPrefix(method, "recordDocumentLink.") {
					namespace = "record_document_links"
				}
				operation := "upsert"
				if strings.HasSuffix(method, ".delete") {
					operation = "delete"
				}
				err = runIdempotentBusinessWrite(ctx, gates, "metadata."+namespace+"."+operation, key, apply)
			}
			if err != nil {
				var domain *metadata.ContentError
				if errors.As(err, &domain) {
					return nil, &productrpc.ContentModelError{Code: domain.Code, Message: domain.Message, Path: domain.Path}
				}
				return nil, err
			}
			return result, nil
		},
	}
}
