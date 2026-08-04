// Package productrow owns the provider-neutral row identity used by
// optimistic mutation guards. QueryPort and MutationKernel must call the same
// implementation so a renderer-visible digest can never drift from the
// digest checked inside the write transaction.
package productrow

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"github.com/pocketbase/pocketbase/core"
)

// DigestField is a reserved transport-only row property. Normalized physical
// field names must begin with a lowercase letter, so no user field can collide
// with it.
const DigestField = "__vibetableDigest"

// FromRecord freezes the exact product row shape guarded by MutationKernel.
// Secret fields participate in the digest without their values crossing the
// product API boundary.
func FromRecord(fieldNames []string, record *core.Record) map[string]any {
	row := make(map[string]any, len(fieldNames))
	row["id"] = record.Id
	for _, name := range fieldNames {
		field := record.Collection().Fields.GetByName(name)
		if field != nil && field.Type() == core.FieldTypePassword {
			row[name] = record.GetString(name + ":hash")
		} else {
			row[name] = record.GetRaw(name)
		}
	}
	return row
}

// Digest hashes canonical JSON. encoding/json deterministically sorts string
// map keys, matching the historical MutationKernel digest contract.
func Digest(row map[string]any) (string, error) {
	raw, err := json.Marshal(row)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}
