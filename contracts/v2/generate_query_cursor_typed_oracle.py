"""Reproduce typed cursor fixtures only with the unchanged Python producer."""

from __future__ import annotations

import argparse
import asyncio
import copy
import subprocess
from pathlib import Path

from backend.contracts.product_rpc import PYTHON_PRODUCT_RPC_REGISTRY, JsonObject
from contracts.v2 import generate_query_window_oracle as source

PRODUCER_COMMIT = "c97c83336e4aa1bdf993fc46a7de57040219fb03"
METHODS = ("query.cursorOpen", "query.cursorFetch")
OUTPUT = Path(__file__).with_name("query-cursor-typed-python-oracle.json")


def require_python_producer() -> None:
    if source.PRODUCER_COMMIT != PRODUCER_COMMIT or not set(METHODS) <= set(
        PYTHON_PRODUCT_RPC_REGISTRY
    ):
        raise ValueError("Capture requires the original Python cursor owners")
    producer_root = Path(source.__file__).resolve().parents[2]
    # Compare tracked production code, including local edits, without checking out a revision.
    result = subprocess.run(
        ["git", "diff", "--quiet", PRODUCER_COMMIT, "--", "backend"],
        cwd=producer_root,
        check=False,
    )
    if result.returncode != 0:
        raise ValueError("Capture requires unchanged backend code from the fixed Python producer")


def cases() -> tuple[source.Case, ...]:
    original = next(case for case in source.cases() if case.name == "open-unicode-falsy")
    response = copy.deepcopy(original.response)
    if not isinstance(response, dict):
        raise ValueError("The original cursor response must be an object")
    response.pop("ignored")
    response["querySnapshot"] = {
        "snapshotId": "opaque-producer-snapshot",
        "digest": "opaque-producer-digest",
        "databaseId": "database-1",
        "table": "orders",
        "schemaRevision": "schema_1",
        "dataRevision": 0,
        "normalizedQuery": {"offset": 0, "limit": 2},
    }
    terminal = {**response, "rows": [], "hasMore": False, "nextCursor": None}
    result: list[source.Case] = []
    for prefix, method in zip(("open", "fetch"), METHODS, strict=True):
        params: JsonObject = (
            {"tableId": "orders", "query": {"offset": 0, "limit": 2}}
            if prefix == "open"
            else {"cursor": "opaque-producer-cursor 中文 /+"}
        )
        result.extend(
            (
                source.Case(f"{prefix}-typed-unicode-falsy", method, params, response),
                source.Case(f"{prefix}-typed-terminal", method, params, terminal),
                source.Case(f"{prefix}-typed-product-error", method, params, failure="product"),
                source.Case(
                    f"{prefix}-typed-null-rows", method, params, {**response, "rows": None}
                ),
                source.Case(
                    f"{prefix}-typed-null-row", method, params, {**response, "rows": [None]}
                ),
                source.Case(
                    f"{prefix}-typed-inconsistent-cursor",
                    method,
                    params,
                    {**response, "nextCursor": None},
                ),
            )
        )
    result.append(
        source.Case(
            "fetch-typed-empty-next-cursor",
            METHODS[1],
            {"cursor": "opaque-producer-cursor 中文 /+"},
            {**response, "nextCursor": ""},
        )
    )
    return tuple(result)


async def capture() -> JsonObject:
    require_python_producer()
    return {
        "producerCommit": PRODUCER_COMMIT,
        "boundary": "Python Product dispatcher + adapter; typed-shape scripted authority response, not signed-domain snapshots or cursors",
        "cases": [await source.capture_case(case) for case in cases()],
    }


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    mode = parser.add_mutually_exclusive_group()
    mode.add_argument("--write", action="store_true", help="Create once; never overwrite")
    mode.add_argument("--check", action="store_true", help="Read-only comparison (default)")
    args = parser.parse_args()
    generated = source.render(asyncio.run(capture()))
    if args.write:
        with OUTPUT.open("x", encoding="utf-8", newline="\n") as stream:
            stream.write(generated)
    elif OUTPUT.read_text(encoding="utf-8") != generated:
        parser.error("Typed cursor oracle differs; inspect the change, do not regenerate")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
