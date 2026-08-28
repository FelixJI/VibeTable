from __future__ import annotations

import importlib.util
import json
import re
import subprocess
import sys
from dataclasses import replace
from pathlib import Path

import pytest

from tests.contract.test_product_contracts import _validate

ROOT = Path(__file__).parents[2]
CONTRACT_ROOT = ROOT / "contracts" / "v2"
GENERATOR = CONTRACT_ROOT / "generate_workspace_rpc_capability_manifest.py"
POLICY = CONTRACT_ROOT / "workspace-rpc-capability-policy.json"
MANIFEST = CONTRACT_ROOT / "workspace-rpc-capability-manifest.json"
SCHEMA = CONTRACT_ROOT / "workspace-rpc-capability-manifest.schema.json"
WEB_PUBLIC_METHOD_SCOPES = (
    ROOT
    / "desktop"
    / "web-grid"
    / "src"
    / "contracts"
    / "generated"
    / "workspaceV2RpcCapabilities.ts"
)


def _load_generator():
    spec = importlib.util.spec_from_file_location(
        "generate_workspace_rpc_capability_manifest",
        GENERATOR,
    )
    assert spec is not None
    assert spec.loader is not None
    module = importlib.util.module_from_spec(spec)
    sys.modules[spec.name] = module
    spec.loader.exec_module(module)
    return module


def test_policy_builds_exact_workspace_rpc_capability_manifest() -> None:
    generator = _load_generator()

    manifest = generator.build_manifest(POLICY)

    assert manifest["contractVersion"] == "2.0"
    assert len(manifest["methods"]) == 59
    assert set(manifest) == {"contractVersion", "methods"}
    assert all(
        set(entry) == {"method", "scope", "capabilityId", "audience"}
        for entry in manifest["methods"]
    )
    by_method = {entry["method"]: entry for entry in manifest["methods"]}
    assert by_method["workspace.list"] == {
        "method": "workspace.list",
        "scope": "global",
        "capabilityId": "workspace.lifecycle",
        "audience": "rendererPublic",
    }
    assert by_method["workspace.relink"]["audience"] == "rendererInternal"
    assert by_method["replica.synchronize"]["audience"] == "rendererInternal"
    assert by_method["fileHistory.materializeDiffPair"] == {
        "method": "fileHistory.materializeDiffPair",
        "scope": "workspace",
        "capabilityId": "document.diff",
        "audience": "hostOnly",
    }
    assert by_method["fileHistory.assertEffectiveRevision"]["audience"] == "hostOnly"
    assert sum(entry["audience"] == "rendererPublic" for entry in manifest["methods"]) == 55
    for method, entry in by_method.items():
        prefix = method.split(".", 1)[0]
        expected_capability = {
            "snapshot": "snapshot",
            "history": "history.restore",
            "repository": "repository.protection",
            "workspaceSearch": "workspace-search",
            "retention": "retention",
            "replica": "replica",
            "conflict": "conflict",
        }.get(prefix)
        if method.startswith("workspace.storage."):
            expected_capability = "workspace.storage"
        elif prefix == "workspace":
            expected_capability = "workspace.lifecycle"
        elif method in {
            "fileHistory.materializeDiffPair",
            "fileHistory.assertEffectiveRevision",
        }:
            expected_capability = "document.diff"
        elif prefix == "fileHistory":
            expected_capability = "file-history"
        assert entry["capabilityId"] == expected_capability, method


def test_policy_delegates_every_method_scope_to_the_rpc_registry() -> None:
    policy = json.loads(POLICY.read_text(encoding="utf-8"))

    assert all(set(entry) == {"method", "capabilityId", "audience"} for entry in policy["methods"])


def test_registry_scope_change_updates_manifest_and_makes_generated_file_stale(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    generator = _load_generator()
    first = generator.RPC_REGISTRY[0]
    changed = replace(first, scope="workspace")
    monkeypatch.setattr(generator, "RPC_REGISTRY", (changed, *generator.RPC_REGISTRY[1:]))

    manifest = generator.build_manifest(POLICY)

    assert manifest["methods"][0]["method"] == first.method
    assert manifest["methods"][0]["scope"] == "workspace"
    with pytest.raises(SystemExit, match=r"workspace-rpc-capability-manifest\.json is stale"):
        generator.main(["--check"])


@pytest.mark.parametrize(
    ("mutate", "message"),
    [
        (
            lambda methods: methods.pop(0),
            "missing policy methods: ['workspace.list']",
        ),
        (
            lambda methods: methods.append(
                {
                    "method": "workspace.retired",
                    "capabilityId": "workspace.lifecycle",
                    "audience": "rendererPublic",
                }
            ),
            "unknown policy methods: ['workspace.retired']",
        ),
        (
            lambda methods: methods.append(methods[0].copy()),
            "duplicate policy method: workspace.list",
        ),
    ],
)
def test_policy_rejects_inexact_registry_coverage(
    tmp_path: Path,
    mutate,
    message: str,
) -> None:
    generator = _load_generator()
    policy = json.loads(POLICY.read_text(encoding="utf-8"))
    mutate(policy["methods"])
    policy_path = tmp_path / "policy.json"
    policy_path.write_text(json.dumps(policy), encoding="utf-8")

    with pytest.raises(ValueError, match=re.escape(message)):
        generator.build_manifest(policy_path)


@pytest.mark.parametrize(
    ("mutate", "message"),
    [
        (
            lambda policy: policy.update(unknown=True),
            "workspace RPC capability policy has unknown or missing fields",
        ),
        (
            lambda policy: policy["methods"][0].update(unknown=True),
            "workspace RPC capability policy entry has unknown or missing fields",
        ),
        (
            lambda policy: policy["methods"][0].pop("audience"),
            "workspace RPC capability policy entry has unknown or missing fields",
        ),
        (
            lambda policy: policy["methods"][0].update(audience="rendererTrusted"),
            "workspace RPC capability policy audience is invalid",
        ),
        (
            lambda policy: policy["methods"][0].update(capabilityId=""),
            "workspace RPC capability policy capabilityId is invalid",
        ),
    ],
)
def test_policy_parser_fails_closed(
    tmp_path: Path,
    mutate,
    message: str,
) -> None:
    generator = _load_generator()
    policy = json.loads(POLICY.read_text(encoding="utf-8"))
    mutate(policy)
    policy_path = tmp_path / "policy.json"
    policy_path.write_text(json.dumps(policy), encoding="utf-8")

    with pytest.raises(ValueError, match=re.escape(message)):
        generator.build_manifest(policy_path)


def test_policy_parser_rejects_duplicate_json_properties(tmp_path: Path) -> None:
    generator = _load_generator()
    raw = POLICY.read_text(encoding="utf-8").replace(
        '"contractVersion": "2.0",',
        '"contractVersion": "2.0", "contractVersion": "2.0",',
        1,
    )
    policy_path = tmp_path / "policy.json"
    policy_path.write_text(raw, encoding="utf-8")

    with pytest.raises(ValueError, match="duplicate JSON property: contractVersion"):
        generator.build_manifest(policy_path)


def test_generated_manifest_is_current_and_validates_against_closed_schema() -> None:
    subprocess.run(
        [sys.executable, str(GENERATOR), "--check"],
        cwd=ROOT,
        check=True,
    )
    manifest = json.loads(MANIFEST.read_text(encoding="utf-8"))
    schema = json.loads(SCHEMA.read_text(encoding="utf-8"))

    _validate(manifest, schema, schema)
    assert {entry["capabilityId"] for entry in manifest["methods"]} == {
        "workspace.lifecycle",
        "workspace.storage",
        "snapshot",
        "history.restore",
        "repository.protection",
        "file-history",
        "document.diff",
        "workspace-search",
        "retention",
        "replica",
        "conflict",
    }


def test_generated_web_public_method_scopes_are_current_and_closed() -> None:
    generator = _load_generator()

    public_methods = generator.build_renderer_public_method_scopes(POLICY)

    assert len(public_methods) == 55
    assert public_methods["workspace.list"] == "global"
    assert public_methods["snapshot.list"] == "workspace"
    assert "workspace.relink" not in public_methods
    assert "replica.synchronize" not in public_methods
    assert WEB_PUBLIC_METHOD_SCOPES.read_text(encoding="utf-8") == (
        generator.encode_renderer_public_method_scopes(POLICY)
    )
