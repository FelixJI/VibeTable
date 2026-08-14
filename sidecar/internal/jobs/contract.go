package jobs

// DurableSchemaVersion identifies the persisted jobs cursor/envelope shape.
// Increment only when a stored cursor can no longer be decoded compatibly.
const DurableSchemaVersion uint64 = 1
