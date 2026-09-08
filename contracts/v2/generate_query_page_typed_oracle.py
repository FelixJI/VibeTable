"""Reproduce the typed-shape supplement only with the unchanged Python producer."""

from __future__ import annotations

import argparse
import asyncio
import copy
import subprocess
from pathlib import Path

from backend.contracts.product_rpc import PYTHON_PRODUCT_RPC_REGISTRY, JsonObject
from contracts.v2 import generate_query_window_oracle as source

PRODUCER_COMMIT = "c97c83336e4aa1bdf993fc46a7de57040219fb03"
OUTPUT = Path(__file__).with_name("query-page-typed-python-oracle.json")


def require_python_producer() -> None:
    if source.PRODUCER_COMMIT != PRODUCER_COMMIT or "query.page" not in PYTHON_PRODUCT_RPC_REGISTRY:
        raise ValueError("Capture requires the original Python query.page owner")
    producer_root = Path(source.__file__).resolve().parents[2]
    # Compare tracked production code, including local edits, against the fixed Git producer.
    # This never checks out code or rewrites the working tree.
    result = subprocess.run(
        ["git", "diff", "--quiet", PRODUCER_COMMIT, "--", "backend"],
        cwd=producer_root,
        check=False,
    )
    if result.returncode != 0:
        raise ValueError("Capture requires unchanged backend code from the fixed Python producer")


def cases() -> tuple[source.Case, ...]:
    params: JsonObject = {"tableId": "orders", "query": {"offset": 0, "limit": 5}}
    original = next(
        case for case in source.cases() if case.name == "page-unicode-falsy-offset-limit"
    )
    response = copy.deepcopy(original.response)
    if not isinstance(response, dict):
        raise ValueError("The original page response must be an object")
    response.pop("ignored")
    response.update(offset=0, limit=5, filteredRows=1, totalRows=1)
    response["querySnapshot"] = {
        "snapshotId": "opaque-producer-snapshot",
        "digest": "opaque-producer-digest",
        "databaseId": "database-1",
        "table": "orders",
        "schemaRevision": "schema_1",
        "dataRevision": 0,
        "normalizedQuery": {"offset": 0, "limit": 5},
    }
    empty = copy.deepcopy(response)
    empty.update(rows=[], offset=5, filteredRows=1, totalRows=1)
    snapshot = empty["querySnapshot"]
    assert isinstance(snapshot, dict)
    normalized = snapshot["normalizedQuery"]
    assert isinstance(normalized, dict)
    normalized["offset"] = 5
    return (
        source.Case("page-typed-unicode-falsy", "query.page", params, response),
        source.Case(
            "page-typed-empty-offset",
            "query.page",
            {"tableId": "orders", "query": {"offset": 5, "limit": 5}},
            empty,
        ),
        source.Case("page-typed-product-error", "query.page", params, failure="product"),
    )


async def capture() -> JsonObject:
    require_python_producer()
    return {
        "producerCommit": PRODUCER_COMMIT,
        "boundary": "Python Product dispatcher + adapter; typed-shape scripted authority response, not a signed-domain snapshot",
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
        parser.error("Typed page oracle differs; inspect the change, do not regenerate")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
