"""Generate the app-settings bridge wire adapters (TypeScript + C#).

Single executable authority: contracts/v2/app-settings-contract.json. The
renderer emits the TS contract module consumed by the real web bridge and the
C# wire types + patch reader consumed by the desktop router/controller. The
conformance fixture (fixtures/app-settings-conformance.json) drives both
sides' tests from the same cases.
"""

from __future__ import annotations

import argparse
import json
from pathlib import Path
from typing import Any

ROOT = Path(__file__).parents[2]
CONTRACT = Path(__file__).with_name("app-settings-contract.json")
FIXTURE = Path(__file__).with_name("fixtures") / "app-settings-conformance.json"
CS_OUTPUT = ROOT / "desktop/src/VibeTable.Contracts/Generated/AppSettingsContracts.g.cs"
TS_OUTPUT = ROOT / "desktop/web-grid/src/contracts/appPreferencesContracts.ts"

TS_TYPE_NAMES = {
    "themePreference": "ThemePreference",
    "updateProxy": "UpdateProxyId",
    "effectiveBaseTheme": "EffectiveBaseTheme",
}
TS_ARRAY_NAMES = {
    "themePreference": "THEME_PREFERENCES",
    "updateProxy": "UPDATE_PROXY_IDS",
}
CS_ENUM_CLASSES = {
    "themePreference": "AppSettingsThemeOption",
    "updateProxy": "AppSettingsUpdateProxyOption",
}

_ROOT_FIELDS = frozenset(
    {
        "contractVersion",
        "methods",
        "notifications",
        "enums",
        "preferencesResult",
        "updatePatch",
        "appearanceChanged",
        "operationFailed",
        "errorCodes",
    }
)
_METHOD_FIELDS = frozenset({"method", "direction", "response", "request"})
_NOTIFICATION_FIELDS = frozenset({"topic", "direction"})
_REQUEST_FIELDS = frozenset({"shape"})
_RESULT_ROOT_FIELDS = frozenset({"fields"})
_PATCH_ROOT_FIELDS = frozenset(
    {"atLeastOneField", "fields", "mutexGroups", "exclusiveFields", "notes"}
)
_FIELD_KEYS_BOOL = frozenset({"name", "type", "optional"})
_FIELD_KEYS_ENUM = frozenset({"name", "type", "enum", "optional", "nullable"})
_FIELD_KEYS_STRING = frozenset({"name", "type", "optional", "maxLengthUtf16Units"})
_NOTE_KEYS_OPTIONAL = frozenset({"notes"})
_ERROR_FIELDS = frozenset({"code", "when"})
_FIELD_TYPES = frozenset({"boolean", "enum", "string"})

PATCH_HAS_FLAG_FIELDS = ("customUpdateProxyUrl", "theme", "adoptLegacyTheme")


class AppSettingsContractError(ValueError):
    """Stable fail-closed error from the app-settings contract seam."""


def _closed_object(pairs: list[tuple[str, Any]]) -> dict[str, Any]:
    result: dict[str, Any] = {}
    for key, value in pairs:
        if key in result:
            raise AppSettingsContractError(f"duplicate JSON property: {key}")
        result[key] = value
    return result


def _load(path: Path) -> dict[str, Any]:
    try:
        source = json.loads(path.read_text(encoding="utf-8"), object_pairs_hook=_closed_object)
    except json.JSONDecodeError as error:
        raise AppSettingsContractError(f"invalid JSON: {error.msg}") from error
    if not isinstance(source, dict):
        raise AppSettingsContractError("contract must be an object")
    return source


def _require_keys(entry: dict[str, Any], allowed: frozenset[str], label: str) -> None:
    unknown = set(entry) - allowed
    if unknown:
        raise AppSettingsContractError(f"{label} has unknown keys: {sorted(unknown)}")


def _is_camel_name(value: object, *, allow_dot: bool = False) -> bool:
    if (
        not isinstance(value, str)
        or value == ""
        or not value[0].isalpha()
        or not value[0].islower()
        or value.startswith(".")
        or value.endswith(".")
    ):
        return False

    def _segment_ok(segment: str) -> bool:
        return segment != "" and all(char.isalnum() for char in segment)

    if allow_dot:
        return all(_segment_ok(segment) for segment in value.split("."))
    return _segment_ok(value)


def validate_contract(contract: dict[str, Any]) -> None:
    _require_keys(contract, _ROOT_FIELDS, "contract")
    if contract.get("contractVersion") != "1.0":
        raise AppSettingsContractError("contractVersion must be '1.0'")

    enums = contract.get("enums")
    if not isinstance(enums, dict) or set(enums) != set(TS_TYPE_NAMES):
        raise AppSettingsContractError("enums must define exactly the known enum families")
    for family, definition in enums.items():
        if not isinstance(definition, dict) or set(definition) != {"values"}:
            raise AppSettingsContractError(f"enum {family} must only define values")
        values = definition["values"]
        if (
            not isinstance(values, list)
            or not values
            or not all(isinstance(v, str) and v for v in values)
        ):
            raise AppSettingsContractError(f"enum {family} values are invalid")
        if len(set(values)) != len(values):
            raise AppSettingsContractError(f"enum {family} has duplicate values")

    methods = contract.get("methods")
    if not isinstance(methods, list) or len(methods) != 2:
        raise AppSettingsContractError("methods must list get and update")
    seen_methods: list[str] = []
    for method in methods:
        if not isinstance(method, dict):
            raise AppSettingsContractError("method entry must be an object")
        _require_keys(method, _METHOD_FIELDS, "method entry")
        name = method.get("method")
        if not _is_camel_name(name, allow_dot=True) or "." not in name:
            raise AppSettingsContractError(f"method name is invalid: {name!r}")
        if method.get("direction") != "webToHost" or method.get("response") != "sameType":
            raise AppSettingsContractError(f"method {name} must be a webToHost sameType RPC")
        request = method.get("request")
        if not isinstance(request, dict) or set(request) != _REQUEST_FIELDS:
            raise AppSettingsContractError(f"method {name} request must only define shape")
        if request.get("shape") not in {"emptyObject", "patch"}:
            raise AppSettingsContractError(f"method {name} request shape is invalid")
        seen_methods.append(name)
    if seen_methods != ["appPreferences.get", "appPreferences.update"]:
        raise AppSettingsContractError(
            "methods must be exactly appPreferences.get and appPreferences.update"
        )
    if methods[0]["request"]["shape"] != "emptyObject" or methods[1]["request"]["shape"] != "patch":
        raise AppSettingsContractError("get must be emptyObject and update must be patch")

    notifications = contract.get("notifications")
    if not isinstance(notifications, list) or len(notifications) != 1:
        raise AppSettingsContractError("notifications must list exactly appearance.changed")
    topic = notifications[0]
    if not isinstance(topic, dict):
        raise AppSettingsContractError("notification entry must be an object")
    _require_keys(topic, _NOTIFICATION_FIELDS, "notification entry")
    if topic.get("topic") != "appearance.changed" or topic.get("direction") != "hostToWeb":
        raise AppSettingsContractError("the only notification must be hostToWeb appearance.changed")

    for section in ("preferencesResult", "appearanceChanged"):
        root = contract.get(section)
        if not isinstance(root, dict) or set(root) != _RESULT_ROOT_FIELDS:
            raise AppSettingsContractError(f"{section} must only define fields")
        _validate_fields(root["fields"], section, allow_nullable=True)

    patch = contract.get("updatePatch")
    if not isinstance(patch, dict):
        raise AppSettingsContractError("updatePatch must be an object")
    _require_keys(patch, _PATCH_ROOT_FIELDS, "updatePatch")
    if patch.get("atLeastOneField") is not True:
        raise AppSettingsContractError("updatePatch must require at least one field")
    fields = patch["fields"]
    _validate_fields(fields, "updatePatch", allow_nullable=False)
    if not isinstance(patch.get("mutexGroups"), list) or not patch["mutexGroups"]:
        raise AppSettingsContractError("updatePatch must define mutexGroups")
    names = [field["name"] for field in fields]
    for group in patch["mutexGroups"]:
        if (
            not isinstance(group, dict)
            or set(group) != {"fields"}
            or not isinstance(group["fields"], list)
        ):
            raise AppSettingsContractError("mutex group must only define fields")
        if len(group["fields"]) < 2 or any(member not in names for member in group["fields"]):
            raise AppSettingsContractError("mutex group must reference at least two known fields")
    exclusive = patch.get("exclusiveFields")
    if (
        not isinstance(exclusive, list)
        or not exclusive
        or any(member not in names for member in exclusive)
    ):
        raise AppSettingsContractError("exclusiveFields must reference known fields")
    for member in exclusive:
        for group in patch["mutexGroups"]:
            if member in group["fields"] and len(group["fields"]) != 2:
                raise AppSettingsContractError("exclusive member mutex groups must be pairs")

    codes = contract.get("errorCodes")
    if not isinstance(codes, list) or not codes:
        raise AppSettingsContractError("errorCodes must be a non-empty list")
    seen_codes: set[str] = set()
    for entry in codes:
        if not isinstance(entry, dict):
            raise AppSettingsContractError("error entry must be an object")
        _require_keys(entry, _ERROR_FIELDS, "error entry")
        code = entry.get("code")
        if not isinstance(code, str) or not code or code in seen_codes:
            raise AppSettingsContractError(f"error code is invalid or duplicated: {code!r}")
        if not isinstance(entry.get("when"), str):
            raise AppSettingsContractError(f"error {code} must describe when")
        seen_codes.add(code)


def _validate_fields(fields: object, label: str, *, allow_nullable: bool) -> None:
    if not isinstance(fields, list) or not fields:
        raise AppSettingsContractError(f"{label} fields must be a non-empty list")
    seen: set[str] = set()
    for field in fields:
        if not isinstance(field, dict):
            raise AppSettingsContractError(f"{label} field must be an object")
        kind = field.get("type")
        if kind not in _FIELD_TYPES:
            raise AppSettingsContractError(f"{label} field type is invalid: {kind!r}")
        if kind == "enum":
            allowed = _FIELD_KEYS_ENUM
        elif kind == "string":
            allowed = _FIELD_KEYS_STRING
        else:
            allowed = _FIELD_KEYS_BOOL
        _require_keys(field, allowed | _NOTE_KEYS_OPTIONAL, f"{label} field")
        name = field.get("name")
        if not _is_camel_name(name) or name in seen:
            raise AppSettingsContractError(f"{label} field name is invalid or duplicated: {name!r}")
        seen.add(name)
        if not isinstance(field.get("optional"), bool):
            raise AppSettingsContractError(f"{label} field {name} optional must be a boolean")
        if kind == "enum":
            if field.get("enum") not in TS_TYPE_NAMES:
                raise AppSettingsContractError(f"{label} field {name} references an unknown enum")
            nullable = field.get("nullable", False)
            if not isinstance(nullable, bool):
                raise AppSettingsContractError(f"{label} field {name} nullable must be a boolean")
            if nullable and not allow_nullable:
                raise AppSettingsContractError(f"{label} field {name} cannot be nullable here")
            if nullable and field.get("optional"):
                raise AppSettingsContractError(
                    f"{label} field {name} cannot be both nullable and optional"
                )
        if kind == "string":
            nullable = field.get("nullable", False)
            if not isinstance(nullable, bool):
                raise AppSettingsContractError(f"{label} field {name} nullable must be a boolean")
            if nullable and field.get("optional") is not True:
                raise AppSettingsContractError(
                    f"{label} field {name}: nullable strings must be optional "
                    "(absent and null both mean unset)"
                )
            limit = field.get("maxLengthUtf16Units")
            if "maxLengthUtf16Units" in field and (
                not isinstance(limit, int) or isinstance(limit, bool) or limit < 1
            ):
                raise AppSettingsContractError(
                    f"{label} field {name} maxLengthUtf16Units is invalid"
                )


def build_contract(path: Path = CONTRACT) -> dict[str, Any]:
    contract = _load(path)
    validate_contract(contract)
    return contract


# ---------------------------------------------------------------------------
# Naming helpers
# ---------------------------------------------------------------------------


def _pascal(name: str) -> str:
    return name[0].upper() + name[1:]


def _const_name(name: str) -> str:
    return name[0].upper() + name[1:]


def _method_const(method: str) -> str:
    return "".join(part[0].upper() + part[1:] for part in method.split("."))


def _enum_const(value: str) -> str:
    return _const_name(value)


# ---------------------------------------------------------------------------
# C# renderer
# ---------------------------------------------------------------------------


def _cs_field_type(field: dict[str, Any]) -> str:
    kind = field["type"]
    if kind == "boolean":
        return "bool"
    if kind == "string":
        return "string?" if field.get("nullable") else "string"
    return "string?" if field.get("nullable") else "string"


def _cs_patch_reader(contract: dict[str, Any]) -> str:
    fields = contract["updatePatch"]["fields"]
    lines: list[str] = []
    lines.append("public static class AppSettingsPatchReader")
    lines.append("{")
    lines.append(
        "    public static bool TryRead(JsonElement payload, out AppSettingsPreferencesPatch? patch)"
    )
    lines.append("    {")
    lines.append("        patch = null;")
    lines.append("        if (payload.ValueKind != JsonValueKind.Object)")
    lines.append("            return false;")
    for field in fields:
        name = field["name"]
        cs_type = "bool?" if field["type"] == "boolean" else "string?"
        lines.append(f"        {cs_type} {name} = null;")
        lines.append(f"        bool saw{_pascal(name)} = false;")
    lines.append("        foreach (JsonProperty property in payload.EnumerateObject())")
    lines.append("        {")
    for field in fields:
        name = field["name"]
        pascal = _pascal(name)
        kind = field["type"]
        if kind == "boolean":
            lines.append(f'            if (property.NameEquals("{name}"))')
            lines.append("            {")
            lines.append(f"                if (saw{pascal} || property.Value.ValueKind is not")
            lines.append("                        (JsonValueKind.True or JsonValueKind.False))")
            lines.append("                    return false;")
            lines.append(f"                saw{pascal} = true;")
            lines.append(f"                {name} = property.Value.GetBoolean();")
            lines.append("                continue;")
            lines.append("            }")
        elif kind == "enum":
            enum_class = CS_ENUM_CLASSES[field["enum"]]
            lines.append(f'            if (property.NameEquals("{name}"))')
            lines.append("            {")
            lines.append(
                f"                if (saw{pascal} || property.Value.ValueKind != JsonValueKind.String)"
            )
            lines.append("                    return false;")
            lines.append(f"                saw{pascal} = true;")
            lines.append(f"                {name} = property.Value.GetString();")
            lines.append(f"                if ({name} is null || !{enum_class}.IsKnown({name}))")
            lines.append("                    return false;")
            lines.append("                continue;")
            lines.append("            }")
        else:
            limit = field.get("maxLengthUtf16Units")
            lines.append(f'            if (property.NameEquals("{name}"))')
            lines.append("            {")
            lines.append(
                f"                if (saw{pascal} || property.Value.ValueKind != JsonValueKind.String)"
            )
            lines.append("                    return false;")
            lines.append(f"                saw{pascal} = true;")
            lines.append(f"                {name} = property.Value.GetString();")
            check = f"{name} is null"
            if isinstance(limit, int):
                check += f" || {name}.Length > {limit}"
            lines.append(f"                if ({check})")
            lines.append("                    return false;")
            lines.append("                continue;")
            lines.append("            }")
    lines.append("            return false;")
    lines.append("        }")
    saw_terms = " && ".join(f"!saw{_pascal(field['name'])}" for field in fields)
    lines.append(f"        if ({saw_terms})")
    lines.append("            return false;")
    for group in contract["updatePatch"]["mutexGroups"]:
        members = " && ".join(f"saw{_pascal(member)}" for member in group["fields"])
        lines.append(f"        if ({members})")
        lines.append("            return false;")
    for member in contract["updatePatch"]["exclusiveFields"]:
        others = [f["name"] for f in fields if f["name"] != member]
        mixed = " || ".join(f"saw{_pascal(other)}" for other in others)
        lines.append(f"        if (saw{_pascal(member)} && ({mixed}))")
        lines.append("            return false;")
    args: list[str] = []
    for field in fields:
        name = field["name"]
        args.append(name)
        if name in PATCH_HAS_FLAG_FIELDS:
            args.append(f"saw{_pascal(name)}")
    lines.append("        patch = new AppSettingsPreferencesPatch(")
    lines.append(f"            {', '.join(args)});")
    lines.append("        return true;")
    lines.append("    }")
    lines.append("}")
    return "\n".join(lines)


def _cs_record(name: str, fields: list[dict[str, Any]], *, wire: bool) -> str:
    params: list[str] = []
    for field in fields:
        cs_type = _cs_field_type(field)
        params.append(
            f'    [property: JsonPropertyName("{field["name"]}")]\n    {cs_type} {_pascal(field["name"])}'
        )
    body = ",\n".join(params)
    return f"public sealed record {name}(\n{body});"


def render_csharp(contract: dict[str, Any]) -> str:
    methods = contract["methods"]
    notifications = contract["notifications"]
    enums = contract["enums"]
    lines: list[str] = []
    lines.append("// Generated from contracts/v2/app-settings-contract.json; do not edit.")
    lines.append("#nullable enable")
    lines.append("")
    lines.append("using System.Text.Json;")
    lines.append("using System.Text.Json.Serialization;")
    lines.append("")
    lines.append("namespace VibeTable.Contracts;")
    lines.append("")
    lines.append("/// <summary>Closed app-settings bridge message names shared by the router and")
    lines.append("/// controllers. WebRequestTypes are admitted web->host requests;")
    lines.append(
        "/// HostNotificationTypes lists correlated responses plus the host->web push.</summary>"
    )
    lines.append("public static class AppSettingsWireMethods")
    lines.append("{")
    for method in methods:
        lines.append(
            f'    public const string {_method_const(method["method"])} = "{method["method"]}";'
        )
    for notification in notifications:
        lines.append(
            f'    public const string {_method_const(notification["topic"])} = "{notification["topic"]}";'
        )
    request_consts = ", ".join(_method_const(m["method"]) for m in methods)
    lines.append("")
    lines.append(f"    public static readonly string[] WebRequestTypes = [{request_consts}];")
    host_consts = ", ".join(
        [_method_const(m["method"]) for m in methods]
        + [_method_const(n["topic"]) for n in notifications]
    )
    lines.append(f"    public static readonly string[] HostNotificationTypes = [{host_consts}];")
    lines.append("}")
    lines.append("")
    for family, definition in enums.items():
        class_name = CS_ENUM_CLASSES.get(family)
        if class_name is None:
            continue
        lines.append(f"/// <summary>Wire values of the {family} enum family.</summary>")
        lines.append(f"public static class {class_name}")
        lines.append("{")
        const_names = []
        for value in definition["values"]:
            const = _enum_const(value)
            const_names.append(const)
            lines.append(f'    public const string {const} = "{value}";')
        lines.append("")
        lines.append(f"    public static readonly string[] Values = [{', '.join(const_names)}];")
        checks = " or ".join(const_names)
        lines.append("")
        lines.append(f"    public static bool IsKnown(string? value) => value is {checks};")
        lines.append("}")
        lines.append("")
    lines.append(
        "/// <summary>Host -> web preferences projection shared by get/update replies.</summary>"
    )
    lines.append(
        _cs_record("AppSettingsPreferences", contract["preferencesResult"]["fields"], wire=True)
    )
    lines.append("")
    lines.append("/// <summary>Host -> web appearance.changed payload.</summary>")
    lines.append(
        _cs_record(
            "AppSettingsAppearanceChanged", contract["appearanceChanged"]["fields"], wire=True
        )
    )
    lines.append("")
    lines.append("/// <summary>Settings-family operation.failed payload; mirrors the host's")
    lines.append(
        "/// <summary>OperationFailedPayload wire shape (nulls serialize as null).</summary>"
    )
    lines.append(
        _cs_record("AppSettingsOperationFailed", contract["operationFailed"]["fields"], wire=True)
    )
    lines.append("")
    lines.append("/// <summary>Parsed appPreferences.update patch. Has* flags distinguish an")
    lines.append("/// explicitly provided string field from an absent one.</summary>")
    lines.append("public sealed record AppSettingsPreferencesPatch(")
    patch_params: list[str] = []
    for field in contract["updatePatch"]["fields"]:
        name = field["name"]
        cs_type = "bool?" if field["type"] == "boolean" else "string?"
        patch_params.append(f"    {cs_type} {_pascal(name)} = null")
        if name in PATCH_HAS_FLAG_FIELDS:
            patch_params.append(f"    bool Has{_pascal(name)} = false")
    lines.append(",\n".join(patch_params))
    lines.append(");")
    lines.append("")
    lines.append(_cs_patch_reader(contract))
    lines.append("")
    lines.append(
        "/// <summary>Stable operation.failed codes for the app-settings family.</summary>"
    )
    lines.append("public static class AppSettingsErrorCodes")
    lines.append("{")
    for entry in contract["errorCodes"]:
        code = entry["code"]
        stripped = code.removeprefix("APP_PREFERENCES_")
        const = "".join(part[0].upper() + part[1:].lower() for part in stripped.split("_"))
        lines.append(f'    public const string {const} = "{code}";')
    lines.append("}")
    lines.append("")
    return "\n".join(lines)


# ---------------------------------------------------------------------------
# TypeScript renderer
# ---------------------------------------------------------------------------


def _ts_is_guard(enum_family: str) -> str:
    return f"is{TS_TYPE_NAMES[enum_family]}"


def render_typescript(contract: dict[str, Any]) -> str:
    enums = contract["enums"]
    fields_result = contract["preferencesResult"]["fields"]
    patch_fields = contract["updatePatch"]["fields"]
    appearance_fields = contract["appearanceChanged"]["fields"]
    lines: list[str] = []
    lines.append("// Generated from contracts/v2/app-settings-contract.json. Do not edit by hand.")
    lines.append("")
    lines.append('import type { OperationFailedPayload } from "@/contracts";')
    lines.append("")
    for family, definition in enums.items():
        values = definition["values"]
        array_name = TS_ARRAY_NAMES.get(family)
        if array_name is not None:
            lines.append(f"export const {array_name}: readonly {TS_TYPE_NAMES[family]}[] = [")
            lines.extend(f'  "{value}",' for value in values)
            lines.append("];")
            lines.append("")
    lines.append(
        f"export type ThemePreference = {' | '.join(f'"{v}"' for v in enums['themePreference']['values'])};"
    )
    lines.append(
        f"export type UpdateProxyId = {' | '.join(f'"{v}"' for v in enums['updateProxy']['values'])};"
    )
    lines.append(
        f"export type EffectiveBaseTheme = {' | '.join(f'"{v}"' for v in enums['effectiveBaseTheme']['values'])};"
    )
    lines.append("")

    def ts_field_type(field: dict[str, Any], optional: bool) -> str:
        kind = field["type"]
        if kind == "boolean":
            base = "boolean"
        elif kind == "string":
            base = "string"
        else:
            base = TS_TYPE_NAMES[field["enum"]]
            if field.get("nullable"):
                base = f"{base} | null"
        return f"{base}{'?' if optional else ''}"

    lines.append("export interface AppPreferences {")
    for field in fields_result:
        lines.append(f"  readonly {field['name']}: {ts_field_type(field, False)};")
    lines.append("}")
    lines.append("")
    lines.append("export interface AppPreferencesUpdate {")
    for field in patch_fields:
        lines.append(f"  readonly {field['name']}?: {ts_field_type(field, False)};")
    lines.append("}")
    lines.append("")
    methods = [m["method"] for m in contract["methods"]]
    lines.append("export const APP_PREFERENCES_WEB_MESSAGE_TYPES = [")
    lines.extend(f'  "{method}",' for method in methods)
    lines.append("] as const;")
    lines.append("")
    lines.append("export type AppPreferencesWebMessageType =")
    lines.append("  (typeof APP_PREFERENCES_WEB_MESSAGE_TYPES)[number];")
    lines.append("")
    lines.append("export const APP_PREFERENCES_HOST_MESSAGE_TYPES =")
    lines.append("  APP_PREFERENCES_WEB_MESSAGE_TYPES;")
    lines.append("")
    lines.append("export type AppPreferencesHostMessageType =")
    lines.append("  (typeof APP_PREFERENCES_HOST_MESSAGE_TYPES)[number];")
    lines.append("")
    lines.append("export interface AppPreferencesWebPayloadMap {")
    lines.append('  "appPreferences.get": Readonly<Record<string, never>>;')
    lines.append('  "appPreferences.update": AppPreferencesUpdate;')
    lines.append("}")
    lines.append("")
    lines.append("export interface AppPreferencesHostPayloadMap {")
    lines.append('  "appPreferences.get": AppPreferences;')
    lines.append('  "appPreferences.update": AppPreferences;')
    lines.append("}")
    lines.append("")
    topics = [n["topic"] for n in contract["notifications"]]
    lines.append("export const APPEARANCE_HOST_MESSAGE_TYPES = [")
    lines.extend(f'  "{topic}",' for topic in topics)
    lines.append("] as const;")
    lines.append("")
    lines.append("export type AppearanceHostMessageType =")
    lines.append("  (typeof APPEARANCE_HOST_MESSAGE_TYPES)[number];")
    lines.append("")
    lines.append("export interface HostAppearancePayload {")
    for field in appearance_fields:
        lines.append(f"  readonly {field['name']}: {ts_field_type(field, False)};")
    lines.append("}")
    lines.append("")
    lines.append("export interface AppearanceHostPayloadMap {")
    lines.append('  "appearance.changed": HostAppearancePayload;')
    lines.append("}")
    lines.append("")
    lines.append("function isRecord(value: unknown): value is Record<string, unknown> {")
    lines.append('  return typeof value === "object" && value !== null && !Array.isArray(value);')
    lines.append("}")
    lines.append("")
    for family, definition in enums.items():
        guard = _ts_is_guard(family)
        type_name = TS_TYPE_NAMES[family]
        checks = " || ".join(f'value === "{v}"' for v in definition["values"])
        lines.append(f"export function {guard}(value: unknown): value is {type_name} {{")
        lines.append(f"  return {checks};")
        lines.append("}")
    lines.append("")

    # Result decoder.
    lines.append("/** Decodes an appPreferences.get/update reply. Unknown extra fields are")
    lines.append(" * tolerated (forward compatibility); missing required fields, wrong types")
    lines.append(" * and unknown enum values are rejected. Returns null on any violation. */")
    lines.append(
        "export function decodeAppPreferencesResult(value: unknown): AppPreferences | null {"
    )
    lines.append("  if (!isRecord(value)) return null;")
    lines.append("  const record = value;")
    result_entries: list[str] = []
    for field in fields_result:
        name = field["name"]
        kind = field["type"]
        if kind == "boolean":
            lines.append(f'  if (typeof record["{name}"] !== "boolean") return null;')
            result_entries.append(f'    {name}: record["{name}"],')
        elif kind == "string":
            lines.append(f'  if (typeof record["{name}"] !== "string") return null;')
            result_entries.append(f'    {name}: record["{name}"],')
        else:
            guard = _ts_is_guard(field["enum"])
            if field.get("nullable"):
                lines.append(f'  if (!("{name}" in record)) return null;')
                lines.append(f'  const {name} = record["{name}"];')
                lines.append(f"  if ({name} !== null && !{guard}({name})) return null;")
                result_entries.append(f"    {name},")
            else:
                lines.append(f'  if (!{guard}(record["{name}"])) return null;')
                result_entries.append(f'    {name}: record["{name}"],')
    lines.append("  return {")
    lines.extend(result_entries)
    lines.append("  };")
    lines.append("}")
    lines.append("")

    # Appearance decoder.
    lines.append("/** Decodes an appearance.changed push payload; same tolerance rules. */")
    lines.append(
        "export function decodeAppearanceChangedPayload(value: unknown): HostAppearancePayload | null {"
    )
    lines.append("  if (!isRecord(value)) return null;")
    lines.append("  const record = value;")
    a_entries: list[str] = []
    for field in appearance_fields:
        name = field["name"]
        kind = field["type"]
        if kind == "boolean":
            lines.append(f'  if (typeof record["{name}"] !== "boolean") return null;')
            a_entries.append(f'    {name}: record["{name}"],')
        elif kind == "string":
            lines.append(f'  if (typeof record["{name}"] !== "string") return null;')
            a_entries.append(f'    {name}: record["{name}"],')
        else:
            guard = _ts_is_guard(field["enum"])
            if field.get("nullable"):
                lines.append(f'  if (!("{name}" in record)) return null;')
                lines.append(f'  const {name} = record["{name}"];')
                lines.append(f"  if ({name} !== null && !{guard}({name})) return null;")
                a_entries.append(f"    {name},")
            else:
                lines.append(f'  if (!{guard}(record["{name}"])) return null;')
                a_entries.append(f'    {name}: record["{name}"],')
    lines.append("  return {")
    lines.extend(a_entries)
    lines.append("  };")
    lines.append("}")
    lines.append("")

    # operation.failed decoder (settings family only).
    failure_fields = contract["operationFailed"]["fields"]
    lines.append("/** Decodes a settings-family operation.failed payload. The host")
    lines.append(" * serializes unset metadata as null (no WhenWritingNull), so absent and")
    lines.append(" * null are both legal for the optional fields; wrong-typed values are")
    lines.append(" * malformed. Unknown string codes pass through as a safe diagnosable")
    lines.append(" * failure. Fields marked not projected are validated but dropped to")
    lines.append(" * keep the current bridge consumption semantics. */")
    lines.append(
        "export function decodeAppSettingsOperationFailed(value: unknown): OperationFailedPayload | null {"
    )
    lines.append("  if (!isRecord(value)) return null;")
    lines.append("  const record = value;")
    failure_entries: list[str] = []
    for field in failure_fields:
        name = field["name"]
        if field["type"] != "string":
            raise AppSettingsContractError("operationFailed fields must be strings")
        if field.get("optional") is not True or field.get("nullable") is not True:
            lines.append(f'  if (typeof record["{name}"] !== "string") return null;')
            failure_entries.append(f'    {name}: record["{name}"],')
            continue
        lines.append(f'  const {name} = record["{name}"];')
        lines.append(
            f'  if ({name} !== undefined && {name} !== null && typeof {name} !== "string") return null;'
        )
        if field.get("projected", True):
            failure_entries.append(f'    ...(typeof {name} === "string" ? {{ {name} }} : {{}}),')
    lines.append("  return {")
    lines.extend(failure_entries)
    lines.append("  };")
    lines.append("}")
    lines.append("")
    lines.append("/** A settings-family reply violated the wire contract. The request outcome")
    lines.append(" * is unknown: callers must not report success, must not treat a sent update")
    lines.append(" * as uncommitted, and must not replay it automatically. */")
    lines.append("export class AppSettingsProtocolError extends Error {")
    lines.append('  public override readonly name = "AppSettingsProtocolError";')
    lines.append("  public readonly messageType: string;")
    lines.append("  public readonly requestId: string;")
    lines.append("  public constructor(messageType: string, requestId: string) {")
    lines.append("    super(")
    lines.append(
        '      `App settings "${messageType}" reply (requestId=${requestId}) violated the wire contract; ` +'
    )
    lines.append('        "the request outcome is unknown.",')
    lines.append("    );")
    lines.append("    this.messageType = messageType;")
    lines.append("    this.requestId = requestId;")
    lines.append("  }")
    lines.append("}")
    lines.append("")
    return "\n".join(lines)


def outputs(contract: dict[str, Any]) -> dict[Path, str]:
    return {
        CS_OUTPUT: render_csharp(contract),
        TS_OUTPUT: render_typescript(contract),
    }


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--check", action="store_true")
    args = parser.parse_args(argv)
    try:
        generated = outputs(build_contract())
    except (OSError, AppSettingsContractError) as error:
        print(error)
        return 1
    if args.check:
        stale = [
            str(path.relative_to(ROOT))
            for path, text in generated.items()
            if not path.exists() or path.read_text(encoding="utf-8") != text
        ]
        if stale:
            print(f"App settings contract outputs are stale: {stale}")
            return 1
        return 0
    for path, text in generated.items():
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(text, encoding="utf-8", newline="\n")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
