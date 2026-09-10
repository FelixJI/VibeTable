package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"unicode/utf8"

	"github.com/vibetable/vibetable/sidecar/internal/contracts/productcapabilities"
	"github.com/vibetable/vibetable/sidecar/internal/metadata"
	"github.com/vibetable/vibetable/sidecar/internal/productrpc"
)

type workCalendarPort interface {
	ReadWorkCalendar(context.Context) (metadata.WorkCalendarResult, error)
	CommitWorkCalendar(context.Context, metadata.WorkCalendarCommit) (metadata.WorkCalendarReceipt, error)
}

func workCalendarReadRegistration(port workCalendarPort) productrpc.Registration {
	return productrpc.Registration{Method: "settings.readWorkCalendar", Scope: productcapabilities.WorkspaceScope,
		ValidateParams: func(raw json.RawMessage) error {
			object, err := decodeWorkCalendarObject(raw)
			if err != nil {
				return err
			}
			if len(object) != 0 {
				return errors.New("calendar read requires empty params")
			}
			return nil
		},
		Handler: func(ctx context.Context, _ json.RawMessage) (any, error) {
			if port == nil {
				return nil, errors.New("calendar unavailable")
			}
			result, err := port.ReadWorkCalendar(ctx)
			return result, workCalendarPublicError(err)
		},
	}
}

func decodeWorkCalendarCommit(raw json.RawMessage) (metadata.WorkCalendarCommit, error) {
	object, err := decodeWorkCalendarObject(raw)
	if err != nil {
		return metadata.WorkCalendarCommit{}, err
	}
	if len(object) != 3 {
		return metadata.WorkCalendarCommit{}, errors.New("calendar commit requires three fields")
	}
	revision, ok := object["expectedRevision"].(string)
	if !ok {
		return metadata.WorkCalendarCommit{}, errors.New("calendar revision is required")
	}
	key, ok := object["idempotencyKey"].(string)
	if !ok {
		return metadata.WorkCalendarCommit{}, errors.New("calendar idempotency key is required")
	}
	list, ok := object["overrides"].([]any)
	if !ok {
		return metadata.WorkCalendarCommit{}, errors.New("calendar overrides must be an array")
	}
	values := make([]metadata.WorkCalendarOverride, 0, len(list))
	for _, rawItem := range list {
		item, ok := rawItem.(map[string]any)
		if !ok || len(item) != 3 {
			return metadata.WorkCalendarCommit{}, errors.New("calendar override requires date, kind and name")
		}
		date, dateOK := item["date"].(string)
		kind, kindOK := item["kind"].(string)
		name, nameOK := item["name"].(string)
		if !dateOK || !kindOK || !nameOK {
			return metadata.WorkCalendarCommit{}, errors.New("calendar override fields must be strings")
		}
		values = append(values, metadata.WorkCalendarOverride{Date: date, Kind: kind, Name: name})
	}
	return metadata.WorkCalendarCommit{Overrides: values, ExpectedRevision: revision, IdempotencyKey: key}, nil
}

func workCalendarCommitRegistration(port workCalendarPort, gates ...businessWriteGate) productrpc.Registration {
	return productrpc.Registration{Method: "settings.commitWorkCalendar", Scope: productcapabilities.WorkspaceScope,
		ValidateParams: func(raw json.RawMessage) error { _, err := decodeWorkCalendarCommit(raw); return err },
		Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
			request, err := decodeWorkCalendarCommit(raw)
			if err != nil {
				return nil, err
			}
			if port == nil {
				return nil, errors.New("calendar unavailable")
			}
			var result metadata.WorkCalendarReceipt
			err = runBusinessWrite(ctx, gates, "settings.workCalendar.commit", request.IdempotencyKey, func(writeContext context.Context) error {
				var applyErr error
				result, applyErr = port.CommitWorkCalendar(writeContext, request)
				return applyErr
			})
			return result, workCalendarPublicError(err)
		},
	}
}

func workCalendarPublicError(err error) error {
	if err == nil {
		return nil
	}
	var domain *metadata.Error
	if !errors.As(err, &domain) || domain == nil {
		return err
	}
	switch domain.Code {
	case "settings.calendar.invalid", "settings.calendar.revision_conflict", "settings.calendar.corrupt", "metadata.idempotency_conflict", "metadata.storage.failed", "metadata.request.invalid":
		path := domain.Path
		return &productrpc.PublicError{Code: domain.Code, Path: &path, Message: "Shared work calendar operation failed", Retryable: domain.Retryable}
	default:
		return errors.New("shared calendar failed")
	}
}

func decodeWorkCalendarObject(raw json.RawMessage) (map[string]any, error) {
	if len(raw) == 0 || len(raw) > maxMetadataRequestBytes || !utf8.Valid(raw) || !json.Valid(raw) {
		return nil, errors.New("invalid calendar JSON")
	}
	for index := 0; index < len(raw); index++ {
		if raw[index] != '"' {
			continue
		}
		start := index
		for index++; index < len(raw); index++ {
			if raw[index] == '\\' {
				index++
				continue
			}
			if raw[index] == '"' {
				break
			}
		}
		if !lookupStringHasUnicodeScalars(raw[start : index+1]) {
			return nil, errors.New("calendar JSON requires Unicode scalars")
		}
	}
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if decoder.Decode(&value) != nil {
		return nil, errors.New("invalid calendar JSON")
	}
	if err := validateQueryViewValue(value, 0); err != nil {
		return nil, err
	}
	object, ok := value.(map[string]any)
	if !ok {
		return nil, errors.New("calendar params must be an object")
	}
	return object, nil
}
