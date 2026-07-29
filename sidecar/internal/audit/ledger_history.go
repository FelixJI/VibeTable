package audit

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"sort"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	"github.com/vibetable/vibetable/sidecar/internal/auditledger"
	"github.com/vibetable/vibetable/sidecar/internal/mutation"
)

const (
	businessHistoryEpoch = "business-v2"
	businessReceiptType  = "workspace.v2.business-mutation"
)

type ledgerHistoryPayload struct {
	RevisionID     *string        `json:"revisionId"`
	ChangeSetID    string         `json:"changeSetId"`
	Sequence       int64          `json:"sequence"`
	TableID        string         `json:"tableId"`
	RecordID       string         `json:"recordId"`
	Operation      string         `json:"operation"`
	Before         map[string]any `json:"before"`
	After          map[string]any `json:"after"`
	SchemaRevision int64          `json:"schemaRevision"`
	DataRevision   int64          `json:"dataRevision"`
	RequestID      string         `json:"requestId"`
	Actor          mutation.Actor `json:"actor"`
}

type ledgerBusinessReceipt struct {
	Type             string `json:"type"`
	WorkspaceID      string `json:"workspaceId"`
	SessionEpoch     uint64 `json:"sessionEpoch"`
	FenceEpoch       uint64 `json:"fenceEpoch"`
	ClaimID          string `json:"claimId"`
	MutationRevision uint64 `json:"mutationRevision"`
	Kind             string `json:"kind"`
	Identity         string `json:"identity"`
}

func (service *Service) readHistoryEvents(
	ctx context.Context,
	tableID string,
	recordID *string,
	actorID *string,
) ([]*core.Record, error) {
	if service.ledger == nil {
		filter := "table_id={:table}"
		bindings := dbx.Params{"table": tableID}
		if recordID != nil {
			filter += " && record_id={:record}"
			bindings["record"] = *recordID
		}
		if actorID != nil {
			filter += " && actor_id={:actor}"
			bindings["actor"] = *actorID
		}
		return service.app.FindRecordsByFilter(
			"vibetable_audit_events",
			filter,
			"-data_revision,-sequence",
			maxHistoryScanEvents+1,
			0,
			bindings,
		)
	}
	events, err := service.projectLedgerHistory(ctx)
	if err != nil {
		return nil, err
	}
	filtered := make([]*core.Record, 0, len(events))
	for _, event := range events {
		if event.GetString("table_id") != tableID {
			continue
		}
		if recordID != nil && event.GetString("record_id") != *recordID {
			continue
		}
		if actorID != nil && event.GetString("actor_id") != *actorID {
			continue
		}
		filtered = append(filtered, event)
	}
	sort.SliceStable(filtered, func(left, right int) bool {
		// dataRevision belongs to the replaceable business database and can
		// repeat or move backwards after snapshot restore. The external ledger
		// sequence is the only monotonic ordering key across restore epochs.
		return filtered[left].GetFloat("ledger_sequence") >
			filtered[right].GetFloat("ledger_sequence")
	})
	return filtered, nil
}

func (service *Service) findHistoryEvent(
	ctx context.Context,
	revisionID string,
) (*core.Record, error) {
	if service.ledger == nil {
		return service.app.FindRecordById(
			"vibetable_audit_events",
			revisionID,
		)
	}
	events, err := service.projectLedgerHistory(ctx)
	if err != nil {
		return nil, err
	}
	for _, event := range events {
		if event.Id == revisionID {
			return event, nil
		}
	}
	return nil, sql.ErrNoRows
}

func (service *Service) projectLedgerHistory(
	ctx context.Context,
) ([]*core.Record, error) {
	records, err := service.ledger.VerifiedRecords(ctx)
	if err != nil {
		return nil, err
	}
	collection := core.NewBaseCollection("vibetable_ledger_history")
	projected := make([]*core.Record, 0, len(records))
	for _, record := range records {
		if !isBusinessHistoryEpoch(record.Envelope.SourceEpoch) {
			if strings.HasPrefix(
				record.Envelope.SourceEpoch,
				businessHistoryEpoch+":",
			) {
				return nil, auditledger.ErrChainCorrupt
			}
			continue
		}
		payload, include, err := decodeLedgerHistoryPayload(
			record.Envelope.Payload,
		)
		if err != nil {
			return nil, errors.Join(auditledger.ErrChainCorrupt, err)
		}
		if !include {
			continue
		}
		revisionID := record.Envelope.EventID
		if payload.RevisionID != nil {
			revisionID = strings.TrimSpace(*payload.RevisionID)
		}
		if revisionID == "" ||
			payload.ChangeSetID == "" ||
			payload.Sequence < 1 ||
			payload.DataRevision < 1 ||
			payload.SchemaRevision < 1 ||
			payload.TableID == "" ||
			payload.RecordID == "" ||
			payload.RequestID == "" ||
			payload.Actor.Type == "" ||
			payload.Actor.ID == "" {
			return nil, auditledger.ErrChainCorrupt
		}
		switch mutation.OperationKind(payload.Operation) {
		case mutation.OperationInsert,
			mutation.OperationUpdate,
			mutation.OperationArchive,
			mutation.OperationRestore,
			mutation.OperationDelete,
			mutation.OperationSetAttachments:
		default:
			return nil, auditledger.ErrChainCorrupt
		}
		event := core.NewRecord(collection)
		event.Set("id", revisionID)
		event.Set("ledger_sequence", record.LedgerSequence)
		event.Set("change_set_id", payload.ChangeSetID)
		event.Set("sequence", payload.Sequence)
		event.Set("data_revision", payload.DataRevision)
		event.Set("table_id", payload.TableID)
		event.Set("record_id", payload.RecordID)
		event.Set("operation", payload.Operation)
		event.Set("before_json", payload.Before)
		event.Set("after_json", payload.After)
		event.Set("schema_revision", payload.SchemaRevision)
		event.Set("request_id", payload.RequestID)
		event.Set("actor_type", payload.Actor.Type)
		event.Set("actor_id", payload.Actor.ID)
		if payload.Actor.DisplayName != nil {
			event.Set("actor_display_name", *payload.Actor.DisplayName)
		}
		event.Set("occurred_at", record.Envelope.OccurredAt)
		projected = append(projected, event)
	}
	return projected, nil
}

func isBusinessHistoryEpoch(sourceEpoch string) bool {
	if sourceEpoch == businessHistoryEpoch {
		return true
	}
	parts := strings.Split(sourceEpoch, ":")
	if len(parts) != 3 || parts[0] != businessHistoryEpoch {
		return false
	}
	workspaceID, err := uuid.Parse(parts[1])
	if err != nil || workspaceID == uuid.Nil ||
		strings.ToLower(parts[1]) != parts[1] {
		return false
	}
	counter, err := strconv.ParseUint(parts[2], 10, 64)
	return err == nil && counter > 1
}

func decodeLedgerHistoryPayload(
	raw json.RawMessage,
) (ledgerHistoryPayload, bool, error) {
	var header map[string]json.RawMessage
	if err := json.Unmarshal(raw, &header); err != nil {
		return ledgerHistoryPayload{}, false, err
	}
	if typeRaw, typed := header["type"]; typed {
		var eventType string
		if err := json.Unmarshal(typeRaw, &eventType); err != nil ||
			eventType != businessReceiptType {
			return ledgerHistoryPayload{}, false, errors.New(
				"audit.ledger_history_type_invalid",
			)
		}
		var receipt ledgerBusinessReceipt
		if err := decodeLedgerObject(raw, &receipt); err != nil ||
			receipt.Type != businessReceiptType ||
			receipt.WorkspaceID == "" ||
			receipt.SessionEpoch == 0 ||
			receipt.FenceEpoch == 0 ||
			receipt.ClaimID == "" ||
			receipt.MutationRevision == 0 ||
			receipt.Kind == "" ||
			receipt.Identity == "" {
			if err == nil {
				err = errors.New("audit.ledger_business_receipt_invalid")
			}
			return ledgerHistoryPayload{}, false, err
		}
		return ledgerHistoryPayload{}, false, nil
	}
	var payload ledgerHistoryPayload
	if err := decodeLedgerObject(raw, &payload); err != nil {
		return ledgerHistoryPayload{}, false, err
	}
	return payload, true, nil
}

func decodeLedgerObject(raw json.RawMessage, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			err = errors.New("audit.ledger_history_trailing_json")
		}
		return err
	}
	return nil
}
