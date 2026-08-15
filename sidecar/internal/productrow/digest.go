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
	"github.com/vibetable/vibetable/sidecar/internal/fieldprojection"
	"github.com/vibetable/vibetable/sidecar/internal/relatedcomputation"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
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

// Project is the single provider-to-product row projection used on both sides
// of optimistic write guards. Schema V2 owns value, presence and computed
// semantics; provider-only companion fields never escape this boundary.
func Project(
	fields []v2.FieldDefinition,
	record *core.Record,
) map[string]any {
	fieldNames := make([]string, 0, len(fields))
	for _, field := range fields {
		fieldNames = append(fieldNames, field.Identity.PhysicalName)
		if field.Value.Presence.PhysicalName != "" {
			fieldNames = append(fieldNames, field.Value.Presence.PhysicalName)
		}
	}
	row := FromRecord(fieldNames, record)
	for _, field := range fields {
		physicalName := field.Identity.PhysicalName
		if field.LogicalType == v2.LogicalFormula || field.LogicalType == v2.LogicalLookup {
			row[physicalName] = relatedcomputation.ProjectStored(row[physicalName])
			continue
		}
		physical := map[string]any{physicalName: record.GetRaw(physicalName)}
		if field.Value.Presence.PhysicalName != "" {
			physical[field.Value.Presence.PhysicalName] =
				record.GetRaw(field.Value.Presence.PhysicalName)
		}
		row[physicalName] = (fieldprojection.Descriptor{
			Definition: field,
		}).ProductValue(physical)
	}
	for _, field := range fields {
		if field.Value.Presence.PhysicalName != "" {
			delete(row, field.Value.Presence.PhysicalName)
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
