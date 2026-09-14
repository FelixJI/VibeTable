"""Capture actual historical grid DTO, service and local-store behavior."""

from __future__ import annotations

import argparse
import asyncio
import json
from pathlib import Path

from pydantic import ValidationError

from backend.application import grid_state_service as service_module
from backend.contracts.grid_state import (
    GridState,
    GridStateGetParams,
    GridStateResult,
    GridStateSaveParams,
)
from backend.state import local_state_store as store_module

PRODUCER = "9fa626a13840830037bcb82eaddc0adf8617075b"


async def capture(root: Path) -> dict[str, object]:
    if not Path(service_module.__file__).resolve().is_relative_to(root.resolve()):
        raise ValueError("Grid service did not come from the isolated historical producer")
    store = store_module.reset_local_state_store_for_tests(db_path=root / "grid-state.db")
    service = service_module.GridStateService()
    revisions: dict[str, str] = {}
    cases: list[dict[str, object]] = []

    def record(name: str, result: object) -> None:
        cases.append({"name": name, "result": result})

    def observe(name: str, result: GridStateResult) -> None:
        # Only server-generated opaque revision identities are normalized.
        payload = result.model_dump(by_alias=True, mode="json")
        revision = payload["revision"]
        revisions.setdefault(revision, f"revision-{len(revisions) + 1}")
        payload["revision"] = revisions[revision]
        record(name, payload)

    get = GridStateGetParams(database_id="workspace-a", table="table-a")
    try:
        first = await service.get(get)
        observe("default-first-get", first)
        observe("default-repeated-get", await service.get(get))
        full = GridState.model_validate(
            {
                "columns": [
                    {"name": "amount", "width": 4096, "visible": False, "frozen": True, "order": 1},
                    {"name": "title", "width": 1, "order": 0},
                ],
                "sorts": [{"field": "amount", "direction": "desc", "nullsLast": True}],
                "filters": [
                    {"field": "amount", "operator": "eq", "value": 9007199254740993, "logic": "and"}
                ],
                "keyword": "查询😀",
                "density": "compact",
                "forcedRemote": True,
            }
        )
        saved = await service.save(
            GridStateSaveParams(
                database_id="workspace-a",
                table="table-a",
                state=full,
                revision=first.revision,
            )
        )
        observe("full-save", saved)
        observe("full-read", await service.get(get))
        observe(
            "stale-save",
            await service.save(
                GridStateSaveParams(
                    database_id="workspace-a",
                    table="table-a",
                    state=GridState(),
                    revision=first.revision,
                )
            ),
        )
        observe(
            "null-revision-existing",
            await service.save(
                GridStateSaveParams(
                    database_id="workspace-a",
                    table="table-a",
                    state=GridState(),
                    revision=None,
                )
            ),
        )
        observe(
            "other-workspace",
            await service.get(
                GridStateGetParams(
                    database_id="workspace-b",
                    table="table-a",
                )
            ),
        )
        observe(
            "other-table",
            await service.get(
                GridStateGetParams(
                    database_id="workspace-a",
                    table="table-b",
                )
            ),
        )
        store.close()
        store = store_module.reset_local_state_store_for_tests(db_path=root / "grid-state.db")
        service = service_module.GridStateService()
        observe("reopen", await service.get(get))
        mutations = {
            "row-data": {"rowData": []},
            "pending-edits": {"pendingEdits": []},
            "columns-overflow": {"columns": [{"name": "x"}] * 513},
            "sorts-overflow": {"sorts": [{}] * 17},
            "filters-overflow": {"filters": [{}] * 65},
            "keyword-overflow": {"keyword": "x" * 257},
            "width-low": {"columns": [{"name": "x", "width": 0}]},
            "width-high": {"columns": [{"name": "x", "width": 4097}]},
            "negative-order": {"columns": [{"name": "x", "order": -1}]},
            "empty-column": {"columns": [{"name": ""}]},
            "invalid-density": {"density": "dense"},
        }
        for name, value in mutations.items():
            try:
                GridState.model_validate(value)
            except ValidationError as error:
                record(
                    name,
                    [{"type": item["type"], "loc": list(item["loc"])} for item in error.errors()],
                )
            else:
                raise AssertionError(f"Historical DTO unexpectedly accepted {name}")
        for model in (GridState, GridStateGetParams, GridStateSaveParams):
            record(f"schema-{model.__name__}", model.model_json_schema(by_alias=True))
    finally:
        store.close()
        store_module._SINGLETON = None
        service_module._SINGLETON = None
    return {"producer": PRODUCER, "cases": cases}


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--producer-root", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    args.output.write_text(
        json.dumps(asyncio.run(capture(args.producer_root)), ensure_ascii=False, indent=2) + "\n",
        encoding="utf-8",
    )


if __name__ == "__main__":
    main()
