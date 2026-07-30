"""Black-box §12.4 matrix for the sidecar from the published product layout.

The strict entry point builds the complete release before touching the sidecar.
For local iteration ``--sidecar-only-build`` keeps the exact same
the versioned ``dist/.../resources/sidecar`` layout while skipping unrelated
build stages.
Neither mode imports or embeds PocketBase.
"""

from __future__ import annotations

import argparse
import base64
import contextlib
import hashlib
import json
import os
import queue
import subprocess
import sys
import threading
import time
import urllib.error
import urllib.parse
import urllib.request
import uuid
import zipfile
from dataclasses import dataclass
from pathlib import Path, PurePosixPath
from typing import Any

REPO_ROOT = Path(__file__).resolve().parents[2]
if str(REPO_ROOT) not in sys.path:
    sys.path.insert(0, str(REPO_ROOT))

from scripts.build_next import RepoPaths  # noqa: E402

PUBLISH_ROOT = RepoPaths.default(REPO_ROOT).publish_root
SIDECAR_NAME = "vibetable-pb.exe" if os.name == "nt" else "vibetable-pb"
SESSION_HEADER = "X-VibeTable-Session"
WORKSPACE_ID = "11111111-1111-4111-8111-111111111111"
CLAIM_ID = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
SESSION_EPOCH = 7
FENCE_EPOCH = 3
PNG_1X1 = base64.b64decode(
    "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII="
)


def _field(
    field_id: str,
    name: str,
    *,
    kind: str = "scalar",
    data_type: str = "shortText",
    storage_type: str = "text",
    nullable: bool = True,
    read_only: bool = False,
    formula: dict[str, Any] | None = None,
    relation: dict[str, Any] | None = None,
    lookup: dict[str, Any] | None = None,
    attachment: dict[str, Any] | None = None,
) -> dict[str, Any]:
    editor = "text"
    if data_type == "integer":
        editor = "number"
    elif data_type == "file":
        editor = "file"
    constraints: list[dict[str, Any]] = []
    if relation:
        constraints.append(
            {
                "kind": "relation",
                "targetTableId": relation["targetTableId"],
                "cardinality": relation["cardinality"],
                "deletePolicy": relation["deletePolicy"],
            }
        )
    if attachment:
        constraints.append({"kind": "attachment", "policy": attachment})
    return {
        "fieldId": field_id,
        "physicalName": name,
        "displayName": name,
        "kind": kind,
        "dataType": data_type,
        "storageType": storage_type,
        "nullable": nullable,
        "defaultValue": None,
        "constraints": constraints,
        "editor": {"kind": editor, "config": {}},
        "readOnly": read_only,
        "formula": formula,
        "relation": relation,
        "lookup": lookup,
        "attachmentPolicy": attachment,
    }


def _table(table_id: str, fields: list[dict[str, Any]]) -> dict[str, Any]:
    return {
        "contractVersion": "1.0",
        "tableId": table_id,
        "physicalName": table_id,
        "displayName": table_id,
        "kind": "base",
        "schemaRevision": "schema_0000",
        "archivePolicy": {
            "mode": "none",
            "fieldId": None,
            "archivedValue": None,
        },
        "fields": fields,
        "indexes": [],
    }


def _mutation(
    table_id: str,
    schema_revision: str,
    key: str,
    operations: list[dict[str, Any]],
) -> dict[str, Any]:
    return {
        "contractVersion": "1.0",
        "requestId": f"request-{key}",
        "idempotencyKey": f"idem-{key}",
        "tableId": table_id,
        "schemaRevision": schema_revision,
        "operations": operations,
        "actor": {"type": "user", "id": "release-matrix", "displayName": None},
        "expectedRevision": None,
        "expectedDigest": None,
    }


@dataclass
class Response:
    status: int
    body: bytes
    headers: Any

    def json(self) -> dict[str, Any]:
        decoded = json.loads(self.body)
        assert isinstance(decoded, dict)
        return decoded


class Sidecar:
    def __init__(
        self,
        binary: Path,
        data_dir: Path,
        *,
        workspace_identity: dict[str, str] | None = None,
    ) -> None:
        self.binary = binary
        self.data_dir = data_dir
        self.workspace_identity = workspace_identity
        self.secret = uuid.uuid4().hex + uuid.uuid4().hex
        self.process: subprocess.Popen[str] | None = None
        self.address = ""

    def start(self) -> None:
        assert self.process is None
        environment = os.environ.copy()
        environment["VIBETABLE_SIDECAR_SESSION_SECRET"] = self.secret
        environment["VIBETABLE_SIDECAR_DATA_DIR"] = str(self.data_dir)
        if self.workspace_identity is not None:
            environment.update(self.workspace_identity)
        self.process = subprocess.Popen(
            [str(self.binary)],
            cwd=self.binary.parent,
            env=environment,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            text=True,
            encoding="utf-8",
            errors="replace",
        )
        process = self.process
        assert process.stdout is not None
        stdout = process.stdout
        lines: queue.Queue[str] = queue.Queue(maxsize=1)
        threading.Thread(
            target=lambda: lines.put(stdout.readline()),
            daemon=True,
        ).start()
        try:
            line = lines.get(timeout=30)
        except queue.Empty as exc:
            raise AssertionError(self._diagnostics("readiness timed out")) from exc
        try:
            ready = json.loads(line)
        except json.JSONDecodeError as exc:
            raise AssertionError(self._diagnostics(f"invalid readiness line: {line!r}")) from exc
        assert ready["contract"] == "vibetable.sidecar.ready.v1"
        assert ready["event"] == "sidecar.ready"
        self.address = ready["address"]
        health = self.request("GET", "/api/vibetable/v1/health").json()
        assert health["status"] == "ok"

    def _diagnostics(self, message: str) -> str:
        process = self.process
        code = None if process is None else process.poll()
        stderr = ""
        if process is not None and process.stderr is not None and code is not None:
            stderr = process.stderr.read()
        return f"{message}; exit={code}; stderr={stderr[-4000:]}"

    def request(
        self,
        method: str,
        path: str,
        *,
        json_body: dict[str, Any] | None = None,
        body: bytes | None = None,
        content_type: str | None = None,
        request_headers: dict[str, str] | None = None,
        expected: int = 200,
        timeout: float = 20,
    ) -> Response:
        headers = {SESSION_HEADER: self.secret}
        headers.update(request_headers or {})
        if json_body is not None:
            body = json.dumps(json_body, separators=(",", ":")).encode()
            content_type = "application/json"
        if content_type:
            headers["Content-Type"] = content_type
        request = urllib.request.Request(
            f"http://{self.address}{path}",
            data=body,
            headers=headers,
            method=method,
        )
        try:
            with urllib.request.urlopen(request, timeout=timeout) as response:
                result = Response(response.status, response.read(), response.headers)
        except urllib.error.HTTPError as error:
            result = Response(error.code, error.read(), error.headers)
        assert result.status == expected, (
            f"{method} {path}: expected {expected}, got {result.status}: "
            f"{result.body.decode(errors='replace')}"
        )
        return result

    def multipart_mutation(
        self,
        mutation: dict[str, Any],
        handle: str,
        filename: str,
        content: bytes,
    ) -> dict[str, Any]:
        boundary = "----vibetable-release-matrix-" + uuid.uuid4().hex
        chunks: list[bytes] = []

        def part(name: str, value: bytes, file_name: str | None = None) -> None:
            chunks.append(f"--{boundary}\r\n".encode())
            disposition = f'Content-Disposition: form-data; name="{name}"'
            if file_name:
                disposition += f'; filename="{file_name}"'
            chunks.append((disposition + "\r\n").encode())
            if file_name:
                chunks.append(b"Content-Type: image/png\r\n")
            chunks.append(b"\r\n")
            chunks.append(value)
            chunks.append(b"\r\n")

        part("request", json.dumps(mutation, separators=(",", ":")).encode())
        part(f"upload:{handle}", content, filename)
        chunks.append(f"--{boundary}--\r\n".encode())
        return self.request(
            "POST",
            "/api/vibetable/v1/mutations/apply",
            body=b"".join(chunks),
            content_type=f"multipart/form-data; boundary={boundary}",
        ).json()

    def stop(self) -> None:
        process = self.process
        if process is None:
            return
        if process.poll() is None and self.address:
            with contextlib.suppress(Exception):
                self.request(
                    "POST",
                    "/api/vibetable/v1/shutdown",
                    expected=202,
                    timeout=5,
                )
        try:
            process.wait(timeout=15)
        except subprocess.TimeoutExpired:
            process.kill()
            process.wait(timeout=10)
        if process.stdout:
            process.stdout.close()
        if process.stderr:
            process.stderr.close()
        self.process = None
        self.address = ""


def _create_v2_workspace(root: Path) -> Path:
    metadata = root / ".vibetable"
    for name in (
        "data",
        "topology",
        "objects",
        "audit",
        "snapshots",
        "coordination",
        "quarantine",
        "temp",
    ):
        (metadata / name).mkdir(parents=True)
    manifest = {
        "contractVersion": "2.0",
        "formatVersion": 1,
        "workspaceId": WORKSPACE_ID,
        "displayName": "Packaged sidecar v2 matrix",
        "createdAt": "2026-07-28T08:00:00Z",
        "storageMode": "direct",
        "encryptionMode": "convenient",
        "repositoryFormat": "kopia-v3",
        "topologySchemaVersion": 1,
        "businessSchemaVersion": 1,
        "importedFromWorkspaceId": None,
        "sourceSnapshotId": None,
    }
    (metadata / "workspace.json").write_text(
        json.dumps(manifest, ensure_ascii=False, separators=(",", ":")),
        encoding="utf-8",
    )
    return metadata / "data"


def _workspace_v2_request(
    sequence: int,
    method: str,
    params: dict[str, Any],
    *,
    operation_id: str | None = None,
) -> dict[str, Any]:
    operation_id = operation_id or str(uuid.uuid4())
    return {
        "jsonrpc": "2.0",
        "id": f"release-matrix-v2-{sequence}",
        "method": method,
        "wire": {
            "scope": "workspace",
            "workspaceId": WORKSPACE_ID,
            "sessionEpoch": SESSION_EPOCH,
            "operationId": operation_id,
            "sequence": sequence,
        },
        "params": params,
    }


def _path_grant_header(
    grant_id: str,
    method: str,
    operation_id: str,
    purpose: str,
    path: Path,
) -> dict[str, str]:
    envelope = {
        "grantId": grant_id,
        "method": method,
        "operationId": operation_id,
        "purpose": purpose,
        "path": str(path.resolve()),
    }
    encoded = base64.urlsafe_b64encode(
        json.dumps(envelope, separators=(",", ":")).encode("utf-8")
    ).decode("ascii")
    return {"X-VibeTable-Path-Grant": encoded.rstrip("=")}


def _assert_snapshot_package(
    path: Path,
    *,
    required_entries: set[str] | None = None,
) -> dict[str, Any]:
    with zipfile.ZipFile(path) as archive:
        infos = archive.infolist()
        names = {item.filename for item in infos}
        assert len(names) == len(infos), archive.namelist()
        for item in infos:
            archive_path = PurePosixPath(item.filename)
            assert not archive_path.is_absolute()
            assert ".." not in archive_path.parts
            assert "\\" not in item.filename
            assert ((item.external_attr >> 16) & 0o170000) != 0o120000
        required = {"manifest.json"} | (required_entries or set())
        assert required <= names, sorted(names)
        manifest = json.loads(archive.read("manifest.json"))
        metadata = manifest["metadata"]
        assert metadata["formatVersion"] == 2
        assert metadata["workspaceId"]
        assert metadata["snapshotId"]
        entries = manifest["entries"]
        assert set(entries) == names - {"manifest.json"}
        for name, expected in entries.items():
            assert hashlib.sha256(archive.read(name)).hexdigest() == expected
        return manifest


def _run_workspace_v2_smoke(
    binary: Path,
    workspace_root: Path,
    package_root: Path,
) -> dict[str, str]:
    build_info_result = subprocess.run(
        [str(binary), "--build-info"],
        cwd=binary.parent,
        check=True,
        capture_output=True,
        text=True,
        encoding="utf-8",
    )
    build_info = json.loads(build_info_result.stdout)
    assert build_info["protocolV2Version"] == "2.0"
    assert build_info["workspaceFormat"] == "1"
    assert build_info["repositoryFormat"] == "kopia-v3"
    assert build_info["snapshotFormat"] == "2"
    assert build_info["packageFormat"] == "2"
    assert build_info["kopiaVersion"]
    assert build_info["ageVersion"]

    data_dir = _create_v2_workspace(workspace_root)
    sidecar = Sidecar(
        binary,
        data_dir,
        workspace_identity={
            "VIBETABLE_WORKSPACE_ID": WORKSPACE_ID,
            "VIBETABLE_WORKSPACE_SESSION_EPOCH": str(SESSION_EPOCH),
            "VIBETABLE_WORKSPACE_FENCE_EPOCH": str(FENCE_EPOCH),
            "VIBETABLE_WORKSPACE_CLAIM_ID": CLAIM_ID,
        },
    )
    try:
        sidecar.start()
        capabilities = sidecar.request(
            "GET",
            "/api/vibetable/v2/capabilities",
        ).json()
        assert capabilities["contractVersion"] == "2.0"
        assert capabilities["workspaceId"] == WORKSPACE_ID
        expected_methods = {
            "snapshot.request",
            "snapshot.list",
            "snapshot.inspect",
            "snapshot.export",
            "snapshot.inspectPackage",
            "snapshot.import",
            "retention.get",
            "retention.update",
            "fileHistory.readTree",
        }
        assert expected_methods <= set(capabilities["rpcMethods"])
        registrations = {item["method"]: item["scope"] for item in capabilities["registrations"]}
        assert registrations["snapshot.export"] == "workspace"
        assert registrations["snapshot.inspectPackage"] == "global"
        assert registrations["snapshot.import"] == "global"

        rejected = sidecar.request(
            "POST",
            "/api/vibetable/v1/history/restore-apply",
            json_body={},
            expected=423,
        ).json()
        assert rejected["code"] == "workspace.v1_write_disabled"

        captured = sidecar.request(
            "POST",
            "/api/vibetable/v2/rpc",
            json_body=_workspace_v2_request(
                1,
                "snapshot.request",
                {"trigger": "manual", "urgency": "foreground"},
            ),
        ).json()
        assert "error" not in captured, captured
        assert captured["result"]["state"] == "ready"

        listed = sidecar.request(
            "POST",
            "/api/vibetable/v2/rpc",
            json_body=_workspace_v2_request(
                2,
                "snapshot.list",
                {"cursor": None, "limit": 50},
            ),
        ).json()
        assert "error" not in listed, listed
        snapshots = listed["result"]["snapshots"]
        assert len(snapshots) == 1, snapshots
        snapshot_id = snapshots[0]["snapshotId"]

        exported_path = workspace_root / "release-matrix.vtsnapshot"
        operation_id = str(uuid.uuid4())
        grant_id = "host-path-grant://" + str(uuid.uuid4())
        exported = sidecar.request(
            "POST",
            "/api/vibetable/v2/rpc",
            json_body=_workspace_v2_request(
                3,
                "snapshot.export",
                {
                    "snapshotId": snapshot_id,
                    "pathGrant": grant_id,
                    "encryption": "none",
                    "recipients": [],
                    "credential": None,
                },
                operation_id=operation_id,
            ),
            request_headers=_path_grant_header(
                grant_id,
                "snapshot.export",
                operation_id,
                "snapshot-export",
                exported_path,
            ),
        ).json()
        assert "error" not in exported, exported
        exported_digest = "sha256:" + hashlib.sha256(exported_path.read_bytes()).hexdigest()
        assert exported["result"]["sha256"] == exported_digest
        exported_manifest = _assert_snapshot_package(
            exported_path,
            required_entries={
                "snapshot/catalog.json",
                "snapshot/manifest.json",
                "snapshot/seal.json",
                "roots/topology-head.json",
                "roots/file-state-head.json",
            },
        )
        assert exported_manifest["metadata"]["workspaceId"] == WORKSPACE_ID
        assert exported_manifest["metadata"]["snapshotId"] == snapshot_id

        fixture_path = (
            package_root / "contracts" / "v2" / "fixtures" / "snapshot-package-v2.vtsnapshot"
        )
        assert fixture_path.is_file(), fixture_path
        fixture_manifest = _assert_snapshot_package(fixture_path)
        assert fixture_manifest["metadata"]["formatVersion"] == 2
        return {
            "workspace-v2-build-info": "passed",
            "workspace-v2-capabilities": "passed",
            "workspace-v2-legacy-write-rejection": "passed",
            "workspace-v2-snapshot-package": "passed",
        }
    finally:
        sidecar.stop()


def _page(sidecar: Sidecar, table_id: str) -> dict[str, Any]:
    return sidecar.request(
        "POST",
        "/api/vibetable/v1/query",
        json_body={
            "operation": "page",
            "tableId": table_id,
            "query": {"filters": [], "sorts": [], "offset": 0, "limit": 100},
        },
    ).json()


def _apply_schema(
    sidecar: Sidecar,
    definition: dict[str, Any],
    expected_revision: int,
    operation_id: str,
) -> dict[str, Any]:
    return sidecar.request(
        "POST",
        "/api/vibetable/v1/schema/apply",
        json_body={
            "definition": definition,
            "expectedRevision": expected_revision,
            "operationId": operation_id,
        },
    ).json()


def _apply(
    sidecar: Sidecar,
    table: str,
    revision: str,
    key: str,
    operations: list[dict[str, Any]],
    *,
    expected: int = 200,
) -> dict[str, Any]:
    return sidecar.request(
        "POST",
        "/api/vibetable/v1/mutations/apply",
        json_body=_mutation(table, revision, key, operations),
        expected=expected,
    ).json()


def _read_sse_event(
    sidecar: Sidecar,
    ready: threading.Event,
    result: queue.Queue[dict[str, Any]],
    expected_record_id: str,
    expected_data_revision: str,
) -> None:
    request = urllib.request.Request(
        f"http://{sidecar.address}/api/vibetable/v1/events",
        headers={SESSION_HEADER: sidecar.secret},
    )
    try:
        with urllib.request.urlopen(request, timeout=20) as response:
            assert response.status == 200
            ready.set()
            event: dict[str, str] = {}
            deadline = time.monotonic() + 15
            while time.monotonic() < deadline:
                line = response.readline().decode().strip()
                if not line:
                    if event.get("data"):
                        payload = json.loads(event["data"])
                        if (
                            payload.get("topic") == "data.changed"
                            and expected_record_id in payload.get("recordIds", [])
                            and payload.get("operation") == "update"
                            and payload.get("dataRevision") == expected_data_revision
                        ):
                            result.put(payload)
                            return
                        event = {}
                    continue
                if ": " in line:
                    key, value = line.split(": ", 1)
                    event[key] = value
    except Exception as exc:
        result.put({"error": repr(exc)})


def run_matrix(binary: Path, data_dir: Path) -> dict[str, str]:
    assert binary.is_file(), f"published sidecar missing: {binary}"
    assert (binary.parent / "migrations" / "manifest.json").is_file()
    assert (binary.parent / f"{binary.name}.sha256").is_file()
    coverage: dict[str, str] = {}
    sidecar = Sidecar(binary, data_dir)
    sidecar.start()
    try:
        assert (data_dir / "data.db").is_file()
        coverage["fresh-data+migrations"] = "passed"

        customers = _apply_schema(
            sidecar,
            _table("matrix_customers", [_field("name_id", "name")]),
            0,
            "create-customers",
        )
        orders_base = _table(
            "matrix_orders",
            [
                _field("title_id", "title"),
                _field(
                    "quantity_id",
                    "quantity",
                    data_type="integer",
                    storage_type="number",
                ),
            ],
        )
        orders = _apply_schema(sidecar, orders_base, 0, "create-orders")
        order_id = "matrixorder0001"
        _apply(
            sidecar,
            "matrix_orders",
            orders["schemaRevision"],
            "insert-before-alter",
            [
                {
                    "kind": "insert",
                    "recordId": order_id,
                    "values": {"title": "before backup", "quantity": 4},
                }
            ],
        )
        relation = {
            "targetTableId": "matrix_customers",
            "cardinality": "one",
            "deletePolicy": "setNull",
            "junctionTableId": None,
        }
        lookup = {
            "relationFieldId": "customer_id",
            "targetFieldId": "name_id",
            "aggregate": "first",
        }
        attachment = {
            "maxFiles": 2,
            "maxBytesPerFile": 1048576,
            "allowedMimeTypes": ["image/png"],
            "thumbnailVariants": ["64x64"],
            "protected": True,
        }
        formula = {
            "language": "cel-v1",
            "source": "quantity * 2",
            "resultType": "integer",
            "version": 1,
            "status": "ready",
        }
        # Alter requests carry the last persisted definition/revision, not the
        # original create draft.
        orders_base = orders
        orders_base["fields"].extend(
            [
                _field(
                    "double_id",
                    "double_quantity",
                    kind="formula",
                    data_type="formula",
                    storage_type="number",
                    nullable=False,
                    read_only=True,
                    formula=formula,
                ),
                _field(
                    "customer_id",
                    "customer",
                    kind="relation",
                    data_type="relation",
                    storage_type="relation",
                    relation=relation,
                ),
                _field(
                    "customer_name_id",
                    "customer_name",
                    kind="lookup",
                    data_type="lookup",
                    storage_type="text",
                    read_only=True,
                    lookup=lookup,
                ),
                _field(
                    "images_id",
                    "images",
                    kind="attachment",
                    data_type="file",
                    storage_type="file",
                    attachment=attachment,
                ),
            ]
        )
        orders_base["indexes"] = [
            {"name": "idx_matrix_orders_title", "fieldIds": ["title_id"], "unique": False}
        ]
        orders = _apply_schema(sidecar, orders_base, 1, "alter-orders")
        assert orders["schemaRevision"] == "schema_0002"
        coverage["schema-create-alter-index"] = "passed"

        preview = sidecar.request(
            "POST",
            "/api/vibetable/v1/formulas/preview",
            json_body={
                "definition": orders,
                "row": {"quantity": 7},
                "changedFieldIds": ["quantity_id"],
            },
        ).json()
        assert preview["values"]["double_quantity"] == 14
        # Describing a backfilling formula schedules/resumes the durable job.
        sidecar.request("GET", "/api/vibetable/v1/schema/tables/matrix_orders")
        deadline = time.monotonic() + 15
        while True:
            row = _page(sidecar, "matrix_orders")["rows"][0]
            if row.get("double_quantity") == 8:
                break
            assert time.monotonic() < deadline, row
            time.sleep(0.1)
        coverage["formula-preview-save-backfill"] = "passed"

        customer_id = "matrixcustomer1"
        _apply(
            sidecar,
            "matrix_customers",
            customers["schemaRevision"],
            "customer-insert",
            [{"kind": "insert", "recordId": customer_id, "values": {"name": "Ada"}}],
        )
        update_receipt = _apply(
            sidecar,
            "matrix_orders",
            orders["schemaRevision"],
            "order-update-relation",
            [
                {
                    "kind": "update",
                    "recordId": order_id,
                    "values": {"title": "updated", "customer": customer_id},
                }
            ],
        )
        assert update_receipt["status"] == "applied"
        page = _page(sidecar, "matrix_orders")
        assert page["totalRows"] == 1
        assert page["rows"][0]["title"] == "updated"
        assert page["rows"][0]["customer_name"] == "Ada"
        described = sidecar.request(
            "GET", "/api/vibetable/v1/relations/describe?tableId=matrix_orders"
        ).json()
        assert described["relations"][0]["relationId"] == "matrix_orders.customer_id"
        lookup_page = sidecar.request(
            "POST",
            "/api/vibetable/v1/lookups/query",
            json_body={
                "tableId": "matrix_orders",
                "schemaRevision": orders["schemaRevision"],
                "query": {"filters": [], "sorts": [], "offset": 0, "limit": 100},
            },
        ).json()
        assert lookup_page["rows"][0]["customer_name"] == "Ada"
        coverage["record-crud-query+relation-lookup"] = "passed"

        rollback = _apply(
            sidecar,
            "matrix_orders",
            orders["schemaRevision"],
            "atomic-rollback",
            [
                {
                    "kind": "insert",
                    "recordId": "matrixrollback01",
                    "values": {"title": "must rollback", "quantity": 1},
                },
                {
                    "kind": "update",
                    "recordId": "missingrecord001",
                    "values": {"title": "fail"},
                },
            ],
            expected=422,
        )
        assert rollback["code"] == "mutation.validation.failed"
        assert _page(sidecar, "matrix_orders")["totalRows"] == 1
        coverage["atomic-batch-rollback"] = "passed"

        ready = threading.Event()
        sse_result: queue.Queue[dict[str, Any]] = queue.Queue(maxsize=1)
        current_data_revision = _page(sidecar, "matrix_orders")["querySnapshot"]["dataRevision"]
        expected_data_revision = f"data_{current_data_revision + 1:04d}"
        sse_thread = threading.Thread(
            target=_read_sse_event,
            args=(
                sidecar,
                ready,
                sse_result,
                order_id,
                expected_data_revision,
            ),
            daemon=True,
        )
        sse_thread.start()
        assert ready.wait(5)
        _apply(
            sidecar,
            "matrix_orders",
            orders["schemaRevision"],
            "sse-update",
            [{"kind": "update", "recordId": order_id, "values": {"quantity": 9}}],
        )
        event = sse_result.get(timeout=15)
        assert event.get("topic") == "data.changed", event
        assert event["recordIds"] == [order_id]
        coverage["sse"] = "passed"

        upload = _mutation(
            "matrix_orders",
            orders["schemaRevision"],
            "file-upload",
            [
                {
                    "kind": "setAttachments",
                    "recordId": order_id,
                    "fieldId": "images_id",
                    "uploadHandles": ["image-one"],
                    "removeStoredNames": [],
                }
            ],
        )
        sidecar.multipart_mutation(upload, "image-one", "pixel.png", PNG_1X1)
        query = urllib.parse.urlencode(
            {
                "tableId": "matrix_orders",
                "recordId": order_id,
                "fieldId": "images_id",
            }
        )
        refs = sidecar.request("GET", f"/api/vibetable/v1/attachments/refs?{query}").json()[
            "attachments"
        ]
        assert len(refs) == 1
        assert refs[0]["originalName"] == "pixel.png"
        assert refs[0]["thumbnails"][0]["variant"] == "64x64"
        download = sidecar.request(
            "GET",
            "/api/vibetable/v1/attachments/download?"
            + urllib.parse.urlencode({"capability": refs[0]["downloadCapability"]}),
        )
        assert download.body == PNG_1X1
        thumb = sidecar.request(
            "GET",
            "/api/vibetable/v1/attachments/download?"
            + urllib.parse.urlencode(
                {"capability": refs[0]["thumbnails"][0]["downloadCapability"]}
            ),
        )
        assert thumb.body.startswith(b"\x89PNG")
        unauthenticated = urllib.request.Request(
            f"http://{sidecar.address}/api/vibetable/v1/attachments/refs?{query}"
        )
        unauthenticated_status = 200
        try:
            urllib.request.urlopen(unauthenticated, timeout=5)
        except urllib.error.HTTPError as error:
            unauthenticated_status = error.code
        assert unauthenticated_status == 401
        stored_name = refs[0]["storedName"]
        _apply(
            sidecar,
            "matrix_orders",
            orders["schemaRevision"],
            "file-delete",
            [
                {
                    "kind": "setAttachments",
                    "recordId": order_id,
                    "fieldId": "images_id",
                    "uploadHandles": [],
                    "removeStoredNames": [stored_name],
                }
            ],
        )
        assert (
            sidecar.request("GET", f"/api/vibetable/v1/attachments/refs?{query}").json()[
                "attachments"
            ]
            == []
        )
        coverage["file-upload-download-delete-thumb-protected"] = "passed"

        history = sidecar.request(
            "GET",
            "/api/vibetable/v1/history/change-sets?"
            + urllib.parse.urlencode(
                {
                    "collection": "matrix_orders",
                    "itemId": order_id,
                    "scope": "row",
                }
            ),
        ).json()
        revisions = [
            change["rootRevisionId"]
            for change in history["changeSets"]
            if any(
                field.get("field") == "title" and field.get("after") == "updated"
                for field in change["scalarChanges"]
            )
        ]
        assert revisions, history
        _apply(
            sidecar,
            "matrix_orders",
            orders["schemaRevision"],
            "title-after-history",
            [{"kind": "update", "recordId": order_id, "values": {"title": "later"}}],
        )
        restore_preview = sidecar.request(
            "POST",
            "/api/vibetable/v1/history/restore-preview",
            json_body={
                "collection": "matrix_orders",
                "itemId": order_id,
                "targetRevision": revisions[0],
                "scope": "row",
            },
        ).json()
        sidecar.request(
            "POST",
            "/api/vibetable/v1/history/restore-apply",
            json_body={
                "collection": "matrix_orders",
                "itemId": order_id,
                "token": restore_preview["token"],
            },
        )
        assert _page(sidecar, "matrix_orders")["rows"][0]["title"] == "updated"
        coverage["audit+restore"] = "passed"

        sidecar.stop()
        sidecar.start()
        assert _page(sidecar, "matrix_orders")["rows"][0]["title"] == "updated"
        coverage["process-restart"] = "passed"

        _apply(
            sidecar,
            "matrix_orders",
            orders["schemaRevision"],
            "hard-delete",
            [{"kind": "delete", "recordId": order_id}],
        )
        assert _page(sidecar, "matrix_orders")["totalRows"] == 0
        coverage["record-delete"] = "passed"
        sidecar.stop()
        workspace_root = data_dir.parent / f"{data_dir.name}-workspace-v2"
        assert not workspace_root.exists(), workspace_root
        coverage.update(
            _run_workspace_v2_smoke(
                binary,
                workspace_root,
                binary.parent.parent,
            )
        )
        return coverage
    finally:
        sidecar.stop()


def build_release(*, sidecar_only: bool) -> None:
    command = [sys.executable, "scripts/build_next.py"]
    if sidecar_only:
        command.extend(["--skip-web", "--skip-backend", "--skip-desktop"])
    else:
        command.append("--release")
    subprocess.run(command, cwd=REPO_ROOT, check=True)


def published_sidecar_binary(package_root: Path) -> Path:
    """Resolve the packaged sidecar from the release layout contract."""

    root = package_root.resolve()
    layout_path = root / "publish-layout.json"
    try:
        layout = json.loads(layout_path.read_text(encoding="utf-8"))
        relative = Path(layout["launch"]["sidecar"])
    except (KeyError, OSError, json.JSONDecodeError, TypeError) as exc:
        raise AssertionError(f"invalid published sidecar layout: {layout_path}: {exc}") from exc
    assert not relative.is_absolute(), f"published sidecar path must be relative: {relative}"
    binary = (root / relative).resolve()
    assert binary.is_relative_to(root), f"published sidecar escapes package root: {relative}"
    assert binary.name == SIDECAR_NAME, f"unexpected published sidecar name: {relative}"
    return binary


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument(
        "--sidecar-only-build",
        action="store_true",
        help="development-only: publish the sidecar layout but skip other product stages",
    )
    parser.add_argument(
        "--skip-build",
        action="store_true",
        help="reuse an already built release; strict CI must not pass this flag",
    )
    parser.add_argument(
        "--package-root",
        type=Path,
        default=PUBLISH_ROOT,
        help="published layout to reuse with --skip-build",
    )
    parser.add_argument(
        "--data-dir",
        type=Path,
        help="fresh directory to use; defaults under build/qa",
    )
    parser.add_argument("--json-report", type=Path)
    args = parser.parse_args(argv)
    if not args.skip_build:
        build_release(sidecar_only=args.sidecar_only_build)
        package_root = PUBLISH_ROOT
    else:
        package_root = args.package_root.resolve()
    data_dir = args.data_dir
    if data_dir is None:
        run_root = REPO_ROOT / "build" / "qa" / ("packaged-sidecar-matrix-" + uuid.uuid4().hex)
        assert not run_root.exists(), f"matrix requires a fresh run root: {run_root}"
        data_dir = run_root / "data"
    assert not data_dir.exists(), f"matrix requires a fresh data directory: {data_dir}"
    audit_dir = data_dir.parent / "audit"
    assert not audit_dir.exists(), f"matrix requires a fresh audit directory: {audit_dir}"
    data_dir.mkdir(parents=True)
    binary = published_sidecar_binary(package_root)
    coverage = run_matrix(binary, data_dir)
    report = {
        "ok": True,
        "binary": str(binary),
        "dataDir": str(data_dir),
        "coverage": coverage,
    }
    if args.json_report:
        args.json_report.parent.mkdir(parents=True, exist_ok=True)
        args.json_report.write_text(
            json.dumps(report, ensure_ascii=False, indent=2) + "\n",
            encoding="utf-8",
        )
    print(json.dumps(report, ensure_ascii=False, indent=2))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
