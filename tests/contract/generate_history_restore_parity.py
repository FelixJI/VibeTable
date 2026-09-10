"""Freeze the paired history restore Python behavior from a separate, clean historical checkout."""

from __future__ import annotations

import argparse
import asyncio
import json
import logging
import subprocess
import sys
from functools import partial
from pathlib import Path
from typing import cast

import httpx
from pydantic import JsonValue


def cases() -> list[dict[str, JsonValue]]:
    preview: dict[str, JsonValue] = {
        "collection": "订单",
        "itemId": "row_1",
        "targetRevision": "revision_1",
        "scope": "row",
    }
    apply: dict[str, JsonValue] = {
        "collection": "订单",
        "itemId": "row_1",
        "token": "restore-token",
    }
    result: list[dict[str, JsonValue]] = []
    for method, base in [("history.previewRestore", preview), ("history.applyRestore", apply)]:
        values = [
            ("valid", base),
            ("unknown", {**base, "extra": 1}),
            ("empty-collection", {**base, "collection": ""}),
            ("null-item", {**base, "itemId": None}),
            ("missing-item", {key: value for key, value in base.items() if key != "itemId"}),
            ("forbidden", {**base, "itemId": {"sessionSecret": "fixture"}}),
        ]
        if method == "history.previewRestore":
            values += [
                ("missing-scope", {key: value for key, value in base.items() if key != "scope"}),
                ("empty-scope", {**base, "scope": ""}),
                ("null-scope", {**base, "scope": None}),
                ("null-field", {**base, "field": None}),
                ("empty-field", {**base, "field": ""}),
                ("cell", {**base, "scope": "cell", "field": "金额"}),
                ("invalid-scope", {**base, "scope": 4}),
            ]
        else:
            values += [
                ("null-token", {**base, "token": None}),
                ("empty-token", {**base, "token": ""}),
            ]
        for name, params in values:
            result.append(
                {
                    "name": method + "/" + name,
                    "method": method,
                    "paramsJson": json.dumps(params, ensure_ascii=False, separators=(",", ":")),
                }
            )
        for code, status, retryable in [
            ("restore_token_unknown", 404, False),
            ("restore_token_expired", 410, False),
            ("restore_conflict", 409, False),
            ("schema_drift", 409, False),
            ("history.storage_failed", 500, True),
        ]:
            result.append(
                {
                    "name": method + "/" + code,
                    "method": method,
                    "paramsJson": json.dumps(base, ensure_ascii=False),
                    "sidecarStatus": status,
                    "sidecarError": {
                        "code": code,
                        "message": "history fixture error",
                        "details": {},
                        "retryable": retryable,
                    },
                }
            )
    return result


async def produce(producer: Path) -> dict[str, JsonValue]:
    sys.path.insert(0, str(producer))
    import backend
    from backend.adapters.pocketbase.client import PocketBaseClient
    from backend.adapters.pocketbase.product_rpc import PocketBaseProductRpc
    from backend.adapters.pocketbase.transport import PocketBaseConfig, StdlibPocketBaseTransport
    from backend.contracts.product_rpc import PRODUCT_RPC_REGISTRY, current_owner_methods
    from backend.rpc.dispatcher import RpcDispatcher
    from backend.rpc.product_errors import register_product_rpc_errors

    if Path(backend.__file__).resolve().parent != producer / "backend":
        raise RuntimeError("Producer imports must resolve to the separate historical checkout")
    if not {"history.previewRestore", "history.applyRestore"}.issubset(
        current_owner_methods("pythonBff")
    ):
        raise RuntimeError(
            "Producer must still use the historical Python history.previewRestore route"
        )
    register_product_rpc_errors()
    preview: dict[str, JsonValue] = {
        "collection": "订单",
        "itemId": "row_1",
        "targetRevision": "revision_1",
        "scope": "row",
        "field": None,
        "currentHash": "fixture-current",
        "schemaRevision": "schema_7",
        "scalarChanges": [{"field": "金额", "before": 12.5, "after": 7}],
        "relationChanges": [],
        "diagnostics": [],
        "canApply": True,
        "restorableFields": ["金额"],
        "token": "restore-token",
        "expiresAt": "2026-09-09T00:00:00Z",
    }
    applied: dict[str, JsonValue] = {
        "collection": "订单",
        "itemId": "row_1",
        "restoredToRevision": "revision_1",
        "newRevisionId": "revision_2",
        "item": {"金额": 7},
    }
    observations = cases()
    for case in observations:
        requests: list[JsonValue] = []

        def respond(
            request: httpx.Request,
            *,
            case: dict[str, JsonValue] = case,
            requests: list[JsonValue] = requests,
        ) -> httpx.Response:
            requests.append(
                {
                    "method": request.method,
                    "path": request.url.path,
                    "body": json.loads(request.content),
                }
            )
            status = case.get("sidecarStatus", 200)
            assert isinstance(status, int)
            return httpx.Response(
                status,
                json=case.get(
                    "sidecarError",
                    preview if case["method"] == "history.previewRestore" else applied,
                ),
            )

        transport = StdlibPocketBaseTransport(
            PocketBaseConfig("http://127.0.0.1:43210", "a" * 64),
            http_transport=httpx.MockTransport(respond),
        )
        client = PocketBaseClient(transport=transport, session_secret="a" * 64)
        service = PocketBaseProductRpc(client=client, transport=transport, session_secret="a" * 64)
        dispatcher = RpcDispatcher()
        method = case["method"]
        assert isinstance(method, str)
        dispatcher.register(method, partial(service.invoke, method), PRODUCT_RPC_REGISTRY[method])
        raw = case["paramsJson"]
        assert isinstance(raw, str)
        response = await dispatcher.dispatch(
            {"jsonrpc": "2.0", "id": "parity", "method": method, "params": json.loads(raw)}
        )
        case["pythonResponse"] = cast(JsonValue, response)
        case["pythonRequests"] = requests
    recorded_cases: list[JsonValue] = list(observations)
    return {
        "formatVersion": 1,
        "methods": ["history.previewRestore", "history.applyRestore"],
        "preview": preview,
        "applied": applied,
        "cases": recorded_cases,
    }


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--producer-root", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    producer = args.producer_root.resolve()
    commit = subprocess.check_output(
        ["git", "-C", str(producer), "rev-parse", "HEAD"], text=True
    ).strip()
    if subprocess.check_output(
        ["git", "-C", str(producer), "status", "--porcelain", "--untracked-files=no"], text=True
    ).strip():
        raise RuntimeError("Producer tracked files must be clean")
    logging.disable(logging.CRITICAL)
    result = asyncio.run(produce(producer))
    result["producer"] = {"commit": commit, "route": "pythonBff", "python": sys.version.split()[0]}
    args.output.write_text(
        json.dumps(result, ensure_ascii=False, indent=2, allow_nan=False) + "\n",
        encoding="utf-8",
        newline="\n",
    )
    print(f"Recorded {len(cases())} historical Python cases from {commit}")


if __name__ == "__main__":
    main()
