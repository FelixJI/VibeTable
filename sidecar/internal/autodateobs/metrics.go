package autodateobs

import "sync/atomic"

const (
	RoleRequired         = "autodate_role_required"
	RoleDuplicate        = "autodate_role_duplicate"
	RoleImmutable        = "autodate_role_immutable"
	BackfillRequired     = "autodate_backfill_required"
	SchemaCreateRollback = "autodate_schema_create_rollback"
	ClientWriteRejected  = "autodate_client_write_rejected"
	ReadParseFailed      = "autodate_read_parse_failed"
)

var counters = map[string]*atomic.Uint64{
	RoleRequired:         {},
	RoleDuplicate:        {},
	RoleImmutable:        {},
	BackfillRequired:     {},
	SchemaCreateRollback: {},
	ClientWriteRejected:  {},
	ReadParseFailed:      {},
}

func Increment(name string) {
	if counter := counters[name]; counter != nil {
		counter.Add(1)
	}
}

func Snapshot() map[string]uint64 {
	result := make(map[string]uint64, len(counters))
	for name, counter := range counters {
		result[name] = counter.Load()
	}
	return result
}
