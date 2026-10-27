"""Contract tests for the host-bridge message catalog (#534).

The supplement references Product method/topic authority and owns only missing
wire names and bridge boundary policy. Existing Workspace inner methods retain
their own authority; workspace.v2.request is the distinct outer envelope. These tests prove (a) the committed
TS/C# adapters are current renderings (drift), (b) a name/flag change in a
test copy changes the projections (AC6-style derivation proof), (c) the
loader fails closed on malformed catalogs, and (d) the intentional boundary
rules (retired/internal-only/bootstrap) hold in the projections.
"""

from __future__ import annotations

import copy
import json
from pathlib import Path

import pytest

from contracts.v2.generate_workspace_rpc_capability_manifest import POLICY as WORKSPACE_POLICY
from contracts.v2.host_bridge_catalog import (
    CATALOG,
    PRODUCT_POLICY,
    HostBridgeCatalogError,
    build_catalog,
    outputs,
    render_csharp,
    render_typescript,
    validate_catalog,
)


def test_generated_adapters_are_current_renderings() -> None:
    rendered = outputs(build_catalog())
    for path, text in rendered.items():
        assert path.read_text(encoding="utf-8") == text, path


def test_catalog_change_alters_both_projections() -> None:
    catalog = build_catalog()
    mutated = copy.deepcopy(catalog)
    entry = next(m for m in mutated["messages"] if m.get("name") == "database.openRequested")
    entry["name"] = "database.openRequestedV2"
    for other in mutated["messages"]:
        if "responses" in other:
            other["responses"] = [
                response if response != "database.openRequested" else "database.openRequestedV2"
                for response in other["responses"]
            ]
    validate_catalog(mutated)
    cs = render_csharp(mutated)
    ts = render_typescript(mutated)
    assert "database.openRequestedV2" in cs
    assert "database.openRequestedV2" in ts
    assert "database.openRequestedV2" not in render_csharp(catalog)
    committed_cs = next(path for path in outputs(catalog) if path.suffix == ".cs")
    committed_ts = next(path for path in outputs(catalog) if path.suffix == ".ts")
    assert committed_cs.read_text(encoding="utf-8") != cs
    assert committed_ts.read_text(encoding="utf-8") != ts


@pytest.mark.parametrize(
    "mutate",
    [
        pytest.param(lambda c: c["messages"][0].__setitem__("routerWeb", "yes"), id="flag-type"),
        pytest.param(
            lambda c: c["messages"][0].__setitem__("status", "unknown"),
            id="unknown-status",
        ),
        pytest.param(
            lambda c: c["messages"].__setitem__(0, {**c["messages"][0], "name": "NotDotted"}),
            id="bad-name",
        ),
        pytest.param(
            lambda c: [m.pop("bridgeHost") for m in c["messages"]],
            id="missing-flag",
        ),
        pytest.param(
            lambda c: next(m for m in c["messages"] if m["status"] == "retired").__setitem__(
                "routerWeb", True
            ),
            id="retired-admitted",
        ),
        pytest.param(
            lambda c: c.__setitem__("messages", [dict(m, bootstrap=True) for m in c["messages"]]),
            id="multiple-bootstrap",
        ),
        pytest.param(
            lambda c: next(m for m in c["messages"] if m.get("responses")).__setitem__(
                "responses", ["not.in.catalog"]
            ),
            id="unknown-response",
        ),
    ],
)
def test_catalog_validation_fails_closed(mutate) -> None:
    catalog = build_catalog()
    mutate(catalog)
    with pytest.raises(HostBridgeCatalogError):
        validate_catalog(catalog)


def test_projections_preserve_intentional_boundaries() -> None:
    catalog = build_catalog()
    messages = validate_catalog(catalog)
    bootstrap = [m for m in messages if m.get("bootstrap")]
    assert [m["name"] for m in bootstrap] == ["app.ready"]
    retired = {m["name"] for m in messages if m["status"] == "retired"}
    internal = {m["name"] for m in messages if m["status"] == "rendererInternalOnly"}
    assert retired
    assert all("dashboard." in name or "interface." in name for name in retired)
    assert internal == {"plugin.lifecycle.rollback"}

    ts = render_typescript(catalog)
    cs = render_csharp(catalog)
    # Retired names must not appear in any admission projection (everything
    # in the C# output BEFORE the retired array, nor anywhere in the TS
    # projections) ... but they must stay present in the retired array.
    admission_cs = cs.split("RetiredRequestTypes")[0]
    for name in retired:
        assert f'"{name}"' not in ts, name
        assert f'"{name}"' not in admission_cs, name
        assert f'"{name}"' in cs, name
    # ...while the renderer-internal rollback stays bridge/router known.
    assert '"plugin.lifecycle.rollback"' in ts
    # The app-settings family stays owned by its own contract: only the
    # generated app-settings projection may contain those names.
    for owned in ("appPreferences.get", "appPreferences.update", "appearance.changed"):
        assert f'"{owned}"' not in ts, owned
        assert f'"{owned}"' not in cs, owned
    # Workspace v2 family projection keeps its closed request name.
    assert '"workspace.v2.request"' in ts


def test_catalog_json_is_deterministic_and_sorted() -> None:
    catalog = build_catalog()
    names = [m["name"] for m in validate_catalog(catalog)]
    assert names == sorted(names)
    assert len(names) == len(set(names))
    # The catalog must be committed in the same canonical formatting the
    # generator reads (stable diffs, no hidden churn).
    assert json.loads(CATALOG.read_text(encoding="utf-8")) == catalog


@pytest.mark.parametrize(("kind", "name"), [("method", "command.list"), ("topic", "data.changed")])
@pytest.mark.parametrize("change", ["delete", "rename", "rendererInternal", "hostOnly"])
def test_product_authority_changes_fail_closed(
    tmp_path: Path, kind: str, name: str, change: str
) -> None:
    policy = json.loads(PRODUCT_POLICY.read_text(encoding="utf-8"))
    entries = policy["rpcMethods" if kind == "method" else "eventTopics"]
    entry = next(item for item in entries if item[kind] == name)
    if change == "delete":
        entries.remove(entry)
    elif change == "rename":
        entry[kind] += "Renamed"
    else:
        entry["audience"] = change
    path = tmp_path / "product-policy.json"
    path.write_text(json.dumps(policy), encoding="utf-8")
    with pytest.raises(HostBridgeCatalogError):
        build_catalog(product_policy_path=path)


@pytest.mark.parametrize(
    "name",
    ["command.list", "data.changed", "workspace.list", "appPreferences.get", "appearance.changed"],
)
def test_existing_authority_cannot_be_owned_by_supplement(name: str) -> None:
    catalog = build_catalog()
    entry = next(m for m in catalog["messages"] if m.get("name") == "admin.openRequested")
    entry["name"] = name
    with pytest.raises(HostBridgeCatalogError, match="existing authority requires a reference"):
        validate_catalog(catalog)


@pytest.mark.parametrize(("kind", "name"), [("method", "command.list"), ("topic", "data.changed")])
def test_missing_referent_cannot_generate(kind: str, name: str) -> None:
    catalog = build_catalog()
    entry = next(m for m in catalog["messages"] if m.get("reference", {}).get(kind) == name)
    entry["reference"][kind] += "Renamed"
    with pytest.raises(HostBridgeCatalogError, match="unresolved authority reference"):
        outputs(catalog)


def test_authority_reference_does_not_copy_metadata() -> None:
    catalog = build_catalog()
    references = [m["reference"] for m in catalog["messages"] if "reference" in m]
    assert sum("method" in ref for ref in references) == 65
    assert sum("topic" in ref for ref in references) == 6
    assert all(ref["source"] == "product" for ref in references)
    assert all(set(ref) in ({"source", "method"}, {"source", "topic"}) for ref in references)
    for entry in catalog["messages"]:
        assert not set(entry) & {"owner", "scope", "audience", "effect"}
    workspace = json.loads(WORKSPACE_POLICY.read_text(encoding="utf-8"))
    workspace_names = {m["method"] for m in workspace["methods"]}
    assert not workspace_names & {m["name"] for m in validate_catalog(catalog)}
    assert "workspace.v2.request" in render_typescript(catalog)


def test_public_product_authority_is_not_implicitly_exposed() -> None:
    from contracts.v2.product_rpc_capability_policy import build_manifest

    catalog = build_catalog()
    projected = {m["name"] for m in validate_catalog(catalog)}
    public = {
        m["method"] for m in build_manifest()["rpcMethods"] if m["audience"] == "rendererPublic"
    }
    omitted = public - projected
    assert omitted
    ts = render_typescript(catalog)
    assert all(f'"{name}"' not in ts for name in omitted)
