"""Freeze Relation writes through the fixed historical Python dispatcher and adapter."""

from __future__ import annotations

import argparse
import asyncio
import copy
import io
import json
import os
import subprocess
import sys
import tarfile
import tempfile
import uuid
from collections.abc import Mapping, Sequence
from dataclasses import asdict, dataclass
from pathlib import Path, PurePosixPath
from unittest.mock import patch

from pydantic import JsonValue

ROOT = Path(__file__).resolve().parents[2]
PRODUCER = "e90889c1cb2c10b3f3a7c69e6f8de46dbccbeef2"
ORACLE = ROOT / "contracts/v2/relation-write-python-oracle.json"
BUILD_ROOT = ROOT / "build/qa/relation-write-oracle"
METHODS = ("relation.createTarget", "relation.applyDelta", "relation.updateSingle")


@dataclass(frozen=True)
class Step:
    method: str
    params: dict[str, JsonValue]
    direct_model: str | None = None


@dataclass(frozen=True)
class Case:
    name: str
    steps: tuple[Step, ...]
    scripted_responses: tuple[JsonValue, ...]
    source_test: str | None = None
    failure: str | None = None


def cases() -> tuple[Case, ...]:
    """Inputs and authority fixtures, never hand-authored expected responses."""
    field = json.loads(
        (ROOT / "contracts/schema-v2/fixtures/field-definition.json").read_text(encoding="utf-8")
    )
    field["logicalType"] = "formula"
    field["storage"]["kind"] = "computed"
    field["value"]["presence"] = {"mode": "computed"}
    field["display"]["kind"] = "readonly"
    field["formula"] = {
        "language": "cel-v1",
        "source": "price * quantity",
        "resultType": "number",
    }
    create: dict[str, JsonValue] = {
        "relationId": "orders.customer",
        "label": "Grace",
        "idempotencyKey": "create-customer-1",
    }
    delta: dict[str, JsonValue] = {
        "relationId": "orders.customer",
        "sourceItemId": "row-1",
        "expectedSchemaRevision": "schema_3",
        "adds": [{"collection": "customers", "itemId": "c-1"}],
        "removes": [],
        "updates": [],
        "idempotencyKey": "relation-op-1",
    }
    single: dict[str, JsonValue] = {
        "relationId": "orders.customer",
        "sourceItemId": "o-1",
        "target": {"collection": "customers", "itemId": "c-new", "label": "New customer"},
        "expectedSchemaRevision": "schema_3",
        "idempotencyKey": "single-relation-1",
        "expectedDigest": "sha256:" + "b" * 64,
    }
    created: JsonValue = {
        "target": {"tableId": "customers", "recordId": "c-2", "label": "Grace"},
    }
    described: JsonValue = {
        "tableId": "orders",
        "schemaRevision": "schema_3",
        "relations": [
            {
                "relationId": "orders.customer",
                "sourceTableId": "orders",
                "sourceFieldId": "customer",
                "physicalName": "customer_id",
                "targetTableId": "customers",
                "cardinality": "one",
                "deletePolicy": "setNull",
            }
        ],
        "lookups": [],
    }
    single_responses: tuple[JsonValue, ...] = (
        described,
        {"rows": [{"id": "o-1", "customer_id": "c-old"}]},
        {"receipt": {"changeSetId": "change-1"}},
    )
    return (
        Case(
            "original-mixed-route-sequence",
            (
                Step(
                    "schema.table.create",
                    {
                        "displayName": "订单",
                        "operationId": "operation-create-table-12345678",
                        "actor": {"id": "desktop-host", "kind": "host"},
                    },
                ),
                Step("formula.validate", {"tableId": "orders", "field": field}),
                Step(
                    "file.token",
                    {
                        "tableId": "orders",
                        "recordId": "row-1",
                        "fieldId": "invoice",
                        "storedName": "invoice.pdf",
                        "variant": "thumb",
                    },
                ),
                Step(
                    "file.applyHostChange",
                    {
                        "tableId": "orders",
                        "recordId": "row-1",
                        "fieldId": "invoice",
                        "schemaRevision": "schema_3",
                        "expectedDigest": "sha256:" + "a" * 64,
                        "hostPaths": [],
                        "removeStoredNames": ["old.pdf"],
                    },
                ),
                Step(METHODS[0], create),
                Step(METHODS[1], delta),
            ),
            (
                {
                    "contract": "vibetable.schema.v2",
                    "operationId": "operation-create-table-12345678",
                    "tableId": "tbl_orders",
                    "displayName": "订单",
                    "schemaRevision": "schema_0001",
                },
                {"valid": True, "diagnostics": []},
                {"downloadCapability": "opaque", "contractVersion": "2.0"},
                {"status": "applied"},
                created,
                {"current": [{"tableId": "customers", "recordId": "c-1", "label": "Ada"}]},
            ),
            "test_closed_routes_cover_schema_formula_file_and_remove_only_attachment",
        ),
        Case(
            "original-single-update",
            (Step(METHODS[2], single),),
            single_responses,
            "test_single_relation_update_translates_current_and_desired_targets",
        ),
        Case(
            "single-clear-target", (Step(METHODS[2], {**single, "target": None}),), single_responses
        ),
        Case(
            "create-full-values",
            (
                Step(
                    METHODS[0],
                    {
                        **create,
                        "values": {"name": "Grace", "count": 0, "enabled": False, "note": None},
                    },
                ),
            ),
            (created,),
        ),
        Case(
            "create-falsy-defaults",
            (
                Step(
                    METHODS[0],
                    {
                        **create,
                        "label": "",
                        "values": {},
                    },
                    direct_model="ProductParams",
                ),
            ),
            (created,),
        ),
        Case(
            "apply-public-error",
            (Step(METHODS[1], {key: value for key, value in delta.items() if key != "updates"}),),
            (),
            failure="product",
        ),
        Case("create-transport-error", (Step(METHODS[0], create),), (), failure="transport"),
        Case("create-invalid-authority-target", (Step(METHODS[0], create),), ({"target": []},)),
    )


async def capture(producer_root: Path) -> dict[str, object]:
    # Import only inside the isolated producer process, never the current adapter.
    import backend
    from backend.__main__ import _register_pocketbase_product_methods
    from backend.adapters.pocketbase.client import PocketBaseClient, PocketBaseProductError
    from backend.adapters.pocketbase.product_rpc import PocketBaseProductRpc
    from backend.adapters.pocketbase.transport import PocketBaseTransportError
    from backend.contracts.product_rpc import PRODUCT_RPC_REGISTRY, ProductParams
    from backend.rpc.dispatcher import RpcDispatcher

    if Path(backend.__file__).resolve().parent != producer_root / "backend":
        raise RuntimeError("Capture must import the fixed archived Python producer")

    class ScriptedTransport:
        def __init__(self, case: Case) -> None:
            self.responses = list(copy.deepcopy(case.scripted_responses))
            self.requests: list[dict[str, JsonValue]] = []
            self.failure = case.failure

        async def request(
            self,
            method: str,
            path: str,
            *,
            query: Mapping[str, JsonValue] | None = None,
            json_body: JsonValue = None,
            headers: Mapping[str, str] | None = None,
            expected_status: Sequence[int] = (200,),
        ) -> JsonValue:
            # The session is synthetic; record the header name, never a credential value.
            header_names: list[JsonValue] = []
            header_names.extend(sorted(headers or {}))
            statuses: list[JsonValue] = list(expected_status)
            self.requests.append(
                {
                    "method": method,
                    "path": path,
                    "query": dict(query) if query is not None else None,
                    "json_body": copy.deepcopy(json_body),
                    "headerNames": header_names,
                    "expected_status": statuses,
                }
            )
            failure, self.failure = self.failure, None
            if failure == "product":
                raise PocketBaseProductError(
                    status=409,
                    payload={
                        "code": "mutation.digest_conflict",
                        "message": "record changed",
                        "path": "expectedDigest",
                        "details": {"expected": "old", "actual": "new"},
                        "retryable": False,
                    },
                )
            if failure == "transport":
                raise PocketBaseTransportError(
                    "sidecar unavailable",
                    code="sidecar.unavailable",
                )
            if not self.responses:
                raise AssertionError(f"unexpected authority request: {method} {path}")
            return self.responses.pop(0)

        async def request_multipart(
            self,
            path: str,
            *,
            json_body: Mapping[str, JsonValue],
            uploads: Sequence[tuple[str, str]],
            headers: Mapping[str, str] | None = None,
            expected_status: Sequence[int] = (200,),
        ) -> JsonValue:
            raise AssertionError("Relation oracle must not upload files")

        async def download_to_file(
            self,
            path: str,
            *,
            query: Mapping[str, JsonValue],
            target_path: str,
            headers: Mapping[str, str] | None = None,
            expected_status: Sequence[int] = (200,),
            maximum_bytes: int = 2 * 1024 * 1024 * 1024,
        ) -> int:
            raise AssertionError("Relation oracle must not download files")

    output = []
    for case in cases():
        # Two independent runs expose the old tests' direct-handler seam and public DTO seam.
        streams: dict[str, object] = {}
        for seam in ("handler", "dispatcher"):
            transport = ScriptedTransport(case)
            client = PocketBaseClient(transport=transport, session_secret="oracle-fixture-session")
            service = PocketBaseProductRpc(
                client=client,
                transport=transport,
                session_secret="oracle-fixture-session",
            )
            dispatcher = RpcDispatcher()
            _register_pocketbase_product_methods(dispatcher, service)
            if not set(METHODS).issubset(dispatcher.registered_methods):
                raise RuntimeError("Producer does not register all three Relation writes")
            steps = []
            for index, step in enumerate(case.steps):
                start = len(transport.requests)
                model = (
                    PRODUCT_RPC_REGISTRY[step.method]
                    if step.direct_model != "ProductParams"
                    and step.method in ("schema.table.create", "relation.createTarget")
                    else ProductParams
                )
                # Preserve the first capture's random attachment prelude IDs as clock/RNG inputs.
                # Relation writes use caller-supplied IDs; their semantics are not normalized.
                attachment_id = {
                    "handler": "540e2cfd-b647-4ec7-9e79-edc0c9cda84d",
                    "dispatcher": "ac09584d-5132-42e3-af6f-1a35b37c8bf3",
                }[seam]
                with patch(
                    "backend.adapters.pocketbase.product_relation_lookup_file_rpc.uuid.uuid4",
                    return_value=uuid.UUID(attachment_id),
                ):
                    if seam == "handler":
                        try:
                            result = await service.invoke(
                                step.method,
                                model.model_validate(copy.deepcopy(step.params)),
                            )
                            response = {"result": result}
                        except Exception as exception:
                            response = {
                                "exception": {
                                    "type": type(exception).__module__
                                    + "."
                                    + type(exception).__name__,
                                    "message": str(exception),
                                }
                            }
                    else:
                        response = await dispatcher.dispatch(
                            {
                                "jsonrpc": "2.0",
                                "id": f"{case.name}:{index}",
                                "method": step.method,
                                "params": copy.deepcopy(step.params),
                            }
                        )
                steps.append(
                    {
                        **asdict(step),
                        "paramsModel": model.__name__
                        if seam == "handler"
                        else PRODUCT_RPC_REGISTRY[step.method].__name__,
                        "response": response,
                        "authorityRequests": copy.deepcopy(transport.requests[start:]),
                    }
                )
            if seam == "handler" and (transport.responses or transport.failure):
                raise RuntimeError(f"Unconsumed handler authority fixture in {case.name}")
            streams[seam] = {
                "steps": steps,
                "remainingResponses": len(transport.responses),
                "untriggeredFailure": transport.failure,
            }
        output.append(
            {
                "name": case.name,
                "sourceTest": case.source_test,
                "scriptedResponses": case.scripted_responses,
                "failure": case.failure,
                **streams,
            }
        )
    return {"producer": PRODUCER, "methods": METHODS, "cases": output}


def extract_backend(archive: bytes, destination: Path) -> None:
    """Admit only regular archived backend files, without replacing any existing file."""
    with tarfile.open(fileobj=io.BytesIO(archive), mode="r:") as source:
        members = source.getmembers()
        if not members:
            raise ValueError("Empty historical backend archive")
        seen: set[str] = set()
        for member in members:
            path = PurePosixPath(member.name)
            if (
                not path.parts
                or path.parts[0] != "backend"
                or path.is_absolute()
                or ".." in path.parts
                or "\\" in member.name
                or ":" in member.name
                or member.name in seen
                or not (member.isdir() or member.isfile())
            ):
                raise ValueError(f"Unsafe backend archive member: {member.name}")
            seen.add(member.name)
        destination.mkdir()
        for member in members:
            target = destination.joinpath(*PurePosixPath(member.name).parts)
            if member.isdir():
                target.mkdir(parents=True, exist_ok=True)
                continue
            target.parent.mkdir(parents=True, exist_ok=True)
            stream = source.extractfile(member)
            if stream is None:
                raise ValueError(f"Missing backend member: {member.name}")
            with stream, target.open("xb") as output:
                output.write(stream.read())


def replay_producer() -> dict[str, object]:
    if not BUILD_ROOT.resolve().is_relative_to((ROOT / "build").resolve()) or not (
        ROOT / "build"
    ).resolve().is_relative_to(ROOT.resolve()):
        raise ValueError("Oracle build directory escapes worktree")
    BUILD_ROOT.mkdir(parents=True, exist_ok=True)
    run_root = Path(tempfile.mkdtemp(prefix="replay-", dir=BUILD_ROOT))
    archive = subprocess.run(
        ["git", "archive", PRODUCER, "backend"],
        cwd=ROOT,
        capture_output=True,
        check=False,
    )
    (run_root / "archive-stderr.log").write_bytes(archive.stderr)
    archive.check_returncode()
    producer_root = run_root / "producer"
    extract_backend(archive.stdout, producer_root)
    result_path = run_root / "captured.json"
    launcher = (
        "import runpy,sys; sys.path.insert(0,sys.argv.pop(1)); "
        "runpy.run_path(sys.argv.pop(1),run_name='__main__')"
    )
    command = [
        sys.executable,
        "-I",
        "-B",
        "-c",
        launcher,
        str(producer_root),
        str(Path(__file__)),
        "--capture",
        "--producer-root",
        str(producer_root),
        "--output",
        str(result_path),
    ]
    (run_root / "command.json").write_text(
        json.dumps(command, ensure_ascii=False, indent=2) + "\n",
        encoding="utf-8",
    )
    env = dict(os.environ)
    env["PYTHONPATH"] = str(producer_root)
    completed = subprocess.run(
        command,
        cwd=producer_root,
        env=env,
        capture_output=True,
        text=True,
        encoding="utf-8",
        check=False,
        timeout=60,
    )
    (run_root / "stdout.log").write_text(completed.stdout, encoding="utf-8")
    (run_root / "stderr.log").write_text(completed.stderr, encoding="utf-8")
    completed.check_returncode()
    captured = json.loads(result_path.read_text(encoding="utf-8"))
    if not isinstance(captured, dict) or captured.get("producer") != PRODUCER:
        raise ValueError("Capture did not identify the fixed producer")
    return captured


def verify_capture(captured: dict[str, object], oracle: Path = ORACLE) -> None:
    current = json.loads(oracle.read_text(encoding="utf-8"))
    if current != captured:
        raise ValueError("Relation write oracle differs from historical producer replay")
    if current.get("producer") != PRODUCER or len(current.get("cases", [])) != len(cases()):
        raise ValueError("Incomplete Relation write oracle")


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--write", action="store_true", help="Create the fixed oracle once")
    parser.add_argument("--check", action="store_true")
    parser.add_argument("--capture", action="store_true", help=argparse.SUPPRESS)
    parser.add_argument("--producer-root", type=Path, help=argparse.SUPPRESS)
    parser.add_argument("--output", type=Path, help=argparse.SUPPRESS)
    args = parser.parse_args()
    if args.capture:
        if args.producer_root is None or args.output is None or args.write:
            parser.error("Capture requires producer-root/output and cannot write the oracle")
        captured = asyncio.run(capture(args.producer_root.resolve()))
        with args.output.open("x", encoding="utf-8") as output:
            output.write(json.dumps(captured, ensure_ascii=False, indent=2) + "\n")
        return
    if args.write and ORACLE.exists():
        parser.error("Frozen oracle already exists; refusing to overwrite")
    captured = replay_producer()
    if args.write:
        with ORACLE.open("x", encoding="utf-8") as output:
            output.write(json.dumps(captured, ensure_ascii=False, indent=2) + "\n")
    verify_capture(captured)
    print(f"Validated {len(cases())} Relation write cases from fixed Python producer.")


if __name__ == "__main__":
    main()
