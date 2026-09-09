"""Freeze the four original Python Interface methods; never derive an oracle from Go."""

from __future__ import annotations

import argparse
import asyncio
import copy
import inspect
import json
import subprocess
from pathlib import Path

from backend.__main__ import _register_surface_methods
from backend.application.revisioned_metadata_port import (
    MetadataConflictError,
    MetadataDelete,
    MetadataQuery,
    MetadataRecord,
    MetadataWrite,
    json_object,
)
from backend.application.surface_service import SurfaceService
from backend.rpc.dispatcher import RpcDispatcher

ROOT = Path(__file__).resolve().parents[2]
PRODUCER = "12556e5db81dd49592d69b5af1780007ccd36c37"
OUTPUT = Path(__file__).with_name("surface-python-oracle.json")
SOURCES = (
    "backend/__main__.py",
    "backend/application/surface_service.py",
    "backend/application/revisioned_metadata_port.py",
    "backend/contracts/generated_workbench.py",
    "backend/contracts/workspace_v2.py",
    "backend/rpc/dispatcher.py",
    "backend/rpc/error_registry.py",
    "backend/rpc/messages.py",
)
METHODS = {"interface.list", "interface.load", "interface.commit", "interface.delete"}


def element(identity="text-1", kind="text", **changes):
    value = {
        "elementId": identity,
        "kind": kind,
        "bindingId": None,
        "actionId": None,
        "text": "Body",
        "width": "full",
        "children": [],
    }
    value.update(changes)
    return value


def binding(identity="orders", **changes):
    value = {
        "bindingId": identity,
        "query": {
            "contractVersion": "1.0",
            "tableId": "not-required-to-exist",
            "fields": ["title", "total"],
            "filters": [],
            "sorts": [],
            "cursor": None,
            "pageSize": 50,
        },
        "variables": [],
    }
    value.update(changes)
    return value


def variable(identity="v1", **changes):
    value = {
        "variableId": identity,
        "targetFieldId": "title",
        "operator": "eq",
        "source": "literal",
        "sourceBindingId": None,
        "sourceFieldId": None,
        "value": "match",
    }
    value.update(changes)
    return value


def action(identity="a1", kind="navigate", **changes):
    value = {
        "actionId": identity,
        "kind": kind,
        "bindingId": None,
        "targetPageId": "main",
        "pluginId": None,
        "pluginActionId": None,
        "requiresConfirmation": False,
    }
    value.update(changes)
    return value


def definition(**changes):
    value = {
        "contractVersion": "1.0",
        "interfaceId": "surface-1",
        "name": "Orders desk",
        "bindings": [binding()],
        "actions": [],
        "pages": [{"pageId": "main", "title": "Main", "elements": [element()]}],
    }
    value.update(changes)
    return value


def row(value=None, *, identity=None):
    value = definition() if value is None else value
    return {
        "logicalId": identity or value["interfaceId"],
        "revision": "fixture-revision-0",
        "values": value,
    }


def commit(value=None, revision=None, key="commit-1"):
    return {
        "definition": value or definition(),
        "expectedRevision": revision,
        "idempotencyKey": key,
    }


def delete(revision="fixture-revision-0", key="delete-1"):
    return {"interfaceId": "surface-1", "expectedRevision": revision, "idempotencyKey": key}


def cases():
    result = []

    def add(name, method, params, *, seed=None, failure=None, again=False):
        calls = [{"method": method, "params": params}]
        if again:
            calls.append(copy.deepcopy(calls[0]))
        result.append(
            {
                "name": name,
                "seed": copy.deepcopy(seed or []),
                "failure": failure,
                "calls": copy.deepcopy(calls),
            }
        )

    def changed(name, mutate):
        value = definition()
        mutate(value)
        add(name, "interface.commit", commit(value))

    add("list-empty", "interface.list", {})
    add("load-missing", "interface.load", {"interfaceId": "surface-1"})
    add("load-existing", "interface.load", {"interfaceId": "surface-1"}, seed=[row()])
    add("create", "interface.commit", commit())
    add(
        "update",
        "interface.commit",
        commit(definition(name="Updated"), "fixture-revision-0"),
        seed=[row()],
    )
    add("delete", "interface.delete", delete(), seed=[row()])
    add("same-commit-replay-old-current-rejects", "interface.commit", commit(), again=True)
    add(
        "same-update-replay-old-current-rejects",
        "interface.commit",
        commit(definition(name="Updated"), "fixture-revision-0"),
        seed=[row()],
        again=True,
    )
    add(
        "same-delete-replay-old-current-rejects",
        "interface.delete",
        delete(),
        seed=[row()],
        again=True,
    )
    for name, params, seed in [
        ("create-existing", commit(), [row()]),
        ("update-stale", commit(revision="stale"), [row()]),
        ("empty-revision-not-null", commit(revision=""), []),
    ]:
        add(name, "interface.commit", params, seed=seed)
    add("delete-stale", "interface.delete", delete("stale"), seed=[row()])
    add("delete-missing", "interface.delete", delete())
    for operation in ["commit", "delete"]:
        for failure in ["conflict", "storage"]:
            add(
                f"{operation}-{failure}",
                f"interface.{operation}",
                commit(revision="fixture-revision-0") if operation == "commit" else delete(),
                seed=[row()],
                failure=failure,
            )
    sorted_rows = [
        row(definition(interfaceId=identity, name=name))
        for identity, name in [
            ("z", "Straße"),
            ("b", "STRASSE"),
            ("a", "strasse"),
            ("ff", "ﬀ"),
            ("f", "FF"),
            ("sigma-final", "ς"),
            ("sigma", "Σ"),
            ("kelvin", "K"),
            ("k", "k"),
        ]
    ]
    add("list-unicode-casefold-then-identity", "interface.list", {}, seed=sorted_rows)
    add(
        "load-storage-identity-mismatch",
        "interface.load",
        {"interfaceId": "surface-1"},
        seed=[row(identity="surface-1", value=definition(interfaceId="different"))],
    )
    add(
        "load-storage-duplicate-identities",
        "interface.load",
        {"interfaceId": "surface-1"},
        seed=[row(), row()],
    )
    invalid_stored = definition(name="  ")
    add("list-invalid-storage", "interface.list", {}, seed=[row(invalid_stored)])
    for identity in ["has/slash", "x" * 129]:
        add(f"load-invalid-id-{len(identity)}", "interface.load", {"interfaceId": identity})
    changed("blank-name", lambda d: d.update(name=" \t"))
    changed("blank-page-title", lambda d: d["pages"][0].update(title=" \t"))
    changed("duplicate-binding", lambda d: d["bindings"].append(copy.deepcopy(d["bindings"][0])))
    changed(
        "duplicate-binding-field",
        lambda d: d["bindings"][0]["query"].update(fields=["title", "title"]),
    )
    changed(
        "duplicate-variable", lambda d: d["bindings"][0].update(variables=[variable(), variable()])
    )
    changed(
        "variable-target-missing",
        lambda d: d["bindings"][0].update(variables=[variable(targetFieldId="missing")]),
    )
    changed(
        "literal-variable-unrelated-source",
        lambda d: d["bindings"][0].update(variables=[variable(sourceBindingId="orders")]),
    )
    for name, changes in [
        ("source-required", {}),
        ("self-cycle", {"sourceBindingId": "orders", "sourceFieldId": "title"}),
        ("source-missing", {"sourceBindingId": "unknown", "sourceFieldId": "title"}),
    ]:
        changed(
            "selected-variable-" + name,
            lambda d, c=changes: d["bindings"][0].update(
                variables=[variable(source="selectedRecordField", **c)]
            ),
        )

    def graph(d, *, cycle=False, missing_field=False):
        d["bindings"] = [
            binding("orders"),
            binding(
                "details",
                variables=[
                    variable(
                        source="selectedRecordField",
                        sourceBindingId="orders",
                        sourceFieldId="absent" if missing_field else "title",
                    )
                ],
            ),
        ]
        if cycle:
            d["bindings"][0]["variables"] = [
                variable(
                    source="selectedRecordField", sourceBindingId="details", sourceFieldId="title"
                )
            ]

    changed("binding-DAG-accepted", graph)
    changed("binding-cycle-rejected", lambda d: graph(d, cycle=True))
    changed("source-field-missing", lambda d: graph(d, missing_field=True))
    changed("duplicate-page", lambda d: d["pages"].append(copy.deepcopy(d["pages"][0])))
    changed("duplicate-action", lambda d: d.update(actions=[action(), action()]))
    for kind in ["record.create", "record.update", "binding.refresh", "navigate", "plugin"]:
        value = definition(
            actions=[
                action(
                    kind=kind,
                    bindingId="orders"
                    if kind.startswith("record.") or kind == "binding.refresh"
                    else None,
                    targetPageId="main" if kind == "navigate" else None,
                    pluginId="plugin" if kind == "plugin" else None,
                    pluginActionId="run" if kind == "plugin" else None,
                )
            ]
        )
        add("action-accepted-" + kind, "interface.commit", commit(value))
    for name, item in [
        ("record-missing-binding", action(kind="record.create", targetPageId=None)),
        ("record-unrelated-target", action(kind="record.create", bindingId="orders")),
        ("navigate-missing-page", action(targetPageId="missing")),
        ("navigate-unrelated-binding", action(bindingId="orders")),
        ("plugin-incomplete", action(kind="plugin", targetPageId=None)),
        ("plugin-unrelated-target", action(kind="plugin", pluginId="p", pluginActionId="a")),
        ("navigate-empty-unrelated-is-allowed", action(bindingId="")),
        (
            "plugin-empty-identities-are-allowed",
            action(kind="plugin", targetPageId=None, pluginId="", pluginActionId=""),
        ),
    ]:
        changed("action-" + name, lambda d, item=item: d.update(actions=[item]))
    for name, item in [
        ("binding-missing", element(bindingId="missing")),
        ("action-missing", element(actionId="missing")),
        ("bound-kind-unbound", element(kind="metric")),
        ("action-kind-unbound", element(kind="button")),
        ("structure-bound", element(kind="section", bindingId="orders")),
        ("nonstructure-children", element(children=[element("child")])),
        ("navigation-kind-mismatch", element(kind="navigation", actionId="a1")),
        ("form-kind-mismatch", element(kind="form", bindingId="orders", actionId="a1")),
    ]:
        value = definition(
            actions=[action(kind="binding.refresh", bindingId="orders", targetPageId=None)],
            pages=[{"pageId": "main", "title": "Main", "elements": [item]}],
        )
        add("element-" + name, "interface.commit", commit(value))
    changed(
        "element-id-duplicate-across-pages",
        lambda d: d["pages"].append({"pageId": "other", "title": "Other", "elements": [element()]}),
    )
    for size in [200, 201]:
        changed(
            f"tree-count-{size}",
            lambda d, n=size: d["pages"][0].update(elements=[element(f"e{i}") for i in range(n)]),
        )
    for depth in [8, 9]:
        child = element(f"depth-{depth}")
        for level in range(depth - 1, 0, -1):
            child = element(f"depth-{level}", kind="section", children=[child])
        changed(f"tree-depth-{depth}", lambda d, c=child: d["pages"][0].update(elements=[c]))
    for name, method, params in [
        ("list-extra", "interface.list", {"extra": True}),
        ("load-missing", "interface.load", {}),
        ("load-empty", "interface.load", {"interfaceId": ""}),
        ("delete-empty-revision", "interface.delete", delete("")),
        ("commit-empty-key", "interface.commit", commit(key="")),
        (
            "commit-missing-nullable",
            "interface.commit",
            {"definition": definition(), "idempotencyKey": "x"},
        ),
        ("commit-extra", "interface.commit", {**commit(), "extra": True}),
    ]:
        add("invalidDTO-" + name, method, params)
    changed("invalidDTO-pages-empty", lambda d: d.update(pages=[]))
    changed(
        "invalidDTO-binding-fields-empty", lambda d: d["bindings"][0]["query"].update(fields=[])
    )
    changed(
        "invalidDTO-element-kind", lambda d: d["pages"][0]["elements"][0].update(kind="unknown")
    )
    changed(
        "invalidDTO-variable-limit",
        lambda d: d["bindings"][0].update(variables=[variable(f"v{i}") for i in range(33)]),
    )
    changed(
        "DTO-integral-pageSize-string", lambda d: d["bindings"][0]["query"].update(pageSize="50.0")
    )
    add("DTO-snake-case-alias", "interface.load", {"interface_id": "surface-1"}, seed=[row()])
    return result


class ScriptedMetadata:
    """Adapter script, not a PocketBase oracle; revisions are explicit fixture values."""

    def __init__(self, seed, failure):
        self.rows = [
            MetadataRecord(
                item["logicalId"], item["revision"], json_object(copy.deepcopy(item["values"]))
            )
            for item in seed
        ]
        self.failure = failure
        self.writes = 0
        self.deletes = 0
        self.receipts = {}

    async def read(self, query: MetadataQuery) -> tuple[MetadataRecord, ...]:
        assert query.namespace == "interfaces"
        return tuple(
            copy.deepcopy(item)
            for item in self.rows
            if not query.keys or item.logical_id in query.keys
        )

    def fail(self):
        if self.failure == "conflict":
            raise MetadataConflictError()
        if self.failure == "storage":
            raise OSError("scripted storage failure")

    async def write(self, command: MetadataWrite) -> MetadataRecord:
        assert command.namespace == "interfaces"
        self.fail()
        if command.idempotency_key in self.receipts:
            return self.receipts[command.idempotency_key]
        self.writes += 1
        item = MetadataRecord(
            command.logical_id, f"fixture-revision-{self.writes}", copy.deepcopy(command.values)
        )
        self.rows = [row for row in self.rows if row.logical_id != item.logical_id] + [item]
        self.receipts[command.idempotency_key] = item
        return item

    async def delete(self, command: MetadataDelete) -> None:
        assert command.namespace == "interfaces"
        self.fail()
        if command.idempotency_key in self.receipts:
            return
        self.deletes += 1
        self.rows = [row for row in self.rows if row.logical_id != command.logical_id]
        self.receipts[command.idempotency_key] = None


def check_producer():
    subprocess.run(
        ["git", "diff", "--exit-code", PRODUCER, "--", *SOURCES],
        cwd=ROOT,
        check=True,
        capture_output=True,
    )
    for symbol, expected in [
        (SurfaceService, SOURCES[1]),
        (_register_surface_methods, SOURCES[0]),
        (RpcDispatcher, SOURCES[5]),
    ]:
        assert Path(inspect.getfile(symbol)).resolve() == ROOT / expected


async def capture():
    check_producer()
    samples = cases()
    for sample in samples:
        metadata = ScriptedMetadata(sample["seed"], sample["failure"])
        dispatcher = RpcDispatcher()
        _register_surface_methods(dispatcher, SurfaceService(metadata_port=metadata))
        assert set(dispatcher.registered_methods) == METHODS
        responses = []
        for index, call in enumerate(sample["calls"], start=1):
            responses.append(await dispatcher.dispatch({"jsonrpc": "2.0", "id": index, **call}))
        sample["responses"] = responses
        sample["metadataEffects"] = {"writes": metadata.writes, "deletes": metadata.deletes}
    return {
        "producer": PRODUCER,
        "producerSources": SOURCES,
        "adapter": "ScriptedMetadata with explicit fixture revisions; not Go/PocketBase output",
        "cases": samples,
    }


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--check", action="store_true")
    args = parser.parse_args()
    result = asyncio.run(capture())
    if args.check:
        if json.loads(OUTPUT.read_text(encoding="utf-8")) != json.loads(json.dumps(result)):
            raise SystemExit("surface oracle differs from fixed Python producer")
        print(f"Validated {len(result['cases'])} fixed Python Interface cases.")
    else:
        if OUTPUT.exists():
            raise SystemExit("Oracle already exists; use --check. Do not overwrite frozen results.")
        OUTPUT.write_text(json.dumps(result, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
        print(f"Captured {len(result['cases'])} fixed Python Interface cases.")


if __name__ == "__main__":
    main()
