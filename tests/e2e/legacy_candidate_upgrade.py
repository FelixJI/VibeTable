"""Run the pinned schema-5 candidate through the production startup migration path."""

from __future__ import annotations

import argparse
import json
import os
import shutil
import sqlite3
import subprocess
import time
import urllib.parse
import uuid
from contextlib import closing, suppress
from pathlib import Path

from scripts.node_toolchain import ensure_node
from tests.e2e.packaged_host_lifecycle import (
    open_workspace_with_packaged_host,
    prepare_workspace_with_legacy_packaged_host,
    run_lifecycle,
)
from tests.integration.packaged_sidecar_matrix import (
    PNG_1X1,
    WORKSPACE_ID,
    Sidecar,
    _apply,
    _apply_schema,
    _create_v2_workspace,
    _field,
    _mutation,
    _page,
    _table,
)

ROOT = Path(__file__).resolve().parents[2]
METADATA_PATH = Path(__file__).with_name("legacy_candidate_upgrade.json")
SIDECAR_NAME = "vibetable-pb.exe" if os.name == "nt" else "vibetable-pb"
CUSTOMERS_TABLE = "legacy_upgrade_customers"
ORDERS_TABLE = "legacy_upgrade_orders"
CUSTOMER_ID = "legacycustomer1"
ORDER_ID = "legacyorder0001"
AUDIT_COUNT_TABLES = (
    "vibetable_audit_events",
    "vibetable_outbox",
    "vibetable_audit_outbox",
)


def _metadata() -> dict[str, object]:
    decoded = json.loads(METADATA_PATH.read_text(encoding="utf-8"))
    assert isinstance(decoded, dict)
    return decoded


def current_candidate_root(repo_root: Path = ROOT) -> Path:
    metadata = _metadata()
    target = metadata["target"]
    assert isinstance(target, dict)
    current_default = target["defaultPackageRoot"]
    assert isinstance(current_default, str)
    current = Path(os.environ.get("VIBETABLE_CURRENT_CANDIDATE_ROOT", current_default))
    if not current.is_absolute():
        current = repo_root / current
    return current.resolve()


def ensure_legacy_candidate(repo_root: Path = ROOT) -> Path:
    override = os.environ.get("VIBETABLE_LEGACY_CANDIDATE_ROOT")
    if override:
        return Path(override).resolve()
    metadata = _metadata()
    candidate = metadata["candidate"]
    assert isinstance(candidate, dict)
    commit = candidate["commit"]
    commands = candidate["buildCommands"]
    assert isinstance(commit, str)
    assert isinstance(commands, list)
    repository_container = (
        repo_root.parent.parent if repo_root.parent.name == ".worktrees" else repo_root
    )
    source_root = repository_container / ".worktrees" / f"legacy-candidate-{commit[:8]}"
    package_root = source_root / "dist" / "VibeTable.Next"
    if not source_root.exists():
        source_root.parent.mkdir(parents=True, exist_ok=True)
        subprocess.run(
            ["git", "worktree", "add", "--detach", str(source_root), commit],
            cwd=repo_root,
            check=True,
        )
    head = subprocess.run(
        ["git", "rev-parse", "HEAD"],
        cwd=source_root,
        check=True,
        capture_output=True,
        text=True,
        encoding="utf-8",
    ).stdout.strip()
    assert head == commit, f"legacy build worktree is {head}, expected {commit}"
    if package_root.is_dir():
        return package_root
    node = ensure_node(repo_root)
    npm = node.with_name("npm.cmd")
    assert npm.is_file(), f"locked npm.cmd is missing next to {node}"
    build_environment = {
        **os.environ,
        "PATH": os.pathsep.join((str(node.parent), os.environ.get("PATH", ""))),
    }
    for raw_command in commands:
        assert isinstance(raw_command, list)
        command = [part for part in raw_command if isinstance(part, str)]
        assert len(command) == len(raw_command)
        if os.name == "nt" and command[0] == "npm":
            command[0] = str(npm)
        subprocess.run(command, cwd=source_root, env=build_environment, check=True)
    assert package_root.is_dir(), f"legacy build did not produce {package_root}"
    return package_root


def release_smoke_command(
    *,
    legacy_package_root: Path,
    current_package_root: Path,
    evidence_root: Path,
) -> list[str]:
    return [
        "uv",
        "run",
        "python",
        "-m",
        "tests.e2e.legacy_candidate_upgrade",
        "--legacy-package-root",
        str(legacy_package_root),
        "--current-package-root",
        str(current_package_root),
        "--evidence-root",
        str(evidence_root),
        "--json-report",
        str(evidence_root / "report.json"),
    ]


def _reserve_workspace_environment(workspace_root: Path) -> dict[str, str]:
    authority_path = (
        workspace_root / ".vibetable" / "coordination" / "desktop-runtime-authority.json"
    )
    authority = json.loads(authority_path.read_text(encoding="utf-8"))
    assert authority.get("workspaceId") == WORKSPACE_ID, authority
    session_epoch = authority.get("lastSessionEpoch")
    fence_epoch = authority.get("fenceEpoch")
    claim_id = authority.get("claimId")
    assert isinstance(session_epoch, int), authority
    assert isinstance(fence_epoch, int), authority
    assert isinstance(claim_id, str), authority
    session_epoch += 1
    authority["lastSessionEpoch"] = session_epoch
    temporary = authority_path.with_suffix(".json.tmp")
    temporary.write_text(
        json.dumps(authority, ensure_ascii=False, separators=(",", ":")),
        encoding="utf-8",
    )
    os.replace(temporary, authority_path)
    return {
        "VIBETABLE_WORKSPACE_ID": WORKSPACE_ID,
        "VIBETABLE_WORKSPACE_SESSION_EPOCH": str(session_epoch),
        "VIBETABLE_WORKSPACE_FENCE_EPOCH": str(fence_epoch),
        "VIBETABLE_WORKSPACE_CLAIM_ID": claim_id,
    }


def _workspace_request(
    sidecar: Sidecar,
    sequence: int,
    method: str,
    params: dict[str, object],
) -> dict[str, object]:
    identity = sidecar.workspace_identity
    assert isinstance(identity, dict), "workspace sidecar identity is missing"
    operation_id = str(uuid.uuid4())
    return {
        "jsonrpc": "2.0",
        "id": f"legacy-upgrade-v2-{sequence}",
        "method": method,
        "wire": {
            "scope": "workspace",
            "workspaceId": identity["VIBETABLE_WORKSPACE_ID"],
            "sessionEpoch": int(identity["VIBETABLE_WORKSPACE_SESSION_EPOCH"]),
            "operationId": operation_id,
            "sequence": sequence,
        },
        "params": params,
    }


def _validate_package(package_root: Path, *, legacy: bool) -> Path:
    metadata = _metadata()
    section = metadata["candidate" if legacy else "target"]
    assert isinstance(section, dict)
    resources_root = package_root
    if not (resources_root / "publish-layout.json").is_file():
        resources_root = package_root / "resources"
    layout_path = resources_root / "publish-layout.json"
    binary = resources_root / "sidecar" / SIDECAR_NAME
    manifest_path = resources_root / "sidecar" / "migrations" / "manifest.json"
    assert layout_path.is_file(), f"candidate publish layout missing: {layout_path}"
    assert binary.is_file(), f"candidate sidecar missing: {binary}"
    assert manifest_path.is_file(), f"candidate migration manifest missing: {manifest_path}"
    layout = json.loads(layout_path.read_text(encoding="utf-8"))
    manifest = json.loads(manifest_path.read_text(encoding="utf-8"))
    sidecar = layout["components"]["sidecar"]
    assert int(sidecar["schemaVersion"]) == manifest["schemaVersion"]
    if legacy:
        assert layout["components"]["host"]["version"] == section["version"]
        assert manifest["schemaVersion"] == section["schemaVersion"]
        assert [item["source"] for item in manifest["migrations"]] == section["migrationFiles"]
    else:
        minimum = section["minimumSchemaVersion"]
        assert isinstance(minimum, int)
        assert manifest["schemaVersion"] >= minimum
    return binary


def _database_state(data_dir: Path) -> dict[str, object]:
    with closing(sqlite3.connect(data_dir / "data.db")) as connection:
        migrations = [
            row[0]
            for row in connection.execute(
                "SELECT file FROM _migrations WHERE file LIKE '2026%' ORDER BY file"
            )
        ]
        collections = {
            row[0]: row[1]
            for row in connection.execute(
                "SELECT name, fields FROM _collections WHERE name IN "
                "('vibetable_fields', 'vibetable_relations', 'vibetable_tables')"
            )
        }
        counts = {
            table: connection.execute(f'SELECT COUNT(*) FROM "{table}"').fetchone()[0]
            for table in AUDIT_COUNT_TABLES
        }
    return {
        "migrations": migrations,
        "hasFieldSettingsV2": "schema_model_version" in collections.get("vibetable_fields", ""),
        "hasRelationPairs": "pair_id" in collections.get("vibetable_relations", ""),
        "hasPrimaryDisplayField": "primary_display_field_id"
        in collections.get("vibetable_tables", ""),
        "counts": counts,
    }


def _report_counts(
    report: dict[str, object],
    section_name: str,
    *,
    database_wrapper: bool,
) -> dict[str, int]:
    section = report[section_name]
    assert isinstance(section, dict), f"{section_name} must be an object"
    database = section.get("database") if database_wrapper else section
    assert isinstance(database, dict), f"{section_name}.database must be an object"
    raw_counts = database.get("counts")
    assert isinstance(raw_counts, dict), f"{section_name} counts must be an object"
    counts: dict[str, int] = {}
    for table in AUDIT_COUNT_TABLES:
        value = raw_counts.get(table)
        assert isinstance(value, int), f"{section_name}.{table} count must be an integer"
        counts[table] = value
    return counts


def validate_upgrade_report(report: dict[str, object]) -> None:
    assert report.get("ok") is True
    assert report.get("evidenceKind") == "packaged-host-upgrade"
    legacy_host = report.get("legacyPackagedHostSeed")
    assert isinstance(legacy_host, dict), "legacy packaged host seed evidence is missing"
    assert legacy_host.get("status") == "passed"
    assert legacy_host.get("evidenceKind") == "legacy-packaged-host-workspace-open"
    assert legacy_host.get("hostExecutable") == "VibeTable.Next.exe"
    assert legacy_host.get("workspaceId") == WORKSPACE_ID
    assert legacy_host.get("sessionState") == "openedWritable"
    assert legacy_host.get("lifecycle", {}).get("status") == "passed"
    packaged_host = report.get("packagedHostOpen")
    assert isinstance(packaged_host, dict), "packaged host evidence is missing"
    assert packaged_host.get("status") == "passed", "packaged host open did not pass"
    assert str(packaged_host.get("hostExecutable", "")).casefold() == "vibetable.next.exe", (
        "packaged host evidence did not run VibeTable.Next.exe"
    )
    assert packaged_host.get("workspaceId") == WORKSPACE_ID
    assert packaged_host.get("sessionState") == "openedWritable"
    clean_epoch = packaged_host.get("sessionEpoch")
    assert isinstance(clean_epoch, int)
    assert clean_epoch > 0
    lifecycle = packaged_host.get("lifecycle")
    assert isinstance(lifecycle, dict), "packaged host lifecycle evidence is missing"
    assert lifecycle.get("status") == "passed", "packaged host lifecycle evidence did not pass"
    interrupted_host = report.get("packagedHostInterrupted")
    assert isinstance(interrupted_host, dict), "packaged host interruption evidence is missing"
    assert interrupted_host.get("status") == "passed"
    assert interrupted_host.get("openOutcome") == "rejected-before-open"
    assert interrupted_host.get("writableSessionPublished") is False
    assert interrupted_host.get("sessionEpoch") is None
    assert interrupted_host.get("startupFaultConsumed") is True
    assert interrupted_host.get("lifecycle", {}).get("status") == "passed"
    recovered_host = report.get("packagedHostRecovered")
    assert isinstance(recovered_host, dict), "packaged host recovery evidence is missing"
    assert recovered_host.get("status") == "passed"
    assert recovered_host.get("openOutcome") == "opened-writable"
    assert recovered_host.get("workspaceId") == WORKSPACE_ID
    assert recovered_host.get("sessionState") == "openedWritable"
    recovered_epoch = recovered_host.get("sessionEpoch")
    assert isinstance(recovered_epoch, int)
    assert recovered_epoch > clean_epoch
    assert recovered_host.get("lifecycle", {}).get("status") == "passed"
    host_lifecycle = report.get("packagedHostLifecycle")
    assert isinstance(host_lifecycle, dict), "packaged host lifecycle evidence is missing"
    assert host_lifecycle.get("ok") is True
    assert host_lifecycle.get("evidenceKind") == "packaged-host-lifecycle"
    tray = host_lifecycle.get("closeToTrayAndTrayExit")
    assert isinstance(tray, dict)
    assert tray.get("status") == "passed"
    assert tray.get("closeToTray", {}).get("windowVisible") is False
    assert tray.get("closeToTray", {}).get("trayVisible") is True
    assert tray.get("lifecycle", {}).get("status") == "passed"
    silent = host_lifecycle.get("silentStartup")
    assert isinstance(silent, dict)
    assert silent.get("status") == "passed"
    assert silent.get("startup", {}).get("windowVisible") is False
    assert silent.get("startup", {}).get("trayVisible") is True
    assert silent.get("lifecycle", {}).get("status") == "passed"
    baseline = _report_counts(report, "seeded", database_wrapper=True)
    for table, count in baseline.items():
        assert count > 0, f"seeded.{table} must be non-empty"
    for section_name in ("cleanUpgrade", "recovered", "snapshotRestore"):
        current = _report_counts(report, section_name, database_wrapper=True)
        for table, baseline_count in baseline.items():
            assert current[table] >= baseline_count, (
                f"{section_name}.{table} dropped below seeded baseline: "
                f"{current[table]} < {baseline_count}"
            )
    rolled_back = _report_counts(report, "rolledBack", database_wrapper=False)
    assert rolled_back == baseline, (
        f"rolledBack counts changed: expected {baseline}, got {rolled_back}"
    )


def _wait_for_transient_cleanup(workspace_root: Path) -> None:
    deadline = time.monotonic() + 5
    while True:
        transient = list((workspace_root / ".vibetable").rglob("*.tmp"))
        if not transient:
            return
        assert time.monotonic() < deadline, transient
        time.sleep(0.05)


def copy_workspace_for_upgrade(source: Path, destination: Path) -> None:
    shutil.copytree(source, destination, ignore=shutil.ignore_patterns("*.tmp"))


def _snapshot(sidecar: Sidecar) -> str:
    captured = sidecar.request(
        "POST",
        "/api/vibetable/v2/rpc",
        json_body=_workspace_request(
            sidecar,
            1,
            "snapshot.request",
            {"trigger": "manual", "urgency": "foreground"},
        ),
    ).json()
    assert "error" not in captured, captured
    assert captured["result"]["state"] == "ready"
    return captured["result"]["snapshotId"]


def _seed_legacy_corpus(binary: Path, workspace_root: Path) -> dict[str, object]:
    data_dir = workspace_root / ".vibetable" / "data"
    assert data_dir.is_dir(), "legacy workspace must be created before corpus seeding"
    sidecar = Sidecar(
        binary,
        data_dir,
        workspace_identity=_reserve_workspace_environment(workspace_root),
    )
    sidecar.start()
    try:
        customers = _apply_schema(
            sidecar,
            _table(CUSTOMERS_TABLE, [_field("name_id", "name")]),
            0,
            "legacy-upgrade-create-customers",
        )
        orders = _apply_schema(
            sidecar,
            _table(
                ORDERS_TABLE,
                [
                    _field("title_id", "title"),
                    _field(
                        "quantity_id",
                        "quantity",
                        data_type="integer",
                        storage_type="number",
                    ),
                ],
            ),
            0,
            "legacy-upgrade-create-orders",
        )
        _apply(
            sidecar,
            CUSTOMERS_TABLE,
            customers["schemaRevision"],
            "legacy-upgrade-customer",
            [{"kind": "insert", "recordId": CUSTOMER_ID, "values": {"name": "Ada"}}],
        )
        _apply(
            sidecar,
            ORDERS_TABLE,
            orders["schemaRevision"],
            "legacy-upgrade-order",
            [
                {
                    "kind": "insert",
                    "recordId": ORDER_ID,
                    "values": {"title": "Legacy order", "quantity": 7},
                }
            ],
        )
        definition = orders
        definition["fields"].extend(
            [
                _field(
                    "double_id",
                    "double_quantity",
                    kind="formula",
                    data_type="formula",
                    storage_type="number",
                    nullable=False,
                    read_only=True,
                    formula={
                        "language": "cel-v1",
                        "source": "quantity * 2",
                        "resultType": "integer",
                        "version": 1,
                        "status": "ready",
                    },
                ),
                _field(
                    "customer_id",
                    "customer",
                    kind="relation",
                    data_type="relation",
                    storage_type="relation",
                    relation={
                        "targetTableId": CUSTOMERS_TABLE,
                        "cardinality": "one",
                        "deletePolicy": "setNull",
                        "junctionTableId": None,
                    },
                ),
                _field(
                    "customer_name_id",
                    "customer_name",
                    kind="lookup",
                    data_type="lookup",
                    storage_type="text",
                    read_only=True,
                    lookup={
                        "relationFieldId": "customer_id",
                        "targetFieldId": "name_id",
                        "aggregate": "none",
                    },
                ),
                _field(
                    "images_id",
                    "images",
                    kind="attachment",
                    data_type="file",
                    storage_type="file",
                    attachment={
                        "maxFiles": 1,
                        "maxBytesPerFile": 1048576,
                        "allowedMimeTypes": ["image/png"],
                        "thumbnailVariants": ["64x64"],
                        "protected": True,
                    },
                ),
            ]
        )
        orders = _apply_schema(
            sidecar,
            definition,
            1,
            "legacy-upgrade-extend-orders",
        )
        _apply(
            sidecar,
            ORDERS_TABLE,
            orders["schemaRevision"],
            "legacy-upgrade-link-customer",
            [
                {
                    "kind": "update",
                    "recordId": ORDER_ID,
                    "values": {"customer": CUSTOMER_ID},
                }
            ],
        )
        upload = _mutation(
            ORDERS_TABLE,
            orders["schemaRevision"],
            "legacy-upgrade-attachment",
            [
                {
                    "kind": "setAttachments",
                    "recordId": ORDER_ID,
                    "fieldId": "images_id",
                    "uploadHandles": ["legacy-pixel"],
                    "removeStoredNames": [],
                }
            ],
        )
        sidecar.multipart_mutation(upload, "legacy-pixel", "legacy-pixel.png", PNG_1X1)
        deadline = time.monotonic() + 15
        while True:
            row = _page(sidecar, ORDERS_TABLE)["rows"][0]
            if row.get("double_quantity") == 14:
                break
            assert time.monotonic() < deadline, row
            time.sleep(0.1)
        snapshot_id = _snapshot(sidecar)
        before = _assert_corpus(sidecar, snapshot_id, sequence=2)
    finally:
        sidecar.stop()
    database = _database_state(data_dir)
    counts = database["counts"]
    assert isinstance(counts, dict)
    assert counts["vibetable_audit_events"] > 0
    assert counts["vibetable_outbox"] > 0
    assert counts["vibetable_audit_outbox"] > 0
    assert database["hasFieldSettingsV2"] is False
    assert database["hasRelationPairs"] is False
    _wait_for_transient_cleanup(workspace_root)
    return {"business": before, "database": database, "snapshotId": snapshot_id}


def _assert_corpus(
    sidecar: Sidecar,
    snapshot_id: str,
    *,
    sequence: int,
) -> dict[str, object]:
    deadline = time.monotonic() + 15
    while True:
        page = _page(sidecar, ORDERS_TABLE)
        if page["rows"][0].get("double_quantity") == 14:
            break
        assert time.monotonic() < deadline, page
        time.sleep(0.1)
    assert page["totalRows"] == 1
    row = page["rows"][0]
    assert row["title"] == "Legacy order"
    assert row["quantity"] == 7
    assert row["double_quantity"] == 14
    assert row["customer"] == CUSTOMER_ID
    assert row["customer_name"] == "Ada"
    relations = sidecar.request(
        "GET",
        f"/api/vibetable/v1/relations/describe?tableId={ORDERS_TABLE}",
    ).json()["relations"]
    assert relations[0]["targetTableId"] == CUSTOMERS_TABLE
    query = urllib.parse.urlencode(
        {"tableId": ORDERS_TABLE, "recordId": ORDER_ID, "fieldId": "images_id"}
    )
    attachments = sidecar.request("GET", f"/api/vibetable/v1/attachments/refs?{query}").json()[
        "attachments"
    ]
    assert len(attachments) == 1
    assert attachments[0]["originalName"] == "legacy-pixel.png"
    downloaded = sidecar.request(
        "GET",
        "/api/vibetable/v1/attachments/download?"
        + urllib.parse.urlencode({"capability": attachments[0]["downloadCapability"]}),
    )
    assert downloaded.body == PNG_1X1
    history = sidecar.request(
        "GET",
        "/api/vibetable/v1/history/change-sets?"
        + urllib.parse.urlencode({"collection": ORDERS_TABLE, "itemId": ORDER_ID, "scope": "row"}),
    ).json()
    assert history["changeSets"]
    snapshots = sidecar.request(
        "POST",
        "/api/vibetable/v2/rpc",
        json_body=_workspace_request(
            sidecar,
            sequence,
            "snapshot.list",
            {"cursor": None, "limit": 50},
        ),
    ).json()
    assert "error" not in snapshots, snapshots
    assert snapshot_id in {item["snapshotId"] for item in snapshots["result"]["snapshots"]}
    return {
        "normal": "passed",
        "formula": "passed",
        "relationLookup": "passed",
        "attachment": "passed",
        "auditHistory": "passed",
        "snapshot": "passed",
    }


def _upgrade_and_verify(binary: Path, workspace_root: Path, snapshot_id: str) -> dict[str, object]:
    data_dir = workspace_root / ".vibetable" / "data"
    sidecar = Sidecar(
        binary,
        data_dir,
        workspace_identity=_reserve_workspace_environment(workspace_root),
    )
    sidecar.start()
    try:
        business = _assert_corpus(sidecar, snapshot_id, sequence=3)
    finally:
        sidecar.stop()
    database = _database_state(data_dir)
    migrations = database["migrations"]
    assert isinstance(migrations, list)
    assert "2026072801_field_settings_v2_metadata.go" in migrations
    assert "2026080501_relation_pairs.go" in migrations
    assert database["hasFieldSettingsV2"] is True
    assert database["hasRelationPairs"] is True
    assert database["hasPrimaryDisplayField"] is True
    counts = database["counts"]
    assert isinstance(counts, dict)
    assert counts["vibetable_audit_events"] > 0
    assert counts["vibetable_outbox"] > 0
    assert counts["vibetable_audit_outbox"] > 0
    return {"business": business, "database": database}


def _verify_with_old_binary(
    binary: Path,
    workspace_root: Path,
    snapshot_id: str,
) -> dict[str, object]:
    data_dir = workspace_root / ".vibetable" / "data"
    sidecar = Sidecar(
        binary,
        data_dir,
        workspace_identity=_reserve_workspace_environment(workspace_root),
    )
    sidecar.start()
    try:
        business = _assert_corpus(sidecar, snapshot_id, sequence=3)
    finally:
        sidecar.stop()
    database = _database_state(data_dir)
    migrations = database["migrations"]
    assert isinstance(migrations, list)
    assert "2026072801_field_settings_v2_metadata.go" not in migrations
    assert "2026080501_relation_pairs.go" not in migrations
    return {"business": business, "database": database}


def _restore_snapshot_and_verify(
    binary: Path,
    workspace_root: Path,
    snapshot_id: str,
) -> dict[str, object]:
    data_dir = workspace_root / ".vibetable" / "data"
    sidecar = Sidecar(
        binary,
        data_dir,
        workspace_identity=_reserve_workspace_environment(workspace_root),
    )
    sidecar.start()
    try:
        definition = sidecar.request(
            "GET",
            f"/api/vibetable/v1/schema/tables/{ORDERS_TABLE}",
        ).json()
        _apply(
            sidecar,
            ORDERS_TABLE,
            definition["schemaRevision"],
            "after-snapshot-change",
            [
                {
                    "kind": "update",
                    "recordId": ORDER_ID,
                    "values": {"title": "Changed after snapshot"},
                }
            ],
        )
        assert _page(sidecar, ORDERS_TABLE)["rows"][0]["title"] == "Changed after snapshot"
        preview = sidecar.request(
            "POST",
            "/api/vibetable/v2/rpc",
            json_body=_workspace_request(
                sidecar,
                10,
                "snapshot.previewRestore",
                {"snapshotId": snapshot_id, "targetMode": "currentWorkspace"},
            ),
        ).json()
        assert "error" not in preview, preview
        assert preview["result"]["protectionRequired"] is True
        applied = sidecar.request(
            "POST",
            "/api/vibetable/v2/rpc",
            json_body=_workspace_request(
                sidecar,
                11,
                "snapshot.applyRestore",
                {"planId": preview["result"]["planId"], "confirmed": True},
            ),
        ).json()
        assert "error" not in applied, applied
        assert applied["result"]["state"] == "prepared"
    finally:
        sidecar.stop()

    reopened = Sidecar(
        binary,
        data_dir,
        workspace_identity=_reserve_workspace_environment(workspace_root),
    )
    try:
        reopened.start()
    except AssertionError as exc:
        process = reopened.process
        stderr = ""
        if process is not None:
            with suppress(subprocess.TimeoutExpired):
                process.wait(timeout=5)
            if process.stderr is not None and process.poll() is not None:
                stderr = process.stderr.read()
        reopened.stop()
        raise AssertionError(f"snapshot restore reopen failed: {exc}; stderr={stderr}") from exc
    try:
        business = _assert_corpus(reopened, snapshot_id, sequence=12)
    finally:
        reopened.stop()
    database = _database_state(data_dir)
    counts = database["counts"]
    assert isinstance(counts, dict)
    assert counts["vibetable_audit_events"] > 0
    assert counts["vibetable_outbox"] > 0
    assert counts["vibetable_audit_outbox"] > 0
    return {
        "previewChanges": preview["result"]["changes"],
        "applyState": "prepared",
        "reopened": business,
        "database": database,
    }


def run_upgrade(
    *,
    legacy_package_root: Path,
    current_package_root: Path,
    evidence_root: Path,
) -> dict[str, object]:
    legacy_binary = _validate_package(legacy_package_root, legacy=True)
    current_binary = _validate_package(current_package_root, legacy=False)
    assert not evidence_root.exists(), f"evidence root must be fresh: {evidence_root}"
    evidence_root.mkdir(parents=True)
    legacy_workspace = evidence_root / "legacy-workspace"
    _create_v2_workspace(legacy_workspace)
    legacy_host_seed = prepare_workspace_with_legacy_packaged_host(
        legacy_package_root,
        legacy_workspace,
        evidence_root / "legacy-packaged-host-seed",
    )
    seeded = _seed_legacy_corpus(legacy_binary, legacy_workspace)
    snapshot_id = seeded["snapshotId"]
    assert isinstance(snapshot_id, str)

    pre_upgrade_copy = evidence_root / "pre-upgrade-copy"
    clean_upgrade = evidence_root / "clean-upgrade"
    interrupted_upgrade = evidence_root / "interrupted-upgrade"
    snapshot_restore = evidence_root / "snapshot-restore"
    copy_workspace_for_upgrade(legacy_workspace, pre_upgrade_copy)
    copy_workspace_for_upgrade(pre_upgrade_copy, clean_upgrade)
    copy_workspace_for_upgrade(pre_upgrade_copy, interrupted_upgrade)
    copy_workspace_for_upgrade(pre_upgrade_copy, snapshot_restore)

    old_binary_reopen = _verify_with_old_binary(
        legacy_binary,
        pre_upgrade_copy,
        snapshot_id,
    )
    packaged_host_open = open_workspace_with_packaged_host(
        current_package_root,
        clean_upgrade,
        evidence_root / "packaged-host-open",
    )
    clean = _upgrade_and_verify(current_binary, clean_upgrade, snapshot_id)
    fault_file = evidence_root / "startup-migration-fault.json"
    fault_file.write_text(
        json.dumps(
            {
                "migration": "2026080501_relation_pairs",
                "checkpoint": "after-relations",
            },
            separators=(",", ":"),
        ),
        encoding="utf-8",
    )
    interrupted = open_workspace_with_packaged_host(
        current_package_root,
        interrupted_upgrade,
        evidence_root / "packaged-host-interrupted",
        startup_fault_file=fault_file,
        expect_open_failure=True,
    )
    rolled_back = _database_state(interrupted_upgrade / ".vibetable" / "data")
    migrations = rolled_back["migrations"]
    assert isinstance(migrations, list)
    assert "2026072801_field_settings_v2_metadata.go" not in migrations
    assert "2026080501_relation_pairs.go" not in migrations
    assert rolled_back["hasFieldSettingsV2"] is False
    assert rolled_back["hasRelationPairs"] is False
    packaged_host_recovered = open_workspace_with_packaged_host(
        current_package_root,
        interrupted_upgrade,
        evidence_root / "packaged-host-recovered",
    )
    recovered = _upgrade_and_verify(current_binary, interrupted_upgrade, snapshot_id)
    _upgrade_and_verify(current_binary, snapshot_restore, snapshot_id)
    restored = _restore_snapshot_and_verify(current_binary, snapshot_restore, snapshot_id)
    packaged_host_lifecycle = run_lifecycle(
        current_package_root,
        evidence_root / "packaged-host-lifecycle",
    )

    metadata = _metadata()
    report: dict[str, object] = {
        "ok": True,
        "evidenceKind": "packaged-host-upgrade",
        "candidate": metadata["candidate"],
        "target": metadata["target"],
        "legacyPackageRoot": str(legacy_package_root),
        "currentPackageRoot": str(current_package_root),
        "seeded": seeded,
        "legacyPackagedHostSeed": legacy_host_seed,
        "oldBinaryReopen": old_binary_reopen,
        "cleanUpgrade": clean,
        "packagedHostOpen": packaged_host_open,
        "packagedHostInterrupted": interrupted,
        "packagedHostRecovered": packaged_host_recovered,
        "packagedHostLifecycle": packaged_host_lifecycle,
        "rolledBack": rolled_back,
        "recovered": recovered,
        "snapshotRestore": restored,
        "rollbackBoundary": metadata["rollbackBoundary"],
        "preUpgradeCopy": str(pre_upgrade_copy),
    }
    validate_upgrade_report(report)
    return report


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--legacy-package-root", type=Path)
    parser.add_argument("--current-package-root", type=Path, default=current_candidate_root())
    parser.add_argument("--evidence-root", type=Path, required=True)
    parser.add_argument("--json-report", type=Path, required=True)
    args = parser.parse_args(argv)
    legacy_package_root = (
        args.legacy_package_root.resolve()
        if args.legacy_package_root is not None
        else ensure_legacy_candidate()
    )
    report = run_upgrade(
        legacy_package_root=legacy_package_root,
        current_package_root=args.current_package_root.resolve(),
        evidence_root=args.evidence_root.resolve(),
    )
    args.json_report.parent.mkdir(parents=True, exist_ok=True)
    args.json_report.write_text(
        json.dumps(report, ensure_ascii=False, indent=2) + "\n",
        encoding="utf-8",
    )
    print(json.dumps(report, ensure_ascii=False, indent=2))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
