#!/usr/bin/env python3
"""Validate the exact v2 legacy-surface retirement contract."""

from __future__ import annotations

import argparse
import json
from pathlib import Path
from typing import Any

PROJECT_ROOT = Path(__file__).resolve().parents[1]
DEFAULT_MANIFEST = PROJECT_ROOT / "contracts" / "v2" / "legacy-surface.json"


def _inside(root: Path, candidate: Path) -> bool:
    try:
        candidate.resolve().relative_to(root.resolve())
        return True
    except ValueError:
        return False


def _resolve(root: Path, value: str) -> Path:
    candidate = root / value
    if Path(value).is_absolute() or not _inside(root, candidate):
        raise ValueError(f"manifest path escapes repository: {value}")
    return candidate


def _json_pointer(value: Any, pointer: str) -> Any:
    if pointer == "":
        return value
    if not pointer.startswith("/"):
        raise ValueError(f"invalid JSON pointer: {pointer}")
    current = value
    for encoded in pointer[1:].split("/"):
        token = encoded.replace("~1", "/").replace("~0", "~")
        if isinstance(current, dict):
            current = current[token]
        elif isinstance(current, list):
            current = current[int(token)]
        else:
            raise KeyError(pointer)
    return current


def _catalog_methods(payload: dict[str, Any]) -> set[str]:
    methods: set[str] = set()
    for advertised_name in ("methods", "rpcMethods"):
        advertised = payload.get(advertised_name)
        if isinstance(advertised, list):
            methods.update(item for item in advertised if isinstance(item, str))
    for fixtures_name in ("fixtures", "rpcCases"):
        fixtures = payload.get(fixtures_name)
        if not isinstance(fixtures, list):
            continue
        for fixture in fixtures:
            if not isinstance(fixture, dict):
                continue
            request = fixture.get("request")
            if isinstance(request, dict) and isinstance(request.get("method"), str):
                methods.add(request["method"])
    return methods


def _json_member_names(value: Any) -> set[str]:
    if isinstance(value, dict):
        return set(value).union(
            *(_json_member_names(item) for item in value.values()),
        )
    if isinstance(value, list):
        return set().union(*(_json_member_names(item) for item in value))
    return set()


def check(root: Path = PROJECT_ROOT, manifest_path: Path | None = None) -> list[str]:
    root = root.resolve()
    source = (manifest_path or root / "contracts" / "v2" / "legacy-surface.json").resolve()
    manifest = json.loads(source.read_text(encoding="utf-8"))
    if manifest.get("schemaVersion") != 1:
        return ["legacy-surface manifest schemaVersion must be 1"]

    errors: list[str] = []
    forbidden_paths = manifest.get("forbiddenPaths")
    if not isinstance(forbidden_paths, list) or not forbidden_paths:
        errors.append("legacy-surface manifest must declare forbiddenPaths")
    else:
        for relative in forbidden_paths:
            if not isinstance(relative, str) or not relative:
                errors.append("legacy-surface forbidden path must be a non-empty string")
                continue
            try:
                candidate = _resolve(root, relative)
            except ValueError as exc:
                errors.append(str(exc))
                continue
            if candidate.exists():
                errors.append(f"legacy product path still exists: {relative}")

    for entry in manifest.get("forbiddenSourceLiterals", []):
        if not isinstance(entry, dict):
            errors.append("forbiddenSourceLiterals entry must be an object")
            continue
        relative = entry.get("path")
        literals = entry.get("literals")
        if not isinstance(relative, str) or not isinstance(literals, list):
            errors.append("invalid forbiddenSourceLiterals entry")
            continue
        path = _resolve(root, relative)
        if not path.is_file():
            continue
        text = path.read_text(encoding="utf-8", errors="replace")
        for literal in literals:
            if isinstance(literal, str) and literal in text:
                errors.append(f"legacy source literal remains in {relative}: {literal}")

    for entry in manifest.get("forbiddenJsonValues", []):
        relative = entry["path"]
        path = _resolve(root, relative)
        if not path.is_file():
            continue
        try:
            target = _json_pointer(
                json.loads(path.read_text(encoding="utf-8")),
                entry["pointer"],
            )
        except (KeyError, TypeError, ValueError, json.JSONDecodeError) as exc:
            errors.append(f"cannot inspect {relative}{entry.get('pointer', '')}: {exc}")
            continue
        values = target if isinstance(target, list) else [target]
        for forbidden in entry["values"]:
            if forbidden in values:
                errors.append(
                    f"legacy JSON value remains in {relative}{entry['pointer']}: {forbidden}"
                )

    for entry in manifest.get("forbiddenJsonMembers", []):
        relative = entry["path"]
        path = _resolve(root, relative)
        if not path.is_file():
            continue
        try:
            target = _json_pointer(
                json.loads(path.read_text(encoding="utf-8")),
                entry["pointer"],
            )
        except (KeyError, TypeError, ValueError, json.JSONDecodeError) as exc:
            errors.append(f"cannot inspect {relative}{entry.get('pointer', '')}: {exc}")
            continue
        if not isinstance(target, dict):
            errors.append(f"JSON pointer does not select an object: {relative}{entry['pointer']}")
            continue
        for member in entry["members"]:
            if member in target:
                errors.append(
                    f"legacy JSON member remains in {relative}{entry['pointer']}: {member}"
                )

    raw_methods = manifest.get("forbiddenRpcMethods")
    if not isinstance(raw_methods, list) or not all(
        isinstance(item, str) and item for item in raw_methods
    ):
        errors.append("legacy-surface forbiddenRpcMethods must contain names")
        raw_methods = []
    raw_fields = manifest.get("forbiddenDtoFields")
    if not isinstance(raw_fields, list) or not all(
        isinstance(item, str) and item for item in raw_fields
    ):
        errors.append("legacy-surface forbiddenDtoFields must contain names")
        raw_fields = []
    current_catalogs = manifest.get("currentCatalogs")
    if not isinstance(current_catalogs, list) or not all(
        isinstance(item, str) and item for item in current_catalogs
    ):
        errors.append("legacy-surface currentCatalogs must contain paths")
        current_catalogs = []
    field_guard_catalogs = manifest.get("catalogsWithLegacyDtoFieldGuard")
    if not isinstance(field_guard_catalogs, list) or not all(
        isinstance(item, str) and item for item in field_guard_catalogs
    ):
        errors.append("legacy-surface catalogsWithLegacyDtoFieldGuard must contain paths")
        field_guard_catalogs = []
    unknown_field_guard_catalogs = set(field_guard_catalogs) - set(current_catalogs)
    errors.extend(
        f"legacy DTO field guard catalog is not current: {item}"
        for item in sorted(unknown_field_guard_catalogs)
    )
    forbidden_methods = set(raw_methods)
    forbidden_fields = set(raw_fields)
    for relative in current_catalogs:
        path = _resolve(root, relative)
        if not path.is_file():
            errors.append(f"missing current RPC catalog: {relative}")
            continue
        payload = json.loads(path.read_text(encoding="utf-8"))
        overlap = sorted(forbidden_methods.intersection(_catalog_methods(payload)))
        errors.extend(
            f"legacy RPC remains in current catalog {relative}: {item}" for item in overlap
        )
        if relative in field_guard_catalogs:
            field_overlap = sorted(forbidden_fields.intersection(_json_member_names(payload)))
            errors.extend(
                f"legacy DTO field remains in current catalog {relative}: {item}"
                for item in field_overlap
            )

    allowlist = manifest.get("allowedDetectionPrimitives")
    if not isinstance(allowlist, list) or not allowlist:
        errors.append("legacy-surface manifest must document detection/upgrade allowlist")
    else:
        for entry in allowlist:
            if not isinstance(entry, dict):
                errors.append("allowedDetectionPrimitives entry must be an object")
                continue
            relative = entry.get("path")
            purpose = entry.get("purpose")
            if not isinstance(relative, str) or not isinstance(purpose, str) or not purpose:
                errors.append("invalid allowedDetectionPrimitives entry")
                continue
            if not _resolve(root, relative).exists():
                errors.append(f"allowlisted detection primitive is missing: {relative}")
    return errors


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--root", type=Path, default=PROJECT_ROOT)
    parser.add_argument("--manifest", type=Path)
    args = parser.parse_args(argv)
    errors = check(args.root, args.manifest)
    if errors:
        print("[FAIL] legacy product surface:")
        for error in errors:
            print(f"  - {error}")
        return 1
    print("[OK] legacy product surface absent")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
