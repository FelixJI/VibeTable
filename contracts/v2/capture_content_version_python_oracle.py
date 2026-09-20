"""Capture the original named-audit-version dispatcher, DTO, service and adapters."""

from __future__ import annotations

import argparse
import ast
import asyncio
import copy
import json
from pathlib import Path

import backend
from backend.adapters.pocketbase.client import PocketBaseClient, PocketBaseProductError
from backend.adapters.pocketbase.internal_metadata import PocketBaseInternalMetadataPort
from backend.application.insights_service import InsightsService
from backend.contracts import presets_versions_dashboards as dto
from backend.rpc.dispatcher import RpcDispatcher
from backend.rpc.error_registry import ErrorDomain, register_application_errors

PRODUCER = "4a078f84156ece7f48a1fe385cb9c5740fce654c"
METHODS = {
    "version.list": ("list_versions", "ListVersionsParams"),
    "version.create": ("create_version", "CreateVersionParams"),
    "version.save": ("save_version", "SaveVersionParams"),
    "version.compare": ("compare_version", "VersionIdParams"),
    "version.promote": ("promote_version", "PromoteVersionParams"),
    "version.delete": ("delete_version", "DeleteVersionParams"),
}
TABLE = "table-articles"
ROW = "record-article"
VERSION = "11111111-1111-4111-8111-111111111111"
HASH = "a" * 64
SELECTION = {"collection": TABLE, "itemId": ROW, "versionId": VERSION}


def item(identity, key, scope=None):
    return {
        "namespace": "content_versions",
        "logicalId": identity,
        "revision": "metadata-old",
        "payload": {
            "key": key,
            "name": '命名 \\"修订',
            "mainHash": "b" * 64,
            "scope": scope or f"{TABLE}:{ROW}",
            "revisionId": "revision-saved",
            "changeSetId": "audit-saved",
            "privateExtension": {"zero": 0, "flag": False},
        },
    }


def fixture():
    return {
        "items": [
            item(VERSION, "zeta"),
            item("version-a", "alpha"),
            item("foreign", "first", "other:record"),
        ],
        "history": {
            "changeSets": [
                {
                    "changeSetId": "audit-current",
                    "rootRevisionId": "root-revision",
                    "recordChanges": [
                        {"itemId": "another", "revisionId": "wrong-revision"},
                        {"itemId": ROW, "revisionId": "revision-current"},
                    ],
                }
            ]
        },
        "preview": {
            "collection": TABLE,
            "itemId": ROW,
            "targetRevision": "revision-saved",
            "currentHash": HASH,
            "token": "oracle-restore-token",
            "canApply": True,
            "scalarChanges": [
                {"field": "title", "before": "现在", "after": "命名修订"},
                {"field": "zero", "before": 0, "after": False},
                {"field": "empty", "before": "", "after": None},
            ],
            "relationChanges": [
                {"field": "parent", "beforeItemId": None, "afterItemId": "related-record"}
            ],
        },
        "restored": {
            "collection": TABLE,
            "itemId": ROW,
            "restoredToRevision": "revision-saved",
            "newRevisionId": "revision-restored",
            "item": {"id": ROW, "title": "命名修订", "zero": False, "empty": None},
        },
    }


class ScriptedTransport:
    """Only bottom HTTP I/O is controlled; no fake application/DTO/adapter behaviour."""

    def __init__(self, initial):
        self.initial = copy.deepcopy(initial)
        self.calls = []

    async def request(
        self, method, path, *, query=None, json_body=None, headers=None, expected_status=(200,)
    ):
        if headers != {"X-VibeTable-Session": "oracle-session"} or tuple(expected_status) != (200,):
            raise AssertionError("producer changed HTTP authentication/status contract")
        self.calls.append(
            {
                "method": method,
                "path": path,
                "query": copy.deepcopy(query),
                "body": copy.deepcopy(json_body),
            }
        )
        failure = self.initial.get("failure")
        if failure and path.endswith(failure["suffix"]):
            raise PocketBaseProductError(status=409, payload=failure["payload"])
        if path.endswith("/content_versions") and method == "GET":
            return {"items": copy.deepcopy(self.initial["items"])}
        if path.endswith("/change-sets"):
            return copy.deepcopy(self.initial["history"])
        if path.endswith("/restore-preview"):
            return copy.deepcopy(self.initial["preview"])
        if path.endswith("/restore-apply"):
            return copy.deepcopy(self.initial["restored"])
        if path.endswith("/content_versions/upsert"):
            return {
                "status": "applied",
                "changeSetId": "metadata-change",
                "emittedEvents": ["metadata-event"],
                "item": {
                    "namespace": "content_versions",
                    "logicalId": json_body["logicalId"],
                    "revision": "metadata-next",
                    "payload": copy.deepcopy(json_body["payload"]),
                },
            }
        if path.endswith("/content_versions/delete"):
            return {
                "status": "applied",
                "changeSetId": "metadata-delete",
                "emittedEvents": ["metadata-deleted"],
            }
        raise AssertionError(f"Unexpected producer HTTP request: {method} {path}")


def cases():
    samples = []

    def add(name, method, params, *, initial=None, repeat=1):
        samples.append(
            {
                "name": name,
                "method": method,
                "params": params,
                "initial": fixture() if initial is None else initial,
                "repeat": repeat,
            }
        )

    create = {
        "collection": TABLE,
        "itemId": ROW,
        "operationId": "create-operation",
        "key": "release",
        "name": "发布修订",
    }
    save = {**SELECTION, "values": {}, "operationId": "save-operation"}
    promote = {**SELECTION, "mainHash": HASH, "operationId": "promote-operation"}
    delete = {**SELECTION, "expectedRevision": "metadata-old", "operationId": "delete-operation"}
    add("list-scope-sort-projection", "version.list", {"collection": TABLE, "itemId": ROW})
    add("create-current-audit-record", "version.create", create)
    add(
        "create-default-key-name",
        "version.create",
        {k: v for k, v in create.items() if k not in {"key", "name"}},
    )
    add("save-preserves-name-key-via-real-adapter", "version.save", save)
    add("compare-scalar-relation-falsy", "version.compare", SELECTION)
    add("promote-restores-audit-reference", "version.promote", promote)
    add("delete-binds-revision-operation", "version.delete", delete)
    for method, params in [
        ("version.create", create),
        ("version.save", save),
        ("version.promote", promote),
        ("version.delete", delete),
    ]:
        add(method + "-repeat-observes-bottom-calls", method, params, repeat=2)
        add(
            method + "-missing-operation",
            method,
            {k: v for k, v in params.items() if k != "operationId"},
        )
    add(
        "save-cross-record-scope-is-not-validated",
        "version.save",
        {**save, "itemId": "other-record"},
    )
    add(
        "delete-cross-record-scope-is-not-validated",
        "version.delete",
        {**delete, "itemId": "other-record"},
    )
    add("compare-cross-record-rejected", "version.compare", {**SELECTION, "itemId": "other-record"})
    add("promote-cross-record-rejected", "version.promote", {**promote, "itemId": "other-record"})
    add(
        "save-values-not-a-working-copy",
        "version.save",
        {**save, "values": {"title": "not allowed"}},
    )
    add(
        "save-unknown-id-creates-metadata", "version.save", {**save, "versionId": "missing-version"}
    )
    add("compare-unknown-id", "version.compare", {**SELECTION, "versionId": "missing-version"})
    add(
        "compare-key-is-accepted-as-identity", "version.compare", {**SELECTION, "versionId": "zeta"}
    )
    add(
        "compare-optional-operation-id",
        "version.compare",
        {**SELECTION, "operationId": "ignored-read-id"},
    )
    add("promote-stale-main-hash", "version.promote", {**promote, "mainHash": "stale"})
    for name, change in [
        ("no-token", {"token": ""}),
        ("blocked", {"canApply": False}),
        ("no-changes", {"scalarChanges": [], "relationChanges": []}),
    ]:
        initial = fixture()
        initial["preview"].update(change)
        add("promote-" + name, "version.promote", promote, initial=initial)
    for name, change in [
        ("no-history", {"changeSets": []}),
        ("invalid-change-set", {"changeSets": [None]}),
        ("missing-change-set-id", {"changeSets": [{}]}),
        (
            "root-revision-fallback",
            {"changeSets": [{"changeSetId": "audit-root", "rootRevisionId": "root-revision"}]},
        ),
        ("missing-any-revision", {"changeSets": [{"changeSetId": "audit-root"}]}),
    ]:
        initial = fixture()
        initial["history"] = change
        add("create-" + name, "version.create", create, initial=initial)
    for method, params in [("version.compare", SELECTION), ("version.promote", promote)]:
        initial = fixture()
        del initial["items"][0]["payload"]["revisionId"]
        add(method + "-missing-audit-reference", method, params, initial=initial)
    initial = fixture()
    initial["items"][0]["payload"]["mainHash"] = HASH
    add("compare-not-outdated", "version.compare", SELECTION, initial=initial)
    initial = fixture()
    initial["items"][0]["payload"]["outdated"] = "false"
    add(
        "list-stored-truthy-outdated",
        "version.list",
        {"collection": TABLE, "itemId": ROW},
        initial=initial,
    )
    initial = fixture()
    initial["failure"] = {
        "suffix": "/upsert",
        "payload": {
            "code": "metadata.revision_conflict",
            "message": "conflict",
            "retryable": False,
        },
    }
    add("save-authority-revision-conflict", "version.save", save, initial=initial)
    initial = fixture()
    initial["failure"] = {
        "suffix": "/restore-apply",
        "payload": {"code": "restore_conflict", "message": "conflict", "retryable": False},
    }
    add("promote-authority-restore-conflict", "version.promote", promote, initial=initial)
    for method, params in [
        ("version.list", {"collection": TABLE, "itemId": ROW}),
        ("version.create", create),
        ("version.save", save),
        ("version.compare", SELECTION),
        ("version.promote", promote),
        ("version.delete", delete),
    ]:
        add(method + "-unknown-dto-field", method, {**params, "private": True})
        add(method + "-array-envelope", method, [])
    add("create-name-max", "version.create", {**create, "name": "名" * 256})
    add("create-name-too-long", "version.create", {**create, "name": "名" * 257})
    add("create-empty-identity", "version.create", {**create, "collection": ""})
    add("save-values-not-object", "version.save", {**save, "values": []})
    return samples


async def capture(producer_root):
    if Path(backend.__file__).resolve().parent != producer_root / "backend":
        raise RuntimeError("capture must import the isolated fixed producer")
    source = ast.parse((producer_root / "backend/__main__.py").read_text(encoding="utf-8"))
    registrations = {}
    for node in ast.walk(source):
        if (
            isinstance(node, ast.Call)
            and isinstance(node.func, ast.Attribute)
            and node.func.attr == "register"
            and len(node.args) == 3
            and isinstance(node.args[0], ast.Constant)
            and node.args[0].value in METHODS
        ):
            handler, model = node.args[1:]
            if not isinstance(handler, ast.Attribute) or not isinstance(model, ast.Name):
                raise RuntimeError("producer registration shape changed")
            registrations[node.args[0].value] = (handler.attr, model.id)
    if registrations != METHODS:
        raise RuntimeError("producer does not register the six exact original methods/DTOs")
    register_application_errors(ErrorDomain.INSIGHTS)
    captured = []
    for case in cases():
        transport = ScriptedTransport(case["initial"])
        client = PocketBaseClient(transport=transport, session_secret="oracle-session")
        service = InsightsService(
            metadata_port=PocketBaseInternalMetadataPort(client=client), query_port=client
        )
        dispatcher = RpcDispatcher()
        for method, (handler, model) in METHODS.items():
            dispatcher.register(method, getattr(service, handler), getattr(dto, model))
        responses = []
        for _ in range(case["repeat"]):
            responses.append(
                await dispatcher.dispatch(
                    {
                        "jsonrpc": "2.0",
                        "id": "oracle",
                        "method": case["method"],
                        "params": copy.deepcopy(case["params"]),
                    }
                )
            )
        captured.append({**case, "responses": responses, "authorityCalls": transport.calls})
    return {"producer": PRODUCER, "methods": list(METHODS), "cases": captured}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--producer-root", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    result = asyncio.run(capture(args.producer_root.resolve()))
    args.output.write_text(
        json.dumps(result, ensure_ascii=False, indent=2) + "\n", encoding="utf-8"
    )


if __name__ == "__main__":
    main()
