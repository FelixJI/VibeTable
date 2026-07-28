// Package auditledger owns the append-only audit database that lives outside
// the replaceable business data directory.
package auditledger

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

const (
	ledgerFileName = "ledger.db"
	hashPrefix     = "sha256:"
)

var (
	ErrInvalidEnvelope = errors.New("audit.invalid_envelope")
	ErrPayloadMismatch = errors.New("audit.payload_hash_mismatch")
	ErrEventConflict   = errors.New("audit.event_conflict")
	ErrSourceConflict  = errors.New("audit.source_sequence_conflict")
	ErrSourceGap       = errors.New("audit.source_sequence_gap")
	ErrChainCorrupt    = errors.New("audit.chain_corrupt")
)

type Envelope struct {
	EventID          string          `json:"eventId"`
	SourceEpoch      string          `json:"sourceEpoch"`
	SourceSequence   uint64          `json:"sourceSequence"`
	MutationIdentity string          `json:"mutationIdentity"`
	PayloadHash      string          `json:"payloadHash"`
	Payload          json.RawMessage `json:"payload"`
	OccurredAt       time.Time       `json:"occurredAt"`
}

type Record struct {
	LedgerSequence uint64   `json:"ledgerSequence"`
	PreviousHash   string   `json:"previousHash"`
	Hash           string   `json:"hash"`
	Envelope       Envelope `json:"envelope"`
}

type Anchor struct {
	SourceEpoch    string `json:"sourceEpoch"`
	SourceSequence uint64 `json:"sourceSequence"`
	LedgerSequence uint64 `json:"ledgerSequence"`
	Hash           string `json:"hash"`
}

type AppendResult struct {
	Record    Record
	Duplicate bool
}

type Prefix struct {
	FormatVersion int      `json:"formatVersion"`
	Anchor        Anchor   `json:"anchor"`
	Records       []Record `json:"records"`
}

type Ledger struct {
	mu sync.Mutex

	db         *sql.DB
	path       string
	records    []Record
	events     map[string]int
	sourceKeys map[string]string
	sourceHigh map[string]uint64
}

func Open(root string) (*Ledger, error) {
	if strings.TrimSpace(root) == "" {
		return nil, errors.New("audit.root_required")
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, fmt.Errorf("create audit root: %w", err)
	}
	path := filepath.Join(root, ledgerFileName)
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open audit ledger: %w", err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`
		PRAGMA journal_mode=WAL;
		PRAGMA synchronous=FULL;
		PRAGMA foreign_keys=ON;
		CREATE TABLE IF NOT EXISTS audit_ledger (
			ledger_sequence INTEGER PRIMARY KEY,
			event_id TEXT NOT NULL UNIQUE,
			source_epoch TEXT NOT NULL,
			source_sequence INTEGER NOT NULL,
			mutation_identity TEXT NOT NULL,
			payload_hash TEXT NOT NULL,
			payload BLOB NOT NULL,
			occurred_at TEXT NOT NULL,
			previous_hash TEXT NOT NULL,
			hash TEXT NOT NULL,
			UNIQUE(source_epoch, source_sequence)
		);
	`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("initialize audit ledger: %w", err)
	}
	ledger := &Ledger{
		db:         db,
		path:       path,
		events:     map[string]int{},
		sourceKeys: map[string]string{},
		sourceHigh: map[string]uint64{},
	}
	if err := ledger.load(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return ledger, nil
}

func NewEnvelope(
	eventID string,
	sourceEpoch string,
	sourceSequence uint64,
	mutationIdentity string,
	payload json.RawMessage,
	occurredAt time.Time,
) (Envelope, error) {
	canonical, err := canonicalJSON(payload)
	if err != nil {
		return Envelope{}, err
	}
	return Envelope{
		EventID:          eventID,
		SourceEpoch:      sourceEpoch,
		SourceSequence:   sourceSequence,
		MutationIdentity: mutationIdentity,
		PayloadHash:      digest(canonical),
		Payload:          canonical,
		OccurredAt:       occurredAt.UTC(),
	}, nil
}

func (ledger *Ledger) Close() error {
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	if ledger.db == nil {
		return nil
	}
	err := ledger.db.Close()
	ledger.db = nil
	return err
}

func (ledger *Ledger) Append(
	ctx context.Context,
	envelope Envelope,
) (AppendResult, error) {
	if err := ctx.Err(); err != nil {
		return AppendResult{}, err
	}
	normalized, err := normalizeEnvelope(envelope)
	if err != nil {
		return AppendResult{}, err
	}

	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	if ledger.db == nil {
		return AppendResult{}, errors.New("audit.ledger_closed")
	}
	if index, exists := ledger.events[normalized.EventID]; exists {
		existing := ledger.records[index]
		if !envelopesEqual(existing.Envelope, normalized) {
			return AppendResult{}, ErrEventConflict
		}
		return AppendResult{Record: existing, Duplicate: true}, nil
	}
	sourceKey := envelopeSourceKey(normalized)
	if eventID, exists := ledger.sourceKeys[sourceKey]; exists &&
		eventID != normalized.EventID {
		return AppendResult{}, ErrSourceConflict
	}
	if normalized.SourceSequence != ledger.sourceHigh[normalized.SourceEpoch]+1 {
		return AppendResult{}, ErrSourceGap
	}

	previous := ""
	if len(ledger.records) > 0 {
		previous = ledger.records[len(ledger.records)-1].Hash
	}
	record := Record{
		LedgerSequence: uint64(len(ledger.records)) + 1,
		PreviousHash:   previous,
		Envelope:       normalized,
	}
	record.Hash, err = recordHash(record)
	if err != nil {
		return AppendResult{}, err
	}
	transaction, err := ledger.db.BeginTx(ctx, nil)
	if err != nil {
		return AppendResult{}, fmt.Errorf("begin audit append: %w", err)
	}
	defer transaction.Rollback()
	if _, err := transaction.ExecContext(
		ctx,
		`INSERT INTO audit_ledger (
			ledger_sequence, event_id, source_epoch, source_sequence,
			mutation_identity, payload_hash, payload, occurred_at,
			previous_hash, hash
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		record.LedgerSequence,
		record.Envelope.EventID,
		record.Envelope.SourceEpoch,
		record.Envelope.SourceSequence,
		record.Envelope.MutationIdentity,
		record.Envelope.PayloadHash,
		[]byte(record.Envelope.Payload),
		record.Envelope.OccurredAt.Format(time.RFC3339Nano),
		record.PreviousHash,
		record.Hash,
	); err != nil {
		return AppendResult{}, fmt.Errorf("append audit record: %w", err)
	}
	if err := transaction.Commit(); err != nil {
		return AppendResult{}, fmt.Errorf("flush audit record: %w", err)
	}
	ledger.events[normalized.EventID] = len(ledger.records)
	ledger.sourceKeys[sourceKey] = normalized.EventID
	ledger.sourceHigh[normalized.SourceEpoch] = normalized.SourceSequence
	ledger.records = append(ledger.records, record)
	return AppendResult{Record: record}, nil
}

func (ledger *Ledger) Anchor() Anchor {
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	if len(ledger.records) == 0 {
		return Anchor{}
	}
	last := ledger.records[len(ledger.records)-1]
	return Anchor{
		SourceEpoch:    last.Envelope.SourceEpoch,
		SourceSequence: last.Envelope.SourceSequence,
		LedgerSequence: last.LedgerSequence,
		Hash:           last.Hash,
	}
}

func (ledger *Ledger) Records(after uint64, limit int) []Record {
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	if limit <= 0 {
		return []Record{}
	}
	result := make([]Record, 0, limit)
	for _, record := range ledger.records {
		if record.LedgerSequence <= after {
			continue
		}
		result = append(result, cloneRecord(record))
		if len(result) == limit {
			break
		}
	}
	return result
}

func (ledger *Ledger) Verify() error {
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	return verifyRecords(ledger.records)
}

func (ledger *Ledger) ExportPrefix(anchor Anchor) ([]byte, string, error) {
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	if anchor.LedgerSequence == 0 || anchor.LedgerSequence > uint64(len(ledger.records)) {
		return nil, "", ErrChainCorrupt
	}
	record := ledger.records[anchor.LedgerSequence-1]
	if record.Hash != anchor.Hash ||
		record.Envelope.SourceEpoch != anchor.SourceEpoch ||
		record.Envelope.SourceSequence != anchor.SourceSequence {
		return nil, "", ErrChainCorrupt
	}
	prefix := Prefix{
		FormatVersion: 1,
		Anchor:        anchor,
		Records:       make([]Record, int(anchor.LedgerSequence)),
	}
	for index := range prefix.Records {
		prefix.Records[index] = cloneRecord(ledger.records[index])
	}
	raw, err := json.Marshal(prefix)
	if err != nil {
		return nil, "", err
	}
	return raw, digest(raw), nil
}

func VerifyPrefix(raw []byte) (Anchor, error) {
	var prefix Prefix
	if err := decodeStrict(raw, &prefix); err != nil ||
		prefix.FormatVersion != 1 || prefix.Anchor.LedgerSequence != uint64(len(prefix.Records)) {
		return Anchor{}, ErrChainCorrupt
	}
	if err := verifyRecords(prefix.Records); err != nil {
		return Anchor{}, err
	}
	if len(prefix.Records) == 0 {
		return Anchor{}, ErrChainCorrupt
	}
	last := prefix.Records[len(prefix.Records)-1]
	if last.Hash != prefix.Anchor.Hash ||
		last.Envelope.SourceEpoch != prefix.Anchor.SourceEpoch ||
		last.Envelope.SourceSequence != prefix.Anchor.SourceSequence {
		return Anchor{}, ErrChainCorrupt
	}
	return prefix.Anchor, nil
}

func (ledger *Ledger) load() error {
	rows, err := ledger.db.Query(`
		SELECT ledger_sequence, event_id, source_epoch, source_sequence,
		       mutation_identity, payload_hash, payload, occurred_at,
		       previous_hash, hash
		FROM audit_ledger ORDER BY ledger_sequence`)
	if err != nil {
		return fmt.Errorf("%w: read ledger: %v", ErrChainCorrupt, err)
	}
	defer rows.Close()
	line := 0
	for rows.Next() {
		line++
		var (
			record     Record
			payload    []byte
			occurredAt string
		)
		if err := rows.Scan(
			&record.LedgerSequence,
			&record.Envelope.EventID,
			&record.Envelope.SourceEpoch,
			&record.Envelope.SourceSequence,
			&record.Envelope.MutationIdentity,
			&record.Envelope.PayloadHash,
			&payload,
			&occurredAt,
			&record.PreviousHash,
			&record.Hash,
		); err != nil {
			return fmt.Errorf("%w: row %d: %v", ErrChainCorrupt, line, err)
		}
		record.Envelope.Payload = append(json.RawMessage(nil), payload...)
		record.Envelope.OccurredAt, err = time.Parse(time.RFC3339Nano, occurredAt)
		if err != nil {
			return fmt.Errorf("%w: row %d timestamp: %v", ErrChainCorrupt, line, err)
		}
		normalized, err := normalizeEnvelope(record.Envelope)
		if err != nil {
			return fmt.Errorf("%w: row %d: %v", ErrChainCorrupt, line, err)
		}
		record.Envelope = normalized
		if record.LedgerSequence != uint64(line) {
			return fmt.Errorf("%w: row %d has sequence %d", ErrChainCorrupt, line, record.LedgerSequence)
		}
		if _, exists := ledger.events[record.Envelope.EventID]; exists {
			return fmt.Errorf("%w: duplicate event %s", ErrChainCorrupt, record.Envelope.EventID)
		}
		sourceKey := envelopeSourceKey(record.Envelope)
		if _, exists := ledger.sourceKeys[sourceKey]; exists {
			return fmt.Errorf("%w: duplicate source sequence %s", ErrChainCorrupt, sourceKey)
		}
		if record.Envelope.SourceSequence != ledger.sourceHigh[record.Envelope.SourceEpoch]+1 {
			return fmt.Errorf("%w: non-contiguous source sequence %s", ErrChainCorrupt, sourceKey)
		}
		ledger.events[record.Envelope.EventID] = len(ledger.records)
		ledger.sourceKeys[sourceKey] = record.Envelope.EventID
		ledger.sourceHigh[record.Envelope.SourceEpoch] = record.Envelope.SourceSequence
		ledger.records = append(ledger.records, record)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("%w: scan ledger: %v", ErrChainCorrupt, err)
	}
	if err := verifyRecords(ledger.records); err != nil {
		return err
	}
	return nil
}

func verifyRecords(records []Record) error {
	previous := ""
	for index, record := range records {
		if record.LedgerSequence != uint64(index+1) ||
			record.PreviousHash != previous {
			return fmt.Errorf("%w: invalid link at sequence %d", ErrChainCorrupt, record.LedgerSequence)
		}
		expected, err := recordHash(record)
		if err != nil {
			return fmt.Errorf("%w: %v", ErrChainCorrupt, err)
		}
		if record.Hash != expected {
			return fmt.Errorf("%w: invalid hash at sequence %d", ErrChainCorrupt, record.LedgerSequence)
		}
		previous = record.Hash
	}
	return nil
}

func normalizeEnvelope(envelope Envelope) (Envelope, error) {
	if strings.TrimSpace(envelope.EventID) == "" ||
		strings.TrimSpace(envelope.SourceEpoch) == "" ||
		envelope.SourceSequence == 0 ||
		strings.TrimSpace(envelope.MutationIdentity) == "" ||
		envelope.OccurredAt.IsZero() {
		return Envelope{}, ErrInvalidEnvelope
	}
	canonical, err := canonicalJSON(envelope.Payload)
	if err != nil {
		return Envelope{}, errors.Join(ErrInvalidEnvelope, err)
	}
	if envelope.PayloadHash != digest(canonical) {
		return Envelope{}, ErrPayloadMismatch
	}
	envelope.Payload = canonical
	envelope.OccurredAt = envelope.OccurredAt.UTC()
	return envelope, nil
}

func canonicalJSON(raw json.RawMessage) ([]byte, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, fmt.Errorf("decode audit payload: %w", err)
	}
	if value == nil {
		return nil, errors.New("audit payload must not be null")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, errors.New("audit payload contains trailing JSON")
		}
		return nil, fmt.Errorf("decode trailing audit payload: %w", err)
	}
	canonical, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("encode canonical audit payload: %w", err)
	}
	return canonical, nil
}

func recordHash(record Record) (string, error) {
	value := struct {
		LedgerSequence uint64   `json:"ledgerSequence"`
		PreviousHash   string   `json:"previousHash"`
		Envelope       Envelope `json:"envelope"`
	}{
		LedgerSequence: record.LedgerSequence,
		PreviousHash:   record.PreviousHash,
		Envelope:       record.Envelope,
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("encode audit hash input: %w", err)
	}
	return digest(raw), nil
}

func digest(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hashPrefix + hex.EncodeToString(sum[:])
}

func envelopeSourceKey(envelope Envelope) string {
	return fmt.Sprintf("%s\x00%020d", envelope.SourceEpoch, envelope.SourceSequence)
}

func envelopesEqual(left, right Envelope) bool {
	leftRaw, leftErr := json.Marshal(left)
	rightRaw, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && bytes.Equal(leftRaw, rightRaw)
}

func cloneRecord(record Record) Record {
	record.Envelope.Payload = append(json.RawMessage(nil), record.Envelope.Payload...)
	return record
}

func decodeStrict(raw []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("unexpected trailing JSON")
		}
		return err
	}
	return nil
}

// SortEnvelopes provides the deterministic source ordering required by
// outbox implementations that read pending rows from multiple epochs.
func SortEnvelopes(envelopes []Envelope) {
	sort.SliceStable(envelopes, func(left, right int) bool {
		if envelopes[left].SourceEpoch != envelopes[right].SourceEpoch {
			return envelopes[left].SourceEpoch < envelopes[right].SourceEpoch
		}
		if envelopes[left].SourceSequence != envelopes[right].SourceSequence {
			return envelopes[left].SourceSequence < envelopes[right].SourceSequence
		}
		return envelopes[left].EventID < envelopes[right].EventID
	})
}
