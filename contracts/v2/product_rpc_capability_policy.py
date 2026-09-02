"""Generate the Product capability/routing manifest and language adapters."""

from __future__ import annotations

import argparse
import json
import sys
from pathlib import Path
from typing import Literal, TypedDict, cast

ROOT = Path(__file__).parents[2]
if str(ROOT) not in sys.path:
    sys.path.insert(0, str(ROOT))

from contracts.v2.product_runtime_inventory import (  # noqa: E402
    ProductRuntimeInventoryError,
    load_product_runtime_inventory,
)

POLICY = Path(__file__).with_name("product-rpc-capability-policy.json")
CATALOG = Path(__file__).with_name("fixtures") / "product-rpc-catalog.json"
MANIFEST = Path(__file__).with_name("product-rpc-capability-manifest.json")
MANIFEST_SCHEMA = Path(__file__).with_name("product-rpc-capability-manifest.schema.json")
TS_OUTPUT = ROOT / "desktop/web-grid/src/contracts/generated/productRpcCapabilities.ts"
PYTHON_OUTPUT = ROOT / "backend/contracts/generated_product_rpc_capabilities.py"
GO_OUTPUT = ROOT / "sidecar/internal/contracts/productcapabilities/product_capabilities.go"

_ROOT_FIELDS = frozenset({"contractVersion", "rpcMethods", "eventTopics"})
_RPC_FIELDS = frozenset({"method", "scope", "audience", "capabilityId"})
_EVENT_FIELDS = frozenset({"topic", "scope", "audience", "capabilityId"})
type Scope = Literal["global", "workspace"]
type Audience = Literal["rendererPublic", "rendererInternal", "hostOnly"]
type CurrentOwner = Literal["pythonBff", "goSidecar", "wpfHost", "pythonWorker"]
type RpcEffect = Literal["read", "write"]
type EventEffect = Literal["notification"]

_SCOPES: frozenset[str] = frozenset({"global", "workspace"})
_AUDIENCES: frozenset[str] = frozenset({"rendererPublic", "rendererInternal", "hostOnly"})
_OWNERS: tuple[CurrentOwner, ...] = ("pythonBff", "goSidecar", "wpfHost", "pythonWorker")


class PolicyRpcEntry(TypedDict):
    method: str
    scope: Scope
    audience: Audience
    capabilityId: str


class PolicyEventEntry(TypedDict):
    topic: str
    scope: Scope
    audience: Audience
    capabilityId: str


class ManifestRpcEntry(PolicyRpcEntry):
    owner: CurrentOwner
    effect: RpcEffect


class ManifestEventEntry(PolicyEventEntry):
    owner: CurrentOwner
    effect: EventEffect


class ProductCapabilityManifest(TypedDict):
    contractVersion: Literal["2.0"]
    rpcMethods: list[ManifestRpcEntry]
    eventTopics: list[ManifestEventEntry]


class ProductCapabilityPolicyError(ValueError):
    """Stable fail-closed error from the Product policy seam."""


def _closed_object(pairs: list[tuple[str, object]]) -> dict[str, object]:
    result: dict[str, object] = {}
    for key, value in pairs:
        if key in result:
            raise ProductCapabilityPolicyError(f"duplicate JSON property: {key}")
        result[key] = value
    return result


def _load(path: Path) -> dict[str, object]:
    try:
        source = json.loads(path.read_text(encoding="utf-8"), object_pairs_hook=_closed_object)
    except json.JSONDecodeError as error:
        raise ProductCapabilityPolicyError(f"invalid JSON: {error.msg}") from error
    if not isinstance(source, dict):
        raise ProductCapabilityPolicyError("policy must be an object")
    return source


def _policy_entries(
    policy: dict[str, object], *, key: str, name: str, expected: list[str]
) -> list[dict[str, object]]:
    items = policy.get(key)
    fields = _RPC_FIELDS if key == "rpcMethods" else _EVENT_FIELDS
    if not isinstance(items, list):
        raise ProductCapabilityPolicyError(f"{key} must be an array")
    result: list[dict[str, object]] = []
    ordered: list[str] = []
    for item in items:
        if not isinstance(item, dict) or set(item) != fields:
            raise ProductCapabilityPolicyError(f"{key} entry has unknown or missing fields")
        declared = item.get(name)
        if not isinstance(declared, str) or not declared:
            raise ProductCapabilityPolicyError(f"{key} {name} is invalid")
        if declared in ordered:
            raise ProductCapabilityPolicyError(f"duplicate policy {name}: {declared}")
        if item.get("scope") not in _SCOPES:
            raise ProductCapabilityPolicyError(f"{key} scope is invalid")
        if item.get("audience") not in _AUDIENCES:
            raise ProductCapabilityPolicyError(f"{key} audience is invalid")
        if not isinstance(item.get("capabilityId"), str) or not item["capabilityId"]:
            raise ProductCapabilityPolicyError(f"{key} capabilityId is invalid")
        result.append(item)
        ordered.append(declared)
    if ordered != expected:
        raise ProductCapabilityPolicyError(f"{key} must exactly match catalog canonical order")
    return result


def _as_scope(value: object, key: str) -> Scope:
    if value not in _SCOPES:
        raise ProductCapabilityPolicyError(f"{key} scope is invalid")
    return cast(Scope, value)


def _as_audience(value: object, key: str) -> Audience:
    if value not in _AUDIENCES:
        raise ProductCapabilityPolicyError(f"{key} audience is invalid")
    return cast(Audience, value)


def _as_string(value: object, key: str) -> str:
    if not isinstance(value, str) or not value:
        raise ProductCapabilityPolicyError(f"{key} is invalid")
    return value


def _as_owner(value: str) -> CurrentOwner:
    if value not in _OWNERS:
        raise ProductCapabilityPolicyError("inventory join has an unknown current owner")
    return value


def _as_rpc_effect(value: str) -> RpcEffect:
    if value not in {"read", "write"}:
        raise ProductCapabilityPolicyError("inventory join has an invalid RPC effect")
    return cast(RpcEffect, value)


def _as_event_effect(value: str) -> EventEffect:
    if value != "notification":
        raise ProductCapabilityPolicyError("inventory join has an invalid event effect")
    return "notification"


def build_manifest(policy_path: Path = POLICY) -> ProductCapabilityManifest:
    policy = _load(policy_path)
    if set(policy) != _ROOT_FIELDS:
        raise ProductCapabilityPolicyError("policy has unknown or missing fields")
    if policy.get("contractVersion") != "2.0":
        raise ProductCapabilityPolicyError("policy contractVersion must be 2.0")
    catalog = _load(CATALOG)
    raw_methods = catalog.get("rpcMethods")
    raw_topics = catalog.get("eventTopics")
    if not isinstance(raw_methods, list) or not all(
        isinstance(value, str) for value in raw_methods
    ):
        raise ProductCapabilityPolicyError("Product catalog rpcMethods is malformed")
    if not isinstance(raw_topics, list) or not all(isinstance(value, str) for value in raw_topics):
        raise ProductCapabilityPolicyError("Product catalog eventTopics is malformed")
    methods = cast(list[str], raw_methods)
    topics = cast(list[str], raw_topics)
    rpc_policy = _policy_entries(policy, key="rpcMethods", name="method", expected=methods)
    event_policy = _policy_entries(policy, key="eventTopics", name="topic", expected=topics)
    try:
        inventory = load_product_runtime_inventory()
    except ProductRuntimeInventoryError as error:
        raise ProductCapabilityPolicyError(f"inventory join failed: {error.code}") from error
    rpc_entries: list[ManifestRpcEntry] = []
    for method, source in zip(methods, rpc_policy, strict=True):
        record = inventory.require("rpc", method)
        rpc_entries.append(
            {
                "method": method,
                "scope": _as_scope(source["scope"], "rpcMethods"),
                "audience": _as_audience(source["audience"], "rpcMethods"),
                "capabilityId": _as_string(source["capabilityId"], "rpcMethods capabilityId"),
                "owner": _as_owner(record.current_route),
                "effect": _as_rpc_effect(record.effect),
            }
        )
    event_entries: list[ManifestEventEntry] = []
    for topic, source in zip(topics, event_policy, strict=True):
        record = inventory.require("event", topic)
        event_entries.append(
            {
                "topic": topic,
                "scope": _as_scope(source["scope"], "eventTopics"),
                "audience": _as_audience(source["audience"], "eventTopics"),
                "capabilityId": _as_string(source["capabilityId"], "eventTopics capabilityId"),
                "owner": _as_owner(record.current_route),
                "effect": _as_event_effect(record.effect),
            }
        )
    return {
        "contractVersion": "2.0",
        "rpcMethods": rpc_entries,
        "eventTopics": event_entries,
    }


def _json(manifest: ProductCapabilityManifest) -> str:
    return json.dumps(manifest, ensure_ascii=False, indent=2) + "\n"


def _schema(manifest: ProductCapabilityManifest) -> str:
    def entry(name: str, effect: list[str]) -> dict[str, object]:
        return {
            "type": "object",
            "additionalProperties": False,
            "required": [name, "scope", "audience", "capabilityId", "owner", "effect"],
            "properties": {
                name: {"type": "string", "minLength": 1},
                "scope": {"enum": sorted(_SCOPES)},
                "audience": {"enum": sorted(_AUDIENCES)},
                "capabilityId": {"type": "string", "minLength": 1},
                "owner": {"enum": list(_OWNERS)},
                "effect": {"enum": effect},
            },
        }

    schema = {
        "$schema": "https://json-schema.org/draft/2020-12/schema",
        "$id": "https://vibetable.local/contracts/v2/product-rpc-capability-manifest.schema.json",
        "title": "VibeTable Product RPC Capability Manifest",
        "type": "object",
        "additionalProperties": False,
        "required": ["contractVersion", "rpcMethods", "eventTopics"],
        "properties": {
            "contractVersion": {"const": "2.0"},
            "rpcMethods": {
                "type": "array",
                "minItems": len(manifest["rpcMethods"]),
                "maxItems": len(manifest["rpcMethods"]),
                "uniqueItems": True,
                "items": entry("method", ["read", "write"]),
            },
            "eventTopics": {
                "type": "array",
                "minItems": len(manifest["eventTopics"]),
                "maxItems": len(manifest["eventTopics"]),
                "uniqueItems": True,
                "items": entry("topic", ["notification"]),
            },
        },
    }
    return json.dumps(schema, ensure_ascii=False, indent=2) + "\n"


def _typescript(manifest: ProductCapabilityManifest) -> str:
    methods = [
        item["method"] for item in manifest["rpcMethods"] if item["audience"] == "rendererPublic"
    ]
    return (
        "// Generated by contracts/v2/product_rpc_capability_policy.py.\n"
        "// Do not edit by hand.\n"
        "export const PRODUCT_RPC_PUBLIC_METHODS = "
        + json.dumps(methods, ensure_ascii=False, indent=2)
        + " as const;\n\nexport type ProductRpcMethod =\n"
        "  (typeof PRODUCT_RPC_PUBLIC_METHODS)[number];\n"
    )


def _python(manifest: ProductCapabilityManifest) -> str:
    lines = [
        "# Generated by contracts/v2/product_rpc_capability_policy.py.",
        "# Do not edit by hand.",
        "",
        "from typing import Literal",
        "",
        'CurrentOwner = Literal["pythonBff", "goSidecar", "wpfHost", "pythonWorker"]',
        "",
    ]
    lines.append("PRODUCT_RPC_METHODS_BY_CURRENT_OWNER = {")
    for owner in _OWNERS:
        values = [item["method"] for item in manifest["rpcMethods"] if item["owner"] == owner]
        if not values:
            lines.append(f'    "{owner}": (),')
            continue
        if len(values) == 1:
            lines.append(f'    "{owner}": ("{values[0]}",),')
            continue
        lines.append(f'    "{owner}": (')
        lines.extend(f'        "{value}",' for value in values)
        lines.append("    ),")
    lines.extend(["}", "", "PRODUCT_EVENT_TOPICS_BY_CURRENT_OWNER = {"])
    for owner in _OWNERS:
        values = [item["topic"] for item in manifest["eventTopics"] if item["owner"] == owner]
        if not values:
            lines.append(f'    "{owner}": (),')
            continue
        lines.append(f'    "{owner}": (')
        lines.extend(f'        "{value}",' for value in values)
        lines.append("    ),")
    lines.extend(["}", "", ""])
    lines.extend(
        [
            "def current_owner_methods(owner: CurrentOwner) -> tuple[str, ...]:",
            "    try:",
            "        return PRODUCT_RPC_METHODS_BY_CURRENT_OWNER[owner]",
            "    except KeyError as error:",
            '        raise ValueError(f"unknown current owner: {owner}") from error',
            "",
            "",
            "def current_owner_topics(owner: CurrentOwner) -> tuple[str, ...]:",
            "    try:",
            "        return PRODUCT_EVENT_TOPICS_BY_CURRENT_OWNER[owner]",
            "    except KeyError as error:",
            '        raise ValueError(f"unknown current owner: {owner}") from error',
            "",
        ]
    )
    return "\n".join(lines)


def _go(manifest: ProductCapabilityManifest) -> str:
    def rpc_values(owner: CurrentOwner) -> str:
        return ", ".join(
            json.dumps(item["method"]) for item in manifest["rpcMethods"] if item["owner"] == owner
        )

    def topic_values(owner: CurrentOwner) -> str:
        return ", ".join(
            json.dumps(item["topic"]) for item in manifest["eventTopics"] if item["owner"] == owner
        )

    def go_name(owner: str) -> str:
        return owner[0].upper() + owner[1:]

    scope_names = {"global": "GlobalScope", "workspace": "WorkspaceScope"}
    audience_names = {
        "rendererPublic": "RendererPublic",
        "rendererInternal": "RendererInternal",
        "hostOnly": "HostOnly",
    }
    effect_names = {"read": "ReadEffect", "write": "WriteEffect"}

    lines = [
        "// Code generated by contracts/v2/product_rpc_capability_policy.py; DO NOT EDIT.",
        "package productcapabilities",
        "",
        "type CurrentOwner string",
        "",
        "type Scope string",
        "",
        "const (",
        '\tGlobalScope    Scope = "global"',
        '\tWorkspaceScope Scope = "workspace"',
        ")",
        "",
        "type Audience string",
        "",
        "const (",
        '\tRendererPublic   Audience = "rendererPublic"',
        '\tRendererInternal Audience = "rendererInternal"',
        '\tHostOnly         Audience = "hostOnly"',
        ")",
        "",
        "type Effect string",
        "",
        "const (",
        '\tReadEffect  Effect = "read"',
        '\tWriteEffect Effect = "write"',
        ")",
        "",
        "const (",
    ]
    lines.extend(f'\t{go_name(owner):<12} CurrentOwner = "{owner}"' for owner in _OWNERS)
    lines.extend(
        [
            ")",
            "",
            "type RPCDescriptor struct {",
            '\tMethod       string       `json:"method"`',
            '\tScope        Scope        `json:"scope"`',
            '\tAudience     Audience     `json:"audience"`',
            '\tCapabilityID string       `json:"capabilityId"`',
            '\tOwner        CurrentOwner `json:"owner"`',
            '\tEffect       Effect       `json:"effect"`',
            "}",
            "",
            "var rpcDescriptors = []RPCDescriptor{",
        ]
    )
    for item in manifest["rpcMethods"]:
        lines.append(
            "\t{"
            f"Method: {json.dumps(item['method'])}, "
            f"Scope: {scope_names[item['scope']]}, "
            f"Audience: {audience_names[item['audience']]}, "
            f"CapabilityID: {json.dumps(item['capabilityId'])}, "
            f"Owner: {go_name(item['owner'])}, "
            f"Effect: {effect_names[item['effect']]}"
            "},"
        )
    lines.extend(["}", "", "var rpcMethods = map[CurrentOwner]map[string]struct{}{"])
    for owner in _OWNERS:
        method_values = rpc_values(owner)
        name = go_name(owner)
        lines.append(f"\t{name}:{' ' * (13 - len(name))}set({method_values}),")
    lines.extend(["}", "", "var eventTopics = map[CurrentOwner]map[string]struct{}{"])
    for owner in _OWNERS:
        event_values = topic_values(owner)
        name = go_name(owner)
        lines.append(f"\t{name}:{' ' * (13 - len(name))}set({event_values}),")
    lines.extend(
        [
            "}",
            "",
            "func RPCDescriptors() []RPCDescriptor {",
            "\treturn append([]RPCDescriptor(nil), rpcDescriptors...)",
            "}",
            "",
            "func CurrentOwnerRPCDescriptors(owner CurrentOwner) []RPCDescriptor {",
            "\tresult := make([]RPCDescriptor, 0)",
            "\tfor _, descriptor := range rpcDescriptors {",
            "\t\tif descriptor.Owner == owner {",
            "\t\t\tresult = append(result, descriptor)",
            "\t\t}",
            "\t}",
            "\treturn result",
            "}",
            "",
            "func HasCurrentOwnerRPCMethod(owner CurrentOwner, method string) bool {",
            "\t_, ok := rpcMethods[owner][method]",
            "\treturn ok",
            "}",
            "",
            "func HasCurrentOwnerEventTopic(owner CurrentOwner, topic string) bool {",
            "\t_, ok := eventTopics[owner][topic]",
            "\treturn ok",
            "}",
            "",
            "func set(values ...string) map[string]struct{} {",
            "\tresult := make(map[string]struct{}, len(values))",
            "\tfor _, value := range values {",
            "\t\tresult[value] = struct{}{}",
            "\t}",
            "\treturn result",
            "}",
            "",
        ]
    )
    return "\n".join(lines)


def outputs(manifest: ProductCapabilityManifest) -> dict[Path, str]:
    return {
        MANIFEST: _json(manifest),
        MANIFEST_SCHEMA: _schema(manifest),
        TS_OUTPUT: _typescript(manifest),
        PYTHON_OUTPUT: _python(manifest),
        GO_OUTPUT: _go(manifest),
    }


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--check", action="store_true")
    args = parser.parse_args(argv)
    try:
        generated = outputs(build_manifest())
    except (OSError, ProductCapabilityPolicyError) as error:
        print(error)
        return 1
    if args.check:
        stale = [
            str(path.relative_to(ROOT))
            for path, text in generated.items()
            if not path.exists() or path.read_text(encoding="utf-8") != text
        ]
        if stale:
            print(f"Product capability outputs are stale: {stale}")
            return 1
        return 0
    for path, text in generated.items():
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(text, encoding="utf-8", newline="\n")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
