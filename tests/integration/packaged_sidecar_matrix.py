"""Black-box §12.4 matrix for the sidecar from the published product layout.

The strict entry point builds the complete release before touching the sidecar.
For local iteration ``--sidecar-only-build`` keeps the exact same
``dist/VibeTable.Next/sidecar`` layout while skipping unrelated build stages.
Neither mode imports or embeds PocketBase.
"""

from __future__ import annotations

import argparse
import base64
import contextlib
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
from dataclasses import dataclass
from pathlib import Path
from typing import Any

REPO_ROOT = Path(__file__).resolve().parents[2]
PUBLISH_ROOT = REPO_ROOT / "dist" / "VibeTable.Next"
SIDECAR_NAME = "vibetable-pb.exe" if os.name == "nt" else "vibetable-pb"
SESSION_HEADER = "X-VibeTable-Session"
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
    def __init__(self, binary: Path, data_dir: Path) -> None:
        self.binary = binary
        self.data_dir = data_dir
        self.secret = uuid.uuid4().hex + uuid.uuid4().hex
        self.process: subprocess.Popen[str] | None = None
        self.address = ""

    def start(self) -> None:
        assert self.process is None
        environment = os.environ.copy()
        environment["VIBETABLE_SIDECAR_SESSION_SECRET"] = self.secret
        environment["VIBETABLE_SIDECAR_DATA_DIR"] = str(self.data_dir)
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
        expected: int = 200,
        timeout: float = 20,
    ) -> Response:
        headers = {SESSION_HEADER: self.secret}
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

        backup = sidecar.request(
            "POST",
            "/api/vibetable/v1/backups",
            json_body={"name": "release_matrix.zip"},
            expected=201,
        ).json()
        assert backup["backup"]["name"] == "release_matrix.zip"
        listed = sidecar.request("GET", "/api/vibetable/v1/backups").json()
        assert any(item["name"] == "release_matrix.zip" for item in listed["backups"])
        _apply(
            sidecar,
            "matrix_orders",
            orders["schemaRevision"],
            "after-backup",
            [{"kind": "update", "recordId": order_id, "values": {"title": "after backup"}}],
        )
        sidecar.request(
            "POST",
            "/api/vibetable/v1/backups/restore",
            json_body={"name": "release_matrix.zip"},
            expected=202,
        )
        process = sidecar.process
        assert process is not None
        assert process.wait(timeout=20) == 0
        sidecar.process = None
        sidecar.address = ""
        sidecar.start()
        assert _page(sidecar, "matrix_orders")["rows"][0]["title"] == "updated"
        coverage["backup+restore"] = "passed"

        _apply(
            sidecar,
            "matrix_orders",
            orders["schemaRevision"],
            "hard-delete",
            [{"kind": "delete", "recordId": order_id}],
        )
        assert _page(sidecar, "matrix_orders")["totalRows"] == 0
        coverage["record-delete"] = "passed"
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
        data_dir = REPO_ROOT / "build" / "qa" / ("packaged-sidecar-matrix-" + uuid.uuid4().hex)
    assert not data_dir.exists(), f"matrix requires a fresh data directory: {data_dir}"
    data_dir.mkdir(parents=True)
    binary = package_root / "sidecar" / SIDECAR_NAME
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
