package objectrepo

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"io"
)

// Compression is private to the Kopia manifest. Public Payload bytes retain the
// same RawMessage normalization used by Kopia's uncompressed PutManifest path.
// The limits choose an encoding; larger records retain the raw representation.
const kopiaEnvelopeLimit = 32 << 20

type kopiaManifestEnvelope struct {
	ID              ManifestID        `json:"id"`
	Name            string            `json:"name"`
	Labels          map[string]string `json:"labels"`
	Payload         json.RawMessage   `json:"payload,omitempty"`
	PayloadEncoding *string           `json:"payloadEncoding,omitempty"`
	PayloadSize     *int64            `json:"payloadSize,omitempty"`
	PayloadData     *[]byte           `json:"payloadData,omitempty"`
}

func encodeKopiaManifest(record ManifestRecord) (any, error) {
	if len(record.Payload) < 1<<20 {
		return record, nil
	}
	normalized, err := json.Marshal(record.Payload)
	if err != nil {
		return nil, err
	}
	if len(normalized) < 1<<20 || len(normalized) > kopiaEnvelopeLimit {
		return record, nil
	}
	var compressed bytes.Buffer
	zipper := gzip.NewWriter(&compressed)
	if _, err := zipper.Write(normalized); err != nil {
		return nil, errors.Join(err, zipper.Close())
	}
	if err := zipper.Close(); err != nil {
		return nil, err
	}
	encoding := "gzip-v1"
	size := int64(len(normalized))
	data := compressed.Bytes()
	return kopiaManifestEnvelope{ID: record.ID, Name: record.Name, Labels: record.Labels,
		PayloadEncoding: &encoding, PayloadSize: &size, PayloadData: &data}, nil
}

func decodeKopiaManifest(wire kopiaManifestEnvelope) (ManifestRecord, error) {
	record := ManifestRecord{ID: wire.ID, Name: wire.Name, Labels: wire.Labels, Payload: wire.Payload}
	if wire.PayloadEncoding == nil {
		if wire.PayloadSize != nil || wire.PayloadData != nil {
			return ManifestRecord{}, ErrCorrupt
		}
		return record, nil
	}
	if *wire.PayloadEncoding != "gzip-v1" || len(wire.Payload) != 0 || wire.PayloadSize == nil || wire.PayloadData == nil ||
		*wire.PayloadSize < 1 || *wire.PayloadSize > kopiaEnvelopeLimit {
		return ManifestRecord{}, ErrCorrupt
	}
	compressed := bytes.NewReader(*wire.PayloadData)
	unzipper, err := gzip.NewReader(compressed)
	if err != nil {
		return ManifestRecord{}, errors.Join(ErrCorrupt, err)
	}
	unzipper.Multistream(false)
	raw := make([]byte, int(*wire.PayloadSize)+1)
	count, readErr := io.ReadFull(unzipper, raw)
	var extra [1]byte
	extraCount, endErr := unzipper.Read(extra[:])
	closeErr := unzipper.Close()
	if count != int(*wire.PayloadSize) || !errors.Is(readErr, io.ErrUnexpectedEOF) || extraCount != 0 ||
		!errors.Is(endErr, io.EOF) || closeErr != nil || compressed.Len() != 0 {
		return ManifestRecord{}, ErrCorrupt
	}
	record.Payload = raw[:count]
	return record, nil
}
