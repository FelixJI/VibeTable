"""Generate bridge boundary projections over existing Product/Workspace authorities.

The supplement owns only missing bridge names and per-side boundary policy.
Product method/topic references resolve through the existing manifest builder.
Workspace method names remain reserved to their existing registry/policy builder.
New public authority entries are never implicitly added to a bridge projection.
Router flags describe static seeds; existing typed registry unions and runtime guards remain in the router.
"""

from __future__ import annotations

import argparse
import json
import re
from pathlib import Path
from typing import Any

try:
    from contracts.v2.app_settings_contract import build_contract as build_app_settings_contract
    from contracts.v2.generate_workspace_rpc_capability_manifest import (
        build_manifest as build_workspace_manifest,
    )
    from contracts.v2.product_rpc_capability_policy import (
        POLICY as PRODUCT_POLICY,
    )
    from contracts.v2.product_rpc_capability_policy import (
        build_manifest as build_product_manifest,
    )
except ModuleNotFoundError:  # pragma: no cover - direct script execution
    from app_settings_contract import build_contract as build_app_settings_contract
    from generate_workspace_rpc_capability_manifest import (
        build_manifest as build_workspace_manifest,
    )
    from product_rpc_capability_policy import (
        POLICY as PRODUCT_POLICY,
    )
    from product_rpc_capability_policy import (
        build_manifest as build_product_manifest,
    )

ROOT = Path(__file__).parents[2]
CATALOG = Path(__file__).with_name("host-bridge-message-catalog.json")
CS_OUTPUT = ROOT / "desktop/src/VibeTable.Contracts/Generated/HostBridgeCatalog.g.cs"
TS_OUTPUT = ROOT / "desktop/web-grid/src/contracts/generated/hostBridgeCatalog.ts"

FAMILIES = (
    "core",
    "clipboard",
    "releaseUpdate",
    "runtimeDiagnostics",
    "preservation",
    "workspaceV2",
)
STATUSES = ("active", "rendererInternalOnly", "retired")
FAMILY_TS_CONSTS = {
    "clipboard": ("CLIPBOARD_WEB_MESSAGE_TYPES", "CLIPBOARD_HOST_MESSAGE_TYPES"),
    "releaseUpdate": ("RELEASE_UPDATE_WEB_MESSAGE_TYPES", "RELEASE_UPDATE_HOST_MESSAGE_TYPES"),
    "runtimeDiagnostics": (
        "RUNTIME_DIAGNOSTICS_WEB_MESSAGE_TYPES",
        "RUNTIME_DIAGNOSTICS_HOST_MESSAGE_TYPES",
    ),
    "preservation": ("PRESERVATION_WEB_MESSAGE_TYPES", "PRESERVATION_HOST_MESSAGE_TYPES"),
    "workspaceV2": ("WORKSPACE_V2_WEB_MESSAGE_TYPES", "WORKSPACE_V2_HOST_MESSAGE_TYPES"),
}

_ROOT_FIELDS = frozenset({"contractVersion", "notes", "messages"})
_ENTRY_FIELDS = frozenset(
    {"family", "status", "routerWeb", "routerHost", "bridgeWeb", "bridgeHost"}
)
_ENTRY_OPTIONAL_FIELDS = frozenset({"name", "reference", "bootstrap", "responses"})
_FLAG_FIELDS = ("routerWeb", "routerHost", "bridgeWeb", "bridgeHost")


class HostBridgeCatalogError(ValueError):
    """Stable fail-closed error from the bridge catalog seam."""


def _closed_object(pairs: list[tuple[str, Any]]) -> dict[str, Any]:
    result: dict[str, Any] = {}
    for key, value in pairs:
        if key in result:
            raise HostBridgeCatalogError(f"duplicate JSON property: {key}")
        result[key] = value
    return result


def _is_message_name(value: object) -> bool:
    return (
        isinstance(value, str)
        and re.fullmatch(r"[a-z][a-zA-Z0-9]*(\.[a-zA-Z][a-zA-Z0-9]*)+", value) is not None
    )


def _load(path: Path) -> dict[str, Any]:
    try:
        source = json.loads(path.read_text(encoding="utf-8"), object_pairs_hook=_closed_object)
    except json.JSONDecodeError as error:
        raise HostBridgeCatalogError(f"invalid JSON: {error.msg}") from error
    if not isinstance(source, dict):
        raise HostBridgeCatalogError("catalog must be an object")
    return source


def validate_catalog(
    catalog: dict[str, Any],
    *,
    product_policy_path: Path = PRODUCT_POLICY,
) -> list[dict[str, Any]]:
    # Resolve the reviewed authorities themselves, never C# source or a second catalog.
    try:
        product = build_product_manifest(product_policy_path)
        workspace = build_workspace_manifest()
        settings = build_app_settings_contract()
    except ValueError as error:
        raise HostBridgeCatalogError(f"authority resolution failed: {error}") from error
    authorities = {
        ("product", "method"): {m["method"]: m for m in product["rpcMethods"]},
        ("product", "topic"): {m["topic"]: m for m in product["eventTopics"]},
    }
    owned_names = (
        {name for entries in authorities.values() for name in entries}
        | {m["method"] for m in workspace["methods"]}
        | {m["method"] for m in settings["methods"]}
        | {m["topic"] for m in settings["notifications"]}
    )

    if set(catalog) - _ROOT_FIELDS:
        raise HostBridgeCatalogError("catalog has unknown root keys")
    if catalog.get("contractVersion") != "1.0":
        raise HostBridgeCatalogError("contractVersion must be '1.0'")
    messages = catalog.get("messages")
    if not isinstance(messages, list) or not messages:
        raise HostBridgeCatalogError("messages must be a non-empty list")
    resolved: list[dict[str, Any]] = []
    names: set[str] = set()
    bootstraps: list[str] = []
    for entry in messages:
        if not isinstance(entry, dict):
            raise HostBridgeCatalogError("message entry must be an object")
        unknown = set(entry) - _ENTRY_FIELDS - _ENTRY_OPTIONAL_FIELDS
        missing = _ENTRY_FIELDS - set(entry)
        if unknown or missing:
            raise HostBridgeCatalogError(
                f"message entry has unknown/missing keys: {sorted(unknown or missing)}"
            )
        if ("name" in entry) == ("reference" in entry):
            raise HostBridgeCatalogError("message requires exactly one name or authority reference")
        if "reference" in entry:
            reference = entry["reference"]
            if not isinstance(reference, dict):
                raise HostBridgeCatalogError("authority reference must be an object")
            kind = "method" if "method" in reference else "topic"
            if set(reference) != {"source", kind}:
                raise HostBridgeCatalogError("authority reference has unknown or missing keys")
            name = reference[kind]
            source = reference["source"]
            if not isinstance(source, str) or not _is_message_name(name):
                raise HostBridgeCatalogError("invalid authority reference")
            authority = authorities.get((source, kind), {}).get(name)
            if authority is None:
                raise HostBridgeCatalogError(
                    f"unresolved authority reference: {source}/{kind}/{name}"
                )
            # A renderer-public reference may never survive an audience downgrade.
            # Internal refusal entries remain explicit boundary policy, not capabilities.
            required_audience = (
                "rendererInternal"
                if entry["status"] == "rendererInternalOnly"
                else "rendererPublic"
            )
            if authority["audience"] != required_audience:
                raise HostBridgeCatalogError(
                    f"{name}: authority audience is not {required_audience}"
                )
        else:
            name = entry["name"]
            if isinstance(name, str) and name in owned_names:
                raise HostBridgeCatalogError(f"{name}: existing authority requires a reference")
        if not _is_message_name(name) or name in names:
            raise HostBridgeCatalogError(f"message name is invalid or duplicated: {name!r}")
        names.add(name)
        entry = {key: value for key, value in entry.items() if key != "reference"} | {"name": name}
        resolved.append(entry)
        if entry["family"] not in FAMILIES:
            raise HostBridgeCatalogError(f"{name}: unknown family {entry['family']!r}")
        if entry["status"] not in STATUSES:
            raise HostBridgeCatalogError(f"{name}: unknown status {entry['status']!r}")
        for flag in _FLAG_FIELDS:
            if not isinstance(entry[flag], bool):
                raise HostBridgeCatalogError(f"{name}: flag {flag} must be a boolean")
        if entry["status"] == "retired" and any(entry[flag] for flag in _FLAG_FIELDS):
            raise HostBridgeCatalogError(f"{name}: retired messages must not be admitted anywhere")
        if entry["status"] == "rendererInternalOnly" and not entry["routerWeb"]:
            raise HostBridgeCatalogError(
                f"{name}: renderer-internal requests stay router-admitted so the "
                "stable CAPABILITY_NOT_PUBLIC refusal keeps working"
            )
        if entry.get("bootstrap"):
            if entry["status"] != "active" or not entry["routerWeb"] or not entry["bridgeWeb"]:
                raise HostBridgeCatalogError(f"{name}: bootstrap must be an active web request")
            bootstraps.append(name)
        responses = entry.get("responses")
        if responses is not None:
            if (
                not isinstance(responses, list)
                or not responses
                or not all(isinstance(item, str) and item for item in responses)
            ):
                raise HostBridgeCatalogError(f"{name}: responses must be a non-empty string list")
            if not entry["bridgeWeb"]:
                raise HostBridgeCatalogError(f"{name}: responses only apply to web requests")
            if len(set(responses)) != len(responses):
                raise HostBridgeCatalogError(f"{name}: duplicate responses")
    if len(bootstraps) != 1:
        raise HostBridgeCatalogError("exactly one bootstrap message (app.ready) is required")
    for entry in resolved:
        for response in entry.get("responses", []):
            if response not in names:
                raise HostBridgeCatalogError(
                    f"{entry['name']}: response {response!r} is not in the catalog"
                )
            if response in bootstraps:
                raise HostBridgeCatalogError(f"{entry['name']}: bootstrap cannot be a response")
    return resolved


def build_catalog(
    path: Path = CATALOG,
    *,
    product_policy_path: Path = PRODUCT_POLICY,
) -> dict[str, Any]:
    catalog = _load(path)
    validate_catalog(
        catalog,
        product_policy_path=product_policy_path,
    )
    return catalog


def _active(messages: list[dict[str, Any]], flag: str) -> list[str]:
    return [m["name"] for m in messages if m["status"] != "retired" and m[flag]]


def render_csharp(catalog: dict[str, Any]) -> str:
    messages = validate_catalog(catalog)

    def array(names: list[str]) -> str:
        body = "".join(f'\n        "{name}",' for name in names)
        return f"[{body}\n    ]"

    retired = [m["name"] for m in messages if m["status"] == "retired"]
    internal = [m["name"] for m in messages if m["status"] == "rendererInternalOnly"]
    bootstrap = next(m["name"] for m in messages if m.get("bootstrap"))
    lines = [
        "// Generated from contracts/v2/host-bridge-message-catalog.json; do not edit.",
        "#nullable enable",
        "",
        "namespace VibeTable.Contracts;",
        "",
        "/// <summary>Bridge boundary projections resolve Product method/topic references",
        "/// through their existing authorities. Router arrays are static seeds;",
        "/// typed Product/relation registries and app-settings projections are",
        "/// composed separately. Retired names retain CAPABILITY_RETIRED refusal.</summary>",
        "public static class HostBridgeCatalog",
        "{",
        f'    public const string BootstrapRequestType = "{bootstrap}";',
        "",
        "    /// <summary>Web -> host static seed (before typed registry composition).</summary>",
        f"    public static readonly string[] RouterWebRequestTypes = {array(_active(messages, 'routerWeb'))};",
        "",
        "    /// <summary>Host -> web static seed (before typed registry composition).</summary>",
        f"    public static readonly string[] RouterHostNotificationTypes = {array(_active(messages, 'routerHost'))};",
        "",
        "    /// <summary>Router-admitted names that are not public renderer",
        "    /// capabilities (rejected with CAPABILITY_NOT_PUBLIC).</summary>",
        f"    public static readonly string[] RendererInternalOnlyRequestTypes = {array(internal)};",
        "",
        "    /// <summary>Retired visual-layout builder requests (rejected with",
        "    /// CAPABILITY_RETIRED).</summary>",
        f"    public static readonly string[] RetiredRequestTypes = {array(retired)};",
        "}",
        "",
    ]
    return "\n".join(lines)


def render_typescript(catalog: dict[str, Any]) -> str:
    messages = validate_catalog(catalog)
    lines: list[str] = [
        "// Generated from contracts/v2/host-bridge-message-catalog.json. Do not edit by hand.",
        "",
    ]

    def emit_const(name: str, names: list[str], comment: list[str]) -> None:
        for line in comment:
            lines.append(f"// {line}")
        lines.append(f"export const {name} = [")
        lines.extend(f'  "{value}",' for value in names)
        lines.append("] as const;")
        lines.append("")

    emit_const(
        "HOST_BRIDGE_WEB_MESSAGE_TYPES",
        _active(messages, "bridgeWeb"),
        [
            "All non-retired web -> host names this bridge may produce",
            "(appPreferences names come from the app-settings contract).",
        ],
    )
    emit_const(
        "HOST_BRIDGE_HOST_MESSAGE_TYPES",
        _active(messages, "bridgeHost"),
        [
            "All non-retired host -> web names this bridge accepts",
            "(appearance.changed comes from the app-settings contract).",
        ],
    )
    lines.extend(
        [
            "// Correlated reply names for requests whose responses use a",
            "// different type; every other closed RPC replies with its own",
            "// request type.",
            "export const HOST_BRIDGE_RESPONSE_TYPE_OVERRIDES = {",
        ]
    )
    for entry in messages:
        responses = entry.get("responses")
        if responses and entry["status"] != "retired":
            values = ", ".join(f'"{response}"' for response in responses)
            lines.append(f'  "{entry["name"]}": [{values}],')
    lines.extend(["} as const;", ""])

    for family in FAMILIES:
        if family == "core":
            continue
        web_const, host_const = FAMILY_TS_CONSTS[family]
        family_entries = [m for m in messages if m["family"] == family and m["status"] != "retired"]
        emit_const(
            web_const,
            [m["name"] for m in family_entries if m["bridgeWeb"]],
            [f"{family} family web -> host names."],
        )
        emit_const(
            host_const,
            [m["name"] for m in family_entries if m["bridgeHost"]],
            [f"{family} family host -> web names."],
        )
    return "\n".join(lines)


def outputs(catalog: dict[str, Any]) -> dict[Path, str]:
    return {
        CS_OUTPUT: render_csharp(catalog),
        TS_OUTPUT: render_typescript(catalog),
    }


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--check", action="store_true")
    args = parser.parse_args(argv)
    try:
        generated = outputs(build_catalog())
    except (OSError, HostBridgeCatalogError) as error:
        print(error)
        return 1
    if args.check:
        stale = [
            str(path.relative_to(ROOT))
            for path, text in generated.items()
            if not path.exists() or path.read_text(encoding="utf-8") != text
        ]
        if stale:
            print(f"Host bridge catalog outputs are stale: {stale}")
            return 1
        return 0
    for path, text in generated.items():
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(text, encoding="utf-8", newline="\n")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
