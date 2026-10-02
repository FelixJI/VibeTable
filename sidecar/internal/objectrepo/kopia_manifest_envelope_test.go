package objectrepo

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	kopiarepo "github.com/kopia/kopia/repo"
	kopiamanifest "github.com/kopia/kopia/repo/manifest"
)

func TestKopiaCompressedEnvelopeMatchesRawPutGet(t *testing.T) {
	ctx := context.Background()
	repository, authority := workspaceRepository(t)
	adapter := repository.(*KopiaRepository)
	payload := json.RawMessage(`{ "body":"` + strings.Repeat("envelope-oracle-", 90000) + `", "duplicate":1, "duplicate":2, "escape":"<>&` + "\u2028" + `\ud800", "number":9007199254740993 }`)
	legacyInput := ManifestInput{Name: "legacy-oracle", Labels: map[string]string{"type": "envelope", "variant": "legacy"}, Payload: payload}
	legacyID, err := canonicalManifestID(legacyInput)
	if err != nil {
		t.Fatal(err)
	}
	session, writer, err := adapter.repository.NewWriter(ctx, kopiarepo.WriteSessionOptions{Purpose: "envelope old Put/Get oracle"})
	if err != nil {
		t.Fatal(err)
	}
	internal, err := writer.PutManifest(session, map[string]string{"type": "vibetable-manifest", "vibetable.publicId": string(legacyID)}, ManifestRecord{ID: legacyID, Name: legacyInput.Name, Labels: legacyInput.Labels, Payload: payload})
	if err != nil {
		t.Fatal(err)
	}
	var oracle ManifestRecord
	if _, err := writer.GetManifest(session, internal, &oracle); err != nil {
		t.Fatal(err)
	}
	next := cloneKopiaState(adapter.state)
	next.Manifests[string(legacyID)] = string(internal)
	if err := putKopiaState(session, writer, next); err != nil {
		t.Fatal(err)
	}
	if err := writer.Flush(session); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(session); err != nil {
		t.Fatal(err)
	}
	adapter.state = next
	old, err := adapter.GetManifest(ctx, legacyID)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(old.Payload, oracle.Payload) {
		t.Fatal("old stored record Payload differs from actual legacy Put/Get")
	}
	compressedInput := legacyInput
	compressedInput.Name = "compressed-oracle"
	compressedInput.Labels = map[string]string{"type": "envelope", "variant": "compressed"}
	receipt, err := adapter.Commit(ctx, CommitRequest{Authority: authority, Manifests: []ManifestInput{compressedInput}})
	if err != nil {
		t.Fatal(err)
	}
	newID := receipt.Manifests[compressedInput.Name]
	config := strings.TrimSuffix(adapter.lockPath, ".vibetable.lock")
	if err := adapter.Close(ctx); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenKopia(ctx, config, "warm-cache-password")
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close(ctx)
	for _, id := range []ManifestID{legacyID, newID} {
		record, err := reopened.GetManifest(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(record.Payload, oracle.Payload) {
			t.Fatalf("reopened %s Payload != actual legacy oracle", id)
		}
		if err := VerifyManifestRecord(record); err != nil {
			t.Fatal(err)
		}
	}
	var wire kopiaManifestEnvelope
	if _, err := reopened.repository.GetManifest(ctx, internal, &wire); err != nil {
		t.Fatal(err)
	}
	if wire.PayloadEncoding != nil {
		t.Fatal("actual legacy record unexpectedly compressed")
	}
	wire = kopiaManifestEnvelope{}
	newInternal := reopened.state.Manifests[string(newID)]
	if _, err := reopened.repository.GetManifest(ctx, kopiamanifest.ID(newInternal), &wire); err != nil {
		t.Fatal(err)
	}
	if wire.PayloadEncoding == nil || *wire.PayloadEncoding != "gzip-v1" {
		t.Fatal("large envelope record did not use private gzip envelope")
	}
	decoded, decodeErr := decodeKopiaManifest(wire)
	if decodeErr != nil || !bytes.Equal(decoded.Payload, oracle.Payload) {
		t.Fatalf("valid gzip envelope decode: %v", decodeErr)
	}
	t.Logf("actual legacy Put/Get oracle and cold reopen: public bytes identical (%d B), raw representation readable, both identities attest", len(oracle.Payload))
	for _, kind := range []string{"mixed", "unknown", "size", "small-size", "zero-size", "large-size", "missing-size", "missing-data", "missing-encoding", "crc", "trailing", "multimember", "truncated"} {
		t.Run(kind, func(t *testing.T) {
			broken := wire
			data := append([]byte(nil), (*wire.PayloadData)...)
			broken.PayloadData = &data
			size := *wire.PayloadSize
			broken.PayloadSize = &size
			encoding := *wire.PayloadEncoding
			broken.PayloadEncoding = &encoding
			switch kind {
			case "mixed":
				broken.Payload = json.RawMessage(`{}`)
			case "unknown":
				encoding = "other"
			case "size":
				size++
			case "small-size":
				size--
			case "zero-size":
				size = 0
			case "large-size":
				size = kopiaEnvelopeLimit + 1
			case "missing-size":
				broken.PayloadSize = nil
			case "missing-data":
				broken.PayloadData = nil
			case "missing-encoding":
				broken.PayloadEncoding = nil
			case "crc":
				data[len(data)-8] ^= 1
			case "trailing":
				data = append(data, 0)
			case "multimember":
				data = append(data, data...)
			case "truncated":
				data = data[:len(data)-3]
			}
			if _, err := decodeKopiaManifest(broken); !errors.Is(err, ErrCorrupt) {
				t.Fatalf("%s error=%v, want ErrCorrupt", kind, err)
			}
		})
	}
}

func TestKopiaRejectsPreviousStateBeforeAuthority(t *testing.T) {
	ctx := context.Background()
	repository, _ := workspaceRepository(t)
	adapter := repository.(*KopiaRepository)
	config := strings.TrimSuffix(adapter.lockPath, ".vibetable.lock")
	old := cloneKopiaState(adapter.state)
	old.FormatVersion = 2
	session, writer, err := adapter.repository.NewWriter(ctx, kopiarepo.WriteSessionOptions{Purpose: "unsupported state fixture"})
	if err != nil {
		t.Fatal(err)
	}
	if err := putKopiaState(session, writer, old); err != nil {
		t.Fatal(err)
	}
	if err := writer.Flush(session); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(session); err != nil {
		t.Fatal(err)
	}
	if err := adapter.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if reopened, err := OpenKopia(ctx, config, "warm-cache-password"); err == nil {
		reopened.Close(ctx)
		t.Fatal("previous state format accepted")
	} else if !strings.Contains(err.Error(), "workspace.format_unsupported") {
		t.Fatal(err)
	}
	raw, err := kopiarepo.Open(ctx, config, "warm-cache-password", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close(ctx)
	entries, err := raw.FindManifests(ctx, map[string]string{"type": kopiaStateManifestType})
	if err != nil || len(entries) != 1 {
		t.Fatalf("state entries: %v %v", entries, err)
	}
	var persisted kopiaState
	if _, err := raw.GetManifest(ctx, entries[0].ID, &persisted); err != nil {
		t.Fatal(err)
	}
	if persisted.FormatVersion != 2 || persisted.Revision != old.Revision || (persisted.Authority == nil || old.Authority == nil || !authorityEqual(*persisted.Authority, *old.Authority)) {
		t.Fatal("unsupported state was modified during failed open")
	}
}
