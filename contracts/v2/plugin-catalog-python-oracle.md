# Plugin catalog Python oracle

`plugin-catalog-python-oracle.json` freezes the Python plugin catalog authority
behavior before the Go/PocketBase ownership switch (Task #375, slice
`rpc.plugin-shared-state`). It is replayed from the fixed producer commit
`aa564213d9526d79a93182cd1db2a4dcbc08f1ef` (PR373) and is never derived from
Go or from a future implementation.

## Frozen surface

- `plugin.listCatalog` / `plugin.setEnabled` snapshots: full `PluginSnapshot`
  camelCase field set, revision increments (1 -> 2 -> 3), status transitions
  and `disabledReason` semantics.
- `plugin.listAudit` (`store.list_audit`): insertion order per plugin,
  lifecycle event sequence `install/enable/disable`, `local-user` actor.
- Four persisted kinds in `plugin_records` (`installation` itemKey `current`,
  `revision` itemKey `packageHash`, `setting` itemKey `settingKey`,
  `audit` itemKey `eventId`) with their exact stored payloads.
- Error semantics: `plugin_already_installed`, `plugin_not_found`,
  `plugin_blocked` (with `rpcErrorData.code`), and the store CAS conflict
  message `plugin revision mismatch: expected <expected>, found <found>`.
- Identities use the real product formats: `packageHash` is
  `sha256:` + 64 lowercase hex (package digest format), `projectKey` is
  `local:` + workspace UUID "N", `projectRevision` is
  `<identity>:<sessionEpoch>` (Host `PluginProjectContext`).

## Determinism

The harness in `capture_plugin_catalog_oracle.py` pins
`backend.application.plugin_registry`'s `uuid4()` sequence and
`datetime.now(UTC)` clock; `event_id`, `started_at` and `finished_at` are
therefore fixed values. No other producer behavior is replaced and no output
is edited after capture.

## Replay and verification

`generate_plugin_catalog_oracle.py` replays the capture against a
`git archive` of the fixed producer commit under the ignored
`build/qa/task375-oracle/producer/` evidence root (same path-safety scheme as
`generate_preset_python_oracle.py`). Expected only ever comes from the fixed
producer:

```
UV_NO_SYNC=1 uv run --frozen --no-sync python contracts/v2/generate_plugin_catalog_oracle.py --check
UV_NO_SYNC=1 uv run --frozen --no-sync python contracts/v2/generate_plugin_catalog_oracle.py --write  # deliberate re-freeze only
```

`tests/contract/test_plugin_catalog_oracle.py` runs the `--check` replay and
additionally proves a modified corpus is rejected. Because verification
replays the archived producer, retiring the in-tree
`PluginProjectStore`/`PluginRegistry` paths later does not invalidate this
contract.

## Limitations

- Registry/store level, not the JSON-RPC dispatcher wire frame; the dispatcher
  serializes these models with the same `by_alias` camelCase mapping.
- `uninstall`, `upgrade`, `rollback`, `inspectInstall` and package file
  lifecycle are out of scope for this slice (they stay on the Python closed
  path in #375).
- The Host-side `ProjectSnapshot` projection (source location masking) is a
  desktop behavior and is not represented here.
