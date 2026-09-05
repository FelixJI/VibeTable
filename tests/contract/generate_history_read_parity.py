"""Freeze history.read Python behavior from a separate, clean historical checkout."""

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
    base: dict[str, JsonValue] = {
        "collection": "订单",
        "itemId": "row_1",
        "limit": 20,
        "offset": 0,
        "scope": "row",
        "actions": [],
    }
    values: list[tuple[str, dict[str, JsonValue]]] = [
        ("row", base),
        (
            "unicode-filters",
            {
                **base,
                "field": "金额",
                "search": "订单 <b>& café café 🧪\u2028",
                "actorId": "用户一",
                "recordId": "row_2",
                "actions": ["update", "restore"],
                "dateFrom": "2026-08-01T00:00:00Z",
                "dateTo": "2026-08-31T23:59:59Z",
            },
        ),
        ("empty-scope-fallback", {**base, "scope": ""}),
        (
            "optional-null",
            {
                **base,
                "itemId": None,
                "field": None,
                "search": None,
                "actorId": None,
                "dateFrom": None,
                "dateTo": None,
                "recordId": None,
            },
        ),
        ("missing-actions", {key: value for key, value in base.items() if key != "actions"}),
        ("unknown-field", {**base, "extra": "unrecognized"}),
        ("nested-credential", {**base, "actions": [{"sessionSecret": "fixture-only"}]}),
        ("fractional-limit", {**base, "limit": 1.5}),
        ("boolean-limit", {**base, "limit": True}),
        ("string-limit", {**base, "limit": "20"}),
        ("null-scope", {**base, "scope": None}),
        ("null-actions", {**base, "actions": None}),
        ("empty-action", {**base, "actions": [""]}),
        ("empty-optional", {**base, "field": ""}),
        ("empty-collection", {**base, "collection": ""}),
    ]
    result: list[dict[str, JsonValue]] = [
        {"name": name, "paramsJson": json.dumps(params, ensure_ascii=False, separators=(",", ":"))}
        for name, params in values
    ]
    result.append(
        {
            "name": "non-finite-exponent",
            "paramsJson": json.dumps(base).replace('"limit": 20', '"limit": 1e309'),
        }
    )
    for code, status, retryable in [
        ("history.table_not_found", 404, False),
        ("history.storage_failed", 500, True),
    ]:
        result.append(
            {
                "name": code,
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
    if "history.read" not in current_owner_methods("pythonBff"):
        raise RuntimeError("Producer must still use the historical Python history.read route")
    register_product_rpc_errors()
    page: dict[str, JsonValue] = {
        "collection": "订单",
        "itemId": "row_1",
        "changeSets": [
            {
                "rootRevisionId": "revision_1",
                "changeSetId": "change_1",
                "activityId": None,
                "action": "update",
                "timestamp": "2026-08-10T12:34:56Z",
                "actor": {"userId": "user_1", "displayName": "用户一"},
                "scalarChanges": [{"field": "金额", "before": None, "after": 12.5}],
                "relationChanges": [],
                "itemId": "row_1",
                "recordLabel": "订单 🧪",
                "revisionIds": ["revision_1"],
                "affectedRecords": 1,
                "recordChanges": [
                    {
                        "revisionId": "revision_1",
                        "itemId": "row_1",
                        "recordLabel": None,
                        "action": "update",
                        "scalarChanges": [{"field": "金额", "before": None, "after": 12.5}],
                        "relationChanges": [],
                    }
                ],
            }
        ],
        "total": 1,
        "capabilityHash": "fixture-capability",
        "schemaRevision": "schema_7",
        "scope": "row",
        "field": None,
        "hasMore": False,
        "archivedDefaultRevisionIds": {"row_1": "revision_0"},
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
            query: dict[str, JsonValue] = {
                key: list(request.url.params.get_list(key)) for key in request.url.params
            }
            requests.append(
                {
                    "method": request.method,
                    "path": request.url.path,
                    "query": query,
                }
            )
            status = case.get("sidecarStatus", 200)
            assert isinstance(status, int)
            return httpx.Response(status, json=case.get("sidecarError", page))

        transport = StdlibPocketBaseTransport(
            PocketBaseConfig("http://127.0.0.1:43210", "a" * 64),
            http_transport=httpx.MockTransport(respond),
        )
        client = PocketBaseClient(transport=transport, session_secret="a" * 64)
        service = PocketBaseProductRpc(client=client, transport=transport, session_secret="a" * 64)
        dispatcher = RpcDispatcher()
        dispatcher.register(
            "history.read",
            partial(service.invoke, "history.read"),
            PRODUCT_RPC_REGISTRY["history.read"],
        )
        raw = case["paramsJson"]
        assert isinstance(raw, str)
        response = await dispatcher.dispatch(
            {"jsonrpc": "2.0", "id": "parity", "method": "history.read", "params": json.loads(raw)}
        )
        case["pythonResponse"] = cast(JsonValue, response)
        case["pythonRequests"] = requests
    recorded_cases: list[JsonValue] = list(observations)
    return {
        "formatVersion": 1,
        "method": "history.read",
        "page": page,
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
