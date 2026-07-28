from __future__ import annotations

import copy
import hashlib
import json
import subprocess
import sys
from pathlib import Path

from tests.contract.test_v1_contracts import SchemaMismatchError, _validate

ROOT = Path(__file__).parents[2]
CONTRACT_ROOT = ROOT / "contracts" / "v2"
SCHEMA_PATH = CONTRACT_ROOT / "contracts.schema.json"
FIXTURES = CONTRACT_ROOT / "fixtures"

FIXTURE_DEFINITIONS = {
    "workspace-manifest.json": "WorkspaceManifest",
    "workspace-registry-entry.json": "WorkspaceRegistryEntry",
    "workspace-session.json": "WorkspaceSession",
    "file-document.json": "FileDocument",
    "file-revision.json": "FileRevision",
    "snapshot-manifest.json": "SnapshotManifest",
    "snapshot-seal.json": "SnapshotSeal",
    "snapshot-catalog-entry.json": "SnapshotCatalogEntry",
    "lease-claim.json": "LeaseClaim",
    "retention-policy.json": "RetentionPolicy",
    "workspace-event.json": "WorkspaceEvent",
    "rpc-catalog.json": "RpcContractCatalog",
}


def _load(path: Path) -> object:
    return json.loads(path.read_text(encoding="utf-8"))


def test_v2_fixtures_validate_against_closed_schema() -> None:
    schema = _load(SCHEMA_PATH)
    assert isinstance(schema, dict)
    assert {path.name for path in FIXTURES.glob("*.json")} == set(FIXTURE_DEFINITIONS)
    for filename, definition in FIXTURE_DEFINITIONS.items():
        payload = _load(FIXTURES / filename)
        _validate(payload, schema["$defs"][definition], schema)
        _validate(payload, schema, schema)


def test_v2_negative_shapes_fail_closed() -> None:
    schema = _load(SCHEMA_PATH)
    assert isinstance(schema, dict)
    payload = _load(FIXTURES / "snapshot-manifest.json")
    assert isinstance(payload, dict)
    payload["unknown"] = True
    try:
        _validate(payload, schema["$defs"]["SnapshotManifest"], schema)
    except SchemaMismatchError:
        pass
    else:
        raise AssertionError("unknown snapshot field was accepted")

    payload.pop("unknown")
    payload["trigger"] = "backup"
    try:
        _validate(payload, schema["$defs"]["SnapshotManifest"], schema)
    except SchemaMismatchError:
        pass
    else:
        raise AssertionError("retired backup trigger was accepted")


def test_shared_v2_negative_fixture_corpus_fails_closed() -> None:
    schema = _load(SCHEMA_PATH)
    corpus = _load(CONTRACT_ROOT / "negative-fixtures.json")
    assert isinstance(schema, dict)
    assert isinstance(corpus, dict)
    for case in corpus["cases"]:
        raw = (FIXTURES / case["fixture"]).read_text(encoding="utf-8")
        if case["operation"] == "appendRaw":
            try:
                json.loads(raw + case["value"])
            except json.JSONDecodeError:
                continue
            raise AssertionError(f"{case['name']} was accepted")
        payload = json.loads(raw)
        target = payload
        for segment in case["path"][:-1]:
            target = target[segment]
        key = case["path"][-1]
        if case["operation"] == "remove":
            target.pop(key)
        else:
            target[key] = case["value"]
        definition = FIXTURE_DEFINITIONS[case["fixture"]]
        try:
            _validate(payload, schema["$defs"][definition], schema)
        except SchemaMismatchError:
            continue
        raise AssertionError(f"{case['name']} was accepted")


def test_v2_catalog_is_generated_and_workspace_scopes_are_complete() -> None:
    subprocess.run(
        [sys.executable, str(CONTRACT_ROOT / "generate_rpc_catalog.py"), "--check"],
        cwd=ROOT,
        check=True,
    )
    catalog = _load(FIXTURES / "rpc-catalog.json")
    assert isinstance(catalog, dict)
    assert [case["method"] for case in catalog["rpcCases"]] == catalog["rpcMethods"]
    for case in catalog["rpcCases"]:
        wire = case["request"]["wire"]
        assert case["success"]["wire"] == wire == case["error"]["wire"]
        assert case["request"]["method"] == case["method"]
        _validate(
            case["request"]["params"],
            case["paramsSchema"],
            case["paramsSchema"],
        )
        _validate(
            case["success"]["result"],
            case["resultSchema"],
            case["resultSchema"],
        )
        if case["scope"] == "workspace":
            assert set(wire) == {
                "scope",
                "workspaceId",
                "sessionEpoch",
                "operationId",
                "sequence",
            }
        else:
            assert set(wire) == {"scope", "operationId", "sequence"}
    assert {case["topic"] for case in catalog["eventCases"]} == set(catalog["eventTopics"])


def test_workspace_create_location_policy_is_strict_and_grant_is_explicit() -> None:
    catalog = _load(FIXTURES / "rpc-catalog.json")
    create = next(case for case in catalog["rpcCases"] if case["method"] == "workspace.create")
    params = create["request"]["params"]
    schema = create["paramsSchema"]
    assert params["locationPolicy"] == "managedDefault"
    assert params["selectedRootGrant"] is None
    assert schema["properties"]["locationPolicy"]["enum"] == [
        "managedDefault",
        "other",
    ]
    for invalid in (
        {**params, "locationPolicy": "remote"},
        {key: value for key, value in params.items() if key != "selectedRootGrant"},
        {**params, "unexpected": True},
    ):
        try:
            _validate(invalid, schema, schema)
        except SchemaMismatchError:
            continue
        raise AssertionError(f"invalid workspace.create params were accepted: {invalid}")


def test_v2_catalog_nonempty_array_items_fail_closed() -> None:
    catalog = _load(FIXTURES / "rpc-catalog.json")
    assert isinstance(catalog, dict)
    case = next(item for item in catalog["rpcCases"] if item["method"] == "conflict.inspect")
    assert case["success"]["result"]["items"]
    schema = case["resultSchema"]
    for mutate in ("unknown", "enum", "required"):
        result = copy.deepcopy(case["success"]["result"])
        item = result["items"][0]
        if mutate == "unknown":
            item["unexpected"] = True
        elif mutate == "enum":
            item["kind"] = "row"
        else:
            item.pop("itemId")
        try:
            _validate(result, schema, schema)
        except SchemaMismatchError:
            continue
        raise AssertionError(f"nested conflict item {mutate} was accepted")

    def assert_typed_items(node: object) -> None:
        if not isinstance(node, dict):
            return
        if node.get("type") == "array":
            assert node.get("items"), "array schema has untyped items"
        for value in node.values():
            assert_typed_items(value)

    for rpc_case in catalog["rpcCases"]:
        assert_typed_items(rpc_case["paramsSchema"])
        assert_typed_items(rpc_case["resultSchema"])


def test_v1_contract_bytes_remain_frozen() -> None:
    expected_lines = (CONTRACT_ROOT / "v1-frozen.sha256").read_text(encoding="utf-8").splitlines()
    expected = {
        path: digest
        for line in expected_lines
        if line.strip()
        for digest, path in [line.split("  ", 1)]
    }
    actual = {
        path.relative_to(ROOT).as_posix(): hashlib.sha256(path.read_bytes()).hexdigest()
        for path in (ROOT / "contracts" / "v1").rglob("*")
        if path.is_file()
    }
    assert actual == expected
