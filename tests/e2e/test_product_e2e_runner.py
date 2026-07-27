from __future__ import annotations

import json
import sqlite3
from pathlib import Path
from typing import Any

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
                            "requestType": "history.queryRequested",
                            "responseType": "history.pageLoaded",
                            "durationMs": 20,
                        },
                        {
                            "requestType": "history.queryRequested",
                            "responseType": "history.pageLoaded",
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
        item for item in summary["byOperation"] if item["requestType"] == "history.queryRequested"
    )
    assert history == {
        "requestType": "history.queryRequested",
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
    assert '"history.queryRequested"' in source
    assert '["history.pageLoaded"]' in source
    assert "externalRequests.length === 0" in source
    assert 'url.hostname === "app.vibetable.local"' in source
    assert '["127.0.0.1", "::1", "localhost"]' in source
    assert "process-network-observations.json" in source
    assert "unexpectedProductNonLoopback.length === 0" in source
    assert "assertCleanBridgeDiagnostics(recorder" in source
    assert "failures.length === 0 && pending.length === 0" in source


def test_invalid_formula_assertion_matches_structured_schema_validation_contract() -> None:
    source = runner.NODE_RUNNER.read_text(encoding="utf-8")

    assert 'response.type === "schema.validate"' in source
    assert 'response.payload?.error?.code === "schema.field.invalid_formula"' in source
    assert 'response.payload?.error?.path === "fields[0].formula.source"' in source


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
    assert "storageProof.counts?.outbox === 0" in source


def test_product_json_scenario_uses_keyboard_and_normalized_deep_comparisons() -> None:
    source = runner.NODE_RUNNER.read_text(encoding="utf-8")
    scenario = source[
        source.index("async function scenario04") : source.index("async function scenario05")
    ]

    assert 'jsonCell.press("Enter")' in scenario
    assert 'page.keyboard.press("Escape")' in scenario
    assert 'jsonCell.press("Shift+F10")' in scenario
    assert "document.activeElement === element" in scenario
    assert 'setProductLocale(page, "en-US")' in scenario
    assert 'setProductLocale(page, "zh-CN")' in scenario
    assert "canonicalJsonSet(authoritativeValues)" in scenario
    assert "canonicalJsonSet(exportedValues)" in scenario


def test_attachment_preview_and_backup_audit_are_exact_evidence() -> None:
    source = runner.NODE_RUNNER.read_text(encoding="utf-8")
    attachment = source[
        source.index("async function scenario07") : source.index("async function scenario08")
    ]
    backup = source[source.index("async function scenario12") : source.index("const scenarios")]

    assert "waitForPreviewArtifact(" in attachment
    assert "attachment-preview-verified.txt" in attachment
    assert "sha256(await fs.readFile(preservedPreviewPath))" in attachment
    assert "beforeAuditSnapshot === afterAuditSnapshot" in backup
    assert "allowedBackupRestoreAuditActions = new Set([])" in backup


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

    proof = runner._handle_storage_proof(
        {"tableId": "tbl_e2e_atomic_import"},
        tmp_path,
    )

    assert proof["status"] == "completed"
    assert proof["database"]["readOnly"] is True
    assert proof["counts"] == {
        "records": 0,
        "audit": 0,
        "idempotency": 0,
        "outbox": 0,
    }


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


def test_schema_scenario_reads_back_constraints_from_authoritative_definition() -> None:
    source = runner.NODE_RUNNER.read_text(encoding="utf-8")
    scenario = source[
        source.index("async function scenario02") : source.index("async function rawBridgeRequest")
    ]

    assert '"schema.getTable"' in scenario
    assert 'findConstraint("quantity", "range")' in scenario
    assert 'findConstraint("status", "enum")' in scenario
    assert 'findConstraint("tags", "enum")' in scenario
    assert "tagsEnum?.minSelected === 1" in scenario
    assert "tagsEnum?.maxSelected === 2" in scenario
    assert '{ value: true, displayName: "Enabled" }' in scenario
    assert '{ value: 2, displayName: "Priority two" }' in scenario
    assert "statusEnum?.options?.map((item) => item.displayName)" in scenario
    assert "attachments?.attachmentPolicy?.protected === true" in scenario
    assert "attachments?.attachmentPolicy?.thumbnailVariants" in scenario
    assert 'parent?.relation?.deletePolicy === "restrict"' in scenario
    assert 'parentLabel?.lookup?.aggregate === "first"' in scenario
    assert 'definition.indexes[0]?.name === "idx_title"' in scenario
    assert 'definition.indexes[1]?.name === "idx_quantity_status"' in scenario
    assert '"e2e-typed-enum-insert"' in scenario
    assert "tags: [true, 2]" in scenario
    assert 'field: "tags", operator: "contains", value: true' in scenario
    assert "typedEnumRow.tags[0] === true" in scenario
    assert "typedEnumRow.tags[1] === 2" in scenario
    assert 'createdAt?.autoDate?.role === "createdAt"' in scenario
    assert 'updatedAt?.autoDate?.role === "updatedAt"' in scenario
    assert '"e2e-autodate-forgery"' in scenario
    assert 'forgedAutoDate.payload?.error?.code === "mutation.field.read_only"' in scenario
    assert "updatedAutoDates?.created_at === insertedAutoDates?.created_at" in scenario
    assert "successful save preserves createdAt and advances updatedAt" in scenario
    assert 'field: "updated_at"' in scenario
    assert 'operator: "gte"' in scenario
    assert "system timestamps support authoritative range filtering and sorting" in scenario


def test_schema_scenario_waits_for_submit_completion_without_a_fixed_delay() -> None:
    source = runner.NODE_RUNNER.read_text(encoding="utf-8")
    scenario = source[
        source.index("async function scenario02") : source.index("async function rawBridgeRequest")
    ]

    assert "waitForCreateTableSubmission(page, submit)" in scenario
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


def test_nonzero_node_exit_overrides_misreported_passing_json(
    monkeypatch,
    tmp_path: Path,
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
            json.dumps({"scenario": scenario.id, "status": "passed"}),
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
    assert result["error"]["code"] == "NODE_RUNNER_FAILED"
    assert "node crashed" in result["error"]["message"]
