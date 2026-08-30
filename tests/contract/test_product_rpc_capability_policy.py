"""Public contract tests for the Product capability and routing policy seam."""

from __future__ import annotations

import json
from collections.abc import Callable
from pathlib import Path
from typing import cast

import pytest

from contracts.v2.product_rpc_capability_policy import (
    MANIFEST,
    MANIFEST_SCHEMA,
    POLICY,
    TS_OUTPUT,
    ProductCapabilityPolicyError,
    build_manifest,
)
from tests.contract.test_product_contracts import _validate

type PolicyMutation = Callable[[dict[str, object]], None]


def _remove_first_rpc(source: dict[str, object]) -> None:
    methods = source["rpcMethods"]
    assert isinstance(methods, list)
    methods.pop()


def _invalidate_first_rpc_scope(source: dict[str, object]) -> None:
    methods = source["rpcMethods"]
    assert isinstance(methods, list)
    assert methods
    first = methods[0]
    assert isinstance(first, dict)
    first["scope"] = "session"


def test_policy_joins_catalog_and_inventory_without_changing_l1_routes() -> None:
    manifest = build_manifest()

    assert manifest["contractVersion"] == "2.0"
    assert len(manifest["rpcMethods"]) == 102
    assert len(manifest["eventTopics"]) == 6
    schema = next(item for item in manifest["rpcMethods"] if item["method"] == "schema.getTable")
    assert schema == {
        "method": "schema.getTable",
        "scope": "workspace",
        "audience": "rendererPublic",
        "capabilityId": "schema.query",
        "owner": "pythonBff",
        "effect": "read",
    }
    assert {item["owner"] for item in manifest["rpcMethods"]} == {"pythonBff"}
    assert {item["owner"] for item in manifest["eventTopics"]} == {"pythonBff"}
    events = {item["topic"]: item for item in manifest["eventTopics"]}
    assert events["plugin.interaction.requested"]["audience"] == "rendererPublic"
    assert events["plugin.file.requested"]["audience"] == "hostOnly"


def test_generated_manifest_schema_is_closed_for_joined_entries() -> None:
    schema = json.loads(MANIFEST_SCHEMA.read_text(encoding="utf-8"))

    assert schema["additionalProperties"] is False
    rpc = schema["properties"]["rpcMethods"]["items"]
    assert rpc["additionalProperties"] is False
    assert schema["properties"]["rpcMethods"]["uniqueItems"] is True
    assert schema["properties"]["eventTopics"]["uniqueItems"] is True
    assert set(rpc["required"]) == {
        "method",
        "scope",
        "audience",
        "capabilityId",
        "owner",
        "effect",
    }


POLICY_MUTATIONS: list[tuple[PolicyMutation, str]] = [
    (lambda source: source.update(unknown=True), "policy has unknown or missing fields"),
    (
        _remove_first_rpc,
        "rpcMethods must exactly match catalog canonical order",
    ),
    (
        _invalidate_first_rpc_scope,
        "rpcMethods scope is invalid",
    ),
]


@pytest.mark.parametrize(
    ("mutate", "message"),
    POLICY_MUTATIONS,
)
def test_policy_fails_closed_for_authored_policy_mismatches(
    tmp_path: Path, mutate: PolicyMutation, message: str
) -> None:
    source = json.loads(POLICY.read_text(encoding="utf-8"))
    mutate(source)
    policy = tmp_path / "policy.json"
    policy.write_text(json.dumps(source), encoding="utf-8")

    with pytest.raises(ProductCapabilityPolicyError, match=message):
        build_manifest(policy)


def test_generated_types_and_python_current_owner_adapter_are_exact() -> None:
    from backend.contracts.generated_product_rpc_capabilities import (
        CurrentOwner,
        current_owner_methods,
    )

    public_types = TS_OUTPUT.read_text(encoding="utf-8")
    assert "export type ProductRpcMethod" in public_types
    assert '"schema.getTable"' in public_types
    assert '"plugin.upgrade"' not in public_types
    methods = current_owner_methods("pythonBff")
    assert len(methods) == 102
    assert methods[0] == "command.list"
    assert current_owner_methods("goSidecar") == ()
    with pytest.raises(ValueError, match="unknown current owner"):
        current_owner_methods(cast(CurrentOwner, "retiredOwner"))


def test_generated_manifest_validates_against_its_closed_schema() -> None:
    manifest = json.loads(MANIFEST.read_text(encoding="utf-8"))
    schema = json.loads(MANIFEST_SCHEMA.read_text(encoding="utf-8"))

    _validate(manifest, schema, schema)
