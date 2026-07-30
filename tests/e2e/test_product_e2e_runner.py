from __future__ import annotations

import hashlib
import json
import sqlite3
from pathlib import Path
from typing import Any

import pytest

from tests.e2e import product_e2e_runner as runner


def test_manifest_has_exactly_twelve_unique_product_scenarios() -> None:
    scenarios = runner.load_scenarios()

    assert len(scenarios) == 12
    assert [item.id[:2] for item in scenarios] == [f"{index:02d}" for index in range(1, 13)]
    assert all(item.title and item.requirement for item in scenarios)
    by_id = {item.id: item.requirement for item in scenarios}
    assert "规范化深比较" in by_id["04-json-round-trip"]
    assert "SHA-256" in by_id["07-attachment-history"]
    assert "幂等键" in by_id["09-atomic-import-scale"]
    assert "审计快照精确一致" in by_id["12-backup-consistency"]


def test_missing_package_is_a_strict_preflight_failure(tmp_path: Path) -> None:
    audit = runner.audit_package(tmp_path / "missing")

    assert audit["passed"] is False
    assert "does not exist" in audit["errors"][0]


def test_atomic_json_write_retries_transient_windows_replace_denial(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    destination = tmp_path / "result.json"
    destination.write_text('{"old": true}\n', encoding="utf-8")
    real_replace = runner.os.replace
    attempts = 0

    def transient_replace(source: Path, target: Path) -> None:
        nonlocal attempts
        attempts += 1
        if attempts < 3:
            raise PermissionError(5, "access denied", str(target))
        real_replace(source, target)

    monkeypatch.setattr(runner.os, "replace", transient_replace)
    monkeypatch.setattr(runner.time, "sleep", lambda _seconds: None)

    runner._write_json_atomic(destination, {"requestId": "second"})

    assert attempts == 3
    assert json.loads(destination.read_text(encoding="utf-8")) == {
        "requestId": "second",
    }
    assert not destination.with_suffix(".json.tmp").exists()


def test_package_fingerprint_is_content_and_path_sensitive(tmp_path: Path) -> None:
    package = tmp_path / "package"
    package.mkdir()
    (package / "a.txt").write_text("alpha", encoding="utf-8")
    first = runner.package_fingerprint(package)
    (package / "a.txt").write_text("beta", encoding="utf-8")
    second = runner.package_fingerprint(package)
    (package / "nested").mkdir()
    (package / "nested" / "a.txt").write_text("alpha", encoding="utf-8")
    third = runner.package_fingerprint(package)

    assert first["packageSha256"] != second["packageSha256"]
    assert first["packageSha256"] != third["packageSha256"]
    assert third["fileCount"] == 2


def test_aggregate_reports_failures_without_skips(tmp_path: Path) -> None:
    scenarios = runner.load_scenarios()
    results = [
        runner._failure_result(
            scenario,
            code="CAPABILITY_MISSING",
            message="not implemented",
        )
        for scenario in scenarios
    ]
    output = tmp_path / "report.json"

    report = runner.write_aggregate(
        output,
        audit={"passed": True},
        results=results,
    )

    assert report["summary"] == {
        "total": 12,
        "passed": 0,
        "failed": 12,
        "skipped": 0,
    }
    assert report["status"] == "failed"
    assert (
        json.loads(output.read_text(encoding="utf-8"))["transport"]["browserLaunchAllowed"] is False
    )


def test_performance_summary_reports_scenarios_bridge_percentiles_and_failures() -> None:
    summary = runner.summarize_performance(
        [
            {
                "scenario": "07-attachment-history",
                "status": "passed",
                "durationMs": 9_500,
                "uiTimings": [{"name": "history.drawer.initialLoad", "durationMs": 125}],
                "bridgeDiagnostics": {
                    "pending": [],
                    "roundTrips": [
                        {
                            "requestType": "history.query",
                            "responseType": "workspace.v2.response",
                            "durationMs": 20,
                        },
                        {
                            "requestType": "history.query",
                            "responseType": "workspace.v2.response",
                            "durationMs": 80,
                        },
                    ],
                },
            },
            {
                "scenario": "10-sse-reconnect",
                "status": "passed",
                "durationMs": 8_000,
                "bridgeDiagnostics": {
                    "pending": [{"requestType": "query.page"}],
                    "roundTrips": [
                        {
                            "requestType": "query.page",
                            "responseType": "operation.failed",
                            "code": "BACKEND_UNAVAILABLE",
                            "durationMs": 3_000,
                        }
                    ],
                },
            },
        ]
    )

    assert summary["assessment"] == {
        "historyQuery": "within-budget",
        "historyDrawer": "within-budget",
        "pendingRequests": 1,
        "bridgeFailures": 1,
    }
    assert summary["scenarios"][0]["durationMs"] == 9_500
    assert summary["byUiAction"] == [
        {
            "name": "history.drawer.initialLoad",
            "count": 1,
            "p50Ms": 125.0,
            "p95Ms": 125.0,
            "maxMs": 125.0,
        }
    ]
    history = next(
        item for item in summary["byOperation"] if item["requestType"] == "history.query"
    )
    assert history == {
        "requestType": "history.query",
        "count": 2,
        "failures": 0,
        "p50Ms": 20.0,
        "p95Ms": 80.0,
        "maxMs": 80.0,
    }


def test_node_runner_only_attaches_to_existing_webview2() -> None:
    source = runner.NODE_RUNNER.read_text(encoding="utf-8")

    assert "chromium.connectOverCDP" in source
    assert "chromium.launch(" not in source
    assert "chromium.launchPersistentContext(" not in source
    assert "CAPABILITY_MISSING" not in source
    assert "pendingScenario" not in source
    for scenario in runner.load_scenarios():
        assert f'"{scenario.id}": scenario{scenario.id[:2]}' in source


def test_node_runner_enforces_closed_history_and_no_external_http() -> None:
    source = runner.NODE_RUNNER.read_text(encoding="utf-8")

    assert '"history.read"' not in source
    assert '"history.query"' in source
    assert "rawWorkspaceV2Request(" in source
    assert '"history.queryRequested"' not in source
    assert "externalRequests.length === 0" in source
    assert 'url.hostname === "app.vibetable.local"' in source
    assert '["127.0.0.1", "::1", "localhost"]' in source
    assert "process-network-observations.json" in source
    assert "unexpectedProductNonLoopback.length === 0" in source
    assert "assertCleanBridgeDiagnostics(recorder" in source
    assert "failures.length === 0 && pending.length === 0" in source
    assert "allowedBridgeFailureCodes" not in source
    assert "allowedPageErrors" not in source


def test_expected_bridge_rejection_is_acknowledged_only_after_the_scenario_asserts_it() -> None:
    source = runner.NODE_RUNNER.read_text(encoding="utf-8")
    scenario = source[
        source.index("async function scenario03") : source.index("async function scenario04")
    ]

    assert "await acknowledgeExpectedBridgeFailure(page, legacy)" in scenario
    assert "diagnostics.acknowledgedFailures" in source
    assert "diagnostics.failures.splice(index, 1)" in source


def test_invalid_field_assertion_matches_typed_v2_diagnostic_contract() -> None:
    source = runner.NODE_RUNNER.read_text(encoding="utf-8")
    scenario = source[
        source.index("async function scenario03") : source.index("async function scenario04")
    ]

    assert '"field.change.plan"' in source
    assert 'v2Response.type === "field.change.plan"' in scenario
    assert '"field.contract.invalid"' in scenario
    assert "v2Response.payload?.error?.code" in scenario
    assert '"schema.validate"' in scenario
    assert 'legacy.type === "operation.failed"' in scenario


def test_atomic_import_fault_waits_for_transactional_barrier() -> None:
    source = runner.NODE_RUNNER.read_text(encoding="utf-8")
    orchestrator = Path(runner.__file__).read_text(encoding="utf-8")

    assert '"mutation-barrier.ready.json"' in source
    assert 'ready.point === "after_record"' in source
    assert "fault.pid === barrier.pid" in source
    assert "VIBETABLE_E2E_MUTATION_BARRIER_DIR" in orchestrator
    assert '"09-atomic-import-scale"' in orchestrator
    assert '"storage-proof-request.json"' in source
    assert "storageProof.counts?.idempotency === 0" in source
    assert "key NOT LIKE 'field-v2:%'" in orchestrator
    assert "storageProof.counts?.outbox === 0" in source
    assert "handled_storage_proof_ids" in orchestrator
    assert "result.requestId !== requestId" in source


def test_product_json_scenario_uses_keyboard_and_normalized_deep_comparisons() -> None:
    source = runner.NODE_RUNNER.read_text(encoding="utf-8")
    scenario = source[
        source.index("async function scenario04") : source.index("async function scenario05")
    ]

    assert 'jsonCell.press("Enter")' in scenario
    assert 'page.keyboard.press("Escape")' in scenario
    assert 'jsonCell.press("Shift+F10")' in scenario
    assert "document.activeElement === element" in scenario
    assert "`${jsonField}\\n" in scenario
    assert 'setProductLocale(page, "en-US")' in scenario
    assert 'setProductLocale(page, "zh-CN")' in scenario
    assert "canonicalJsonSet(authoritativeValues)" in scenario
    assert "canonicalJsonSet(exportedValues)" in scenario


def test_attachment_preview_and_verified_purge_receipt_are_exact_evidence() -> None:
    source = runner.NODE_RUNNER.read_text(encoding="utf-8")
    attachment = source[
        source.index("async function scenario07") : source.index("async function scenario08")
    ]
    backup = source[source.index("async function scenario12") : source.index("const scenarios")]

    assert "waitForPreviewArtifact(" in attachment
    assert "attachment-preview-verified.txt" in attachment
    assert "sha256(await fs.readFile(preservedPreviewPath))" in attachment
    assert "preservedChangeSetIds" in backup
    assert "postSnapshotValuePreserved" in backup
    assert "postSnapshotAttachmentPreserved" in backup
    assert "beforeAuditSnapshot === afterAuditSnapshot" not in backup
    assert "snapshotStorageProof.auditLedger?.verified === true" in backup
    assert "preservedSnapshotAnchor === snapshotStorageProof.auditLedger.anchorHash" in backup
    assert 'record.sourceEpoch.startsWith("snapshot-restore:")' in backup
    assert 'record.sourceEpoch === "business-v2"' in backup


def test_backup_consistency_uses_current_snapshot_versions_ui_contract() -> None:
    source = runner.NODE_RUNNER.read_text(encoding="utf-8")
    scenario = source[source.index("async function scenario12") : source.index("const scenarios")]

    assert 'getByTestId("settings-nav-versions")' in scenario
    assert 'getByTestId("snapshot-create")' in scenario
    assert 'getByTestId("snapshot-restore-open")' in scenario
    assert 'getByTestId("snapshot-restore-preview")' in scenario
    assert "settings-nav-backup" not in scenario
    assert "backup-create" not in scenario
    assert "backup-restore-" not in scenario


def test_fault_scenarios_use_host_allocated_table_and_field_identities() -> None:
    source = runner.NODE_RUNNER.read_text(encoding="utf-8")
    scenario07 = source[
        source.index("async function scenario07") : source.index("async function scenario08")
    ]
    scenario09 = source[
        source.index("async function scenario09") : source.index("async function scenario10")
    ]
    scenario11 = source[
        source.index("async function scenario11") : source.index("async function scenario12")
    ]
    scenario12 = source[source.index("async function scenario12") : source.index("const scenarios")]

    assert '"tbl_e2e_attachments"' not in scenario07
    assert '"tbl_e2e_atomic_import"' not in scenario09
    assert '"tbl_e2e_plugin_target"' not in scenario11
    assert '"tbl_e2e_backup_consistency"' not in scenario12
    assert 'const tableId = await createEmptyTable(page, "E2E Backup Consistency")' in scenario12
    assert "createV2Field(" in scenario12
    assert "valueField.physicalName" in scenario12
    assert "formulaField.physicalName" in scenario12
    assert "attachmentField.physicalName" in scenario12
    assert "create-table-field-name-" not in scenario12


def test_storage_proof_reads_all_transactional_surfaces_read_only(
    tmp_path: Path,
) -> None:
    data_root = tmp_path / "runtime" / "local-data" / "pocketbase"
    data_root.mkdir(parents=True)
    database = data_root / "data.db"
    connection = sqlite3.connect(database)
    try:
        connection.executescript(
            """
            CREATE TABLE vibetable_tables(table_id TEXT, physical_name TEXT);
            CREATE TABLE e2e_atomic_import(id TEXT);
            CREATE TABLE vibetable_audit_events(table_id TEXT);
            CREATE TABLE vibetable_idempotency_keys(key TEXT);
            CREATE TABLE vibetable_outbox(event_id TEXT, payload_json TEXT);
            INSERT INTO vibetable_tables(table_id, physical_name)
            VALUES ('tbl_e2e_atomic_import', 'e2e_atomic_import');
            INSERT INTO vibetable_idempotency_keys(key)
            VALUES ('metadata:identifier:create:test');
            INSERT INTO vibetable_outbox(event_id, payload_json)
            VALUES ('metadata-event', '{"tableId":"metadata:identifier_mappings"}');
            """
        )
        connection.commit()
    finally:
        connection.close()
    audit_root = data_root.parent / "audit"
    audit_root.mkdir()
    ledger = sqlite3.connect(audit_root / "ledger.db")
    try:
        ledger.execute(
            """
            CREATE TABLE audit_ledger (
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
            )
            """
        )
        ledger.commit()
    finally:
        ledger.close()

    proof = runner._handle_storage_proof(
        {
            "requestId": "11111111-1111-4111-8111-111111111111",
            "tableId": "tbl_e2e_atomic_import",
        },
        tmp_path,
    )

    assert proof["status"] == "completed"
    assert proof["requestId"] == "11111111-1111-4111-8111-111111111111"
    assert proof["database"]["readOnly"] is True
    assert proof["auditLedger"] == {
        "path": str(audit_root / "ledger.db"),
        "readOnly": True,
        "verified": True,
        "count": 0,
        "anchorHash": "",
        "sourceHighWatermarks": {},
        "records": [],
    }
    assert proof["counts"] == {
        "records": 0,
        "audit": 0,
        "idempotency": 0,
        "outbox": 0,
    }


def test_audit_ledger_proof_verifies_payload_links_and_record_hashes(
    tmp_path: Path,
) -> None:
    ledger_path = tmp_path / "ledger.db"
    connection = sqlite3.connect(ledger_path)
    payload = b'{"type":"workspace.snapshotRestored"}'
    payload_hash = "sha256:" + hashlib.sha256(payload).hexdigest()
    occurred_at = "2026-07-29T00:00:00Z"
    envelope = {
        "eventId": "snapshot-restore:operation",
        "sourceEpoch": "snapshot-restore:operation",
        "sourceSequence": 1,
        "mutationIdentity": "snapshot-restore:operation",
        "payloadHash": payload_hash,
        "payload": json.loads(payload),
        "occurredAt": occurred_at,
    }
    record_hash = (
        "sha256:"
        + hashlib.sha256(
            json.dumps(
                {
                    "ledgerSequence": 1,
                    "previousHash": "",
                    "envelope": envelope,
                },
                ensure_ascii=False,
                separators=(",", ":"),
            ).encode()
        ).hexdigest()
    )
    try:
        connection.executescript(
            """
            CREATE TABLE audit_ledger (
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
            """
        )
        connection.execute(
            """
            INSERT INTO audit_ledger VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
            """,
            (
                1,
                envelope["eventId"],
                envelope["sourceEpoch"],
                1,
                envelope["mutationIdentity"],
                payload_hash,
                payload,
                occurred_at,
                "",
                record_hash,
            ),
        )
        connection.commit()
    finally:
        connection.close()

    proof = runner._audit_ledger_proof(ledger_path)

    assert proof["verified"] is True
    assert proof["count"] == 1
    assert proof["anchorHash"] == record_hash
    assert proof["sourceHighWatermarks"] == {"snapshot-restore:operation": 1}

    connection = sqlite3.connect(ledger_path)
    try:
        connection.execute("UPDATE audit_ledger SET hash = 'sha256:tampered'")
        connection.commit()
    finally:
        connection.close()
    with pytest.raises(RuntimeError, match="chain is invalid"):
        runner._audit_ledger_proof(ledger_path)


def test_process_network_report_rejects_listener_and_remote_non_loopback() -> None:
    evidence = {
        "samples": 2,
        "errors": [],
        "observations": {
            "loopback": {
                "protocol": "TCP",
                "local": "127.0.0.1:4000",
                "remote": "0.0.0.0:0",
                "state": "侦听",
                "pid": 10,
                "processName": "vibetable-pb.exe",
            },
            "listener": {
                "protocol": "TCP",
                "local": "0.0.0.0:5000",
                "remote": "0.0.0.0:0",
                "state": "LISTENING",
                "pid": 11,
                "processName": "VibeTable.Next.exe",
            },
            "remote": {
                "protocol": "TCP",
                "local": "192.0.2.5:5001",
                "remote": "203.0.113.9:443",
                "state": "ESTABLISHED",
                "pid": 12,
                "processName": "msedgewebview2.exe",
            },
        },
    }

    report = runner._process_network_report(evidence, status="completed")

    assert report["status"] == "completed"
    assert {item["reason"] for item in report["unexpectedNonLoopback"]} == {
        "non_loopback_listener",
        "non_loopback_remote",
    }
    assert [item["processName"] for item in report["unexpectedProductNonLoopback"]] == [
        "VibeTable.Next.exe"
    ]
    assert [item["processName"] for item in report["webViewRuntimeBackgroundNetwork"]] == [
        "msedgewebview2.exe"
    ]


def test_schema_scenario_uses_authoritative_capabilities_and_stable_identities() -> None:
    source = runner.NODE_RUNNER.read_text(encoding="utf-8")
    scenario = source[
        source.index("async function scenario02") : source.index("async function rawBridgeRequest")
    ]

    assert "createEmptyTable(page" in scenario
    assert "createV2Field(page" in scenario
    assert '"field.settings.describe"' in scenario
    assert '"field.change.plan"' in source
    assert '"field.change.apply"' in source
    assert 'field.fieldId?.startsWith("fld_")' in scenario
    assert 'field.physicalName?.startsWith("f_")' in scenario
    assert "updated.planned?.payload?.classes?.every" in scenario
    assert '"field.recycleBin.list"' in scenario
    assert '"retire"' in scenario
    assert '"restore"' in scenario
    assert 'legacyWrite.type === "operation.failed"' in scenario


def test_schema_scenario_waits_for_submit_completion_without_a_fixed_delay() -> None:
    source = runner.NODE_RUNNER.read_text(encoding="utf-8")
    scenario = source[
        source.index("async function scenario02") : source.index("async function rawBridgeRequest")
    ]

    assert "createEmptyTable(page" in scenario
    assert "waitForCreateTableSubmission(page, submit)" in source
    assert "waitForTimeout(1_000)" not in scenario
    assert "create table submission did not complete before timeout" in source
    assert "inputVisible" in source
    assert "submitDisabled" in source


def test_realtime_scenario_refreshes_the_active_table_without_reselection() -> None:
    source = runner.NODE_RUNNER.read_text(encoding="utf-8")
    scenario = source[
        source.index("async function scenario10") : source.index("async function scenario11")
    ]

    assert "waitForTableRecovery" not in scenario
    assert "waitForActiveTableBackend" in scenario
    assert '"mutation.apply"' in scenario
    assert '"after-reconnect"' in scenario
    assert "waitForStableGridState(page" in scenario
    assert "expectedMatchingCells: 1" in scenario
    assert "stableForMs: 1_500" in scenario
    assert "waitForTimeout(750)" not in scenario
    assert "grid did not reach a stable expected state" in source


def test_plugin_confirmation_assertion_is_not_constant_true() -> None:
    source = runner.NODE_RUNNER.read_text(encoding="utf-8")

    constant_true_assertion = (
        'recorder.check("authorized mutation plan required explicit confirmation", true'
    )
    assert constant_true_assertion not in source
    assert "/FINAL WRITE CONFIRMATION/i.test(confirmationText)" in source
    assert 'confirmationTargetCount.trim() === "1"' in source
    assert "await confirmationApprove.isEnabled()" in source


@pytest.mark.parametrize(
    ("node_result", "expected_code", "expected_message"),
    [
        (
            {"status": "passed"},
            "NODE_RUNNER_FAILED",
            "node crashed",
        ),
        (
            {
                "status": "failed",
                "error": {
                    "code": "SCENARIO_FAILED",
                    "message": "authoritative assertion failed",
                },
            },
            "SCENARIO_FAILED",
            "authoritative assertion failed",
        ),
    ],
)
def test_nonzero_node_exit_preserves_structured_scenario_failure_but_rejects_passing_json(
    monkeypatch,
    tmp_path: Path,
    node_result: dict[str, Any],
    expected_code: str,
    expected_message: str,
) -> None:
    scenario = runner.Scenario(
        id="01-offline-first-start",
        title="offline",
        requirement="fail closed",
    )
    package = tmp_path / "package"
    package.mkdir()
    (package / "host.exe").write_bytes(b"host")
    (package / "publish-layout.json").write_text(
        json.dumps({"launch": {"host": "host.exe"}}),
        encoding="utf-8",
    )

    class FakeProcess:
        pid = 42
        returncode = None

        def poll(self) -> None:
            return None

    monkeypatch.setattr(
        runner.subprocess,
        "Popen",
        lambda *_args, **_kwargs: FakeProcess(),
    )
    monkeypatch.setattr(runner, "_wait_for_cdp", lambda *_args: None)
    monkeypatch.setattr(
        runner,
        "_wait_for_readiness",
        lambda *_args: {"ready": True},
    )
    monkeypatch.setattr(runner, "_stop_process_tree", lambda *_args: None)

    def fake_node(
        _command: list[str],
        *,
        scenario_dir: Path,
        host_process: Any,
        process_network: dict[str, Any] | None = None,
    ) -> tuple[int, str, str]:
        del host_process
        del process_network
        (scenario_dir / f"{scenario.id}-result.json").write_text(
            json.dumps({"scenario": scenario.id, **node_result}),
            encoding="utf-8",
        )
        (scenario_dir / "process-network-observations.json").write_text(
            json.dumps(
                {
                    "status": "completed",
                    "samples": 1,
                    "observations": [],
                    "unexpectedNonLoopback": [],
                    "errors": [],
                }
            ),
            encoding="utf-8",
        )
        return 7, "", "node crashed"

    monkeypatch.setattr(runner, "_run_node_runner", fake_node)
    result = runner.run_scenario(
        scenario,
        package_root=package,
        evidence_root=tmp_path / "evidence",
        node="node",
    )

    assert result["status"] == "failed"
    assert result["nodeExitCode"] == 7
    assert result["error"]["code"] == expected_code
    assert expected_message in result["error"]["message"]
