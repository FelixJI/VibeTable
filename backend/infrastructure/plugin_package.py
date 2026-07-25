"""Offline inspection and deterministic packaging for ``.vtplugin`` bundles."""

from __future__ import annotations

import hashlib
import json
import re
import stat
import unicodedata
import zipfile
from dataclasses import dataclass
from pathlib import Path, PurePosixPath
from typing import Any, ClassVar

from backend.infrastructure.plugin_schema import (
    PluginSchemaError,
    validate_plugin_schema_document,
)


@dataclass(frozen=True, slots=True)
class PackagePolicy:
    """Host-owned package limits; plugins cannot raise these values."""

    MAX_PACKAGE_BYTES: ClassVar[int] = 64 * 1024 * 1024
    MAX_FILE_COUNT: ClassVar[int] = 2_048
    MAX_SINGLE_FILE_BYTES: ClassVar[int] = 32 * 1024 * 1024
    MAX_UNCOMPRESSED_BYTES: ClassVar[int] = 256 * 1024 * 1024

    max_package_bytes: int = MAX_PACKAGE_BYTES
    max_file_count: int = MAX_FILE_COUNT
    max_single_file_bytes: int = MAX_SINGLE_FILE_BYTES
    max_uncompressed_bytes: int = MAX_UNCOMPRESSED_BYTES


DEFAULT_PACKAGE_POLICY = PackagePolicy()


@dataclass(frozen=True, slots=True)
class PluginCompatibilityPolicy:
    """Versions implemented by this host and checked before package installation."""

    host_version: str = "1.0.0"
    plugin_api: str = "1.x"


DEFAULT_COMPATIBILITY_POLICY = PluginCompatibilityPolicy()

_PLUGIN_ID_PATTERN = re.compile(r"^[a-z0-9]+(?:[.-][a-z0-9][a-z0-9-]*)+$")
_SEMVER_PATTERN = re.compile(
    r"^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)"
    r"(?:-[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?(?:\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?$"
)
_VERSION_TERM_PATTERN = re.compile(r"^(>=|<=|>|<|=)?(\d+)(?:\.(\d+))?(?:\.(\d+))?$")
_RISKS = {"read", "write", "destructive"}
_MODES = {"local"}
_SCHEMA_REFERENCE_KEYS = {"formSchema", "inputSchema", "outputSchema"}
_LOCAL_DEVELOPMENT_DIRECTORIES = {".git", ".pytest_cache", "node_modules", "src", "tests"}
_LOCAL_DEVELOPMENT_FILES = {
    ".gitignore",
    "package-lock.json",
    "package.json",
    "tsconfig.json",
}


def _version_tuple(value: str) -> tuple[int, int, int] | None:
    """Return a comparable three-part release version for compatibility checks."""

    match = _VERSION_TERM_PATTERN.fullmatch(value)
    if match is None or match.group(1) is not None:
        return None
    return tuple(int(part or 0) for part in match.groups()[1:])  # type: ignore[return-value]


def _range_contains(expression: str, version: str) -> bool | None:
    """Evaluate the small conjunctive range dialect used by plugin manifests."""

    candidate = _version_tuple(version)
    if candidate is None:
        return None
    terms = expression.split()
    if not terms:
        return None
    matched = True
    for term in terms:
        parsed = _VERSION_TERM_PATTERN.fullmatch(term)
        if parsed is None:
            return None
        operator = parsed.group(1) or "="
        expected = tuple(int(part or 0) for part in parsed.groups()[1:])
        comparisons = {
            "=": candidate == expected,
            ">": candidate > expected,
            ">=": candidate >= expected,
            "<": candidate < expected,
            "<=": candidate <= expected,
        }
        matched = matched and comparisons[operator]
    return matched


def _validate_compatibility(
    compatibility: Any,
    *,
    policy: PluginCompatibilityPolicy = DEFAULT_COMPATIBILITY_POLICY,
) -> None:
    if not isinstance(compatibility, dict):
        raise PluginPackageError("compatibility_invalid", "compatibility must be an object")
    missing = [
        key
        for key in ("minHostVersion", "pluginApi")
        if not isinstance(compatibility.get(key), str) or not compatibility[key].strip()
    ]
    if missing:
        raise PluginPackageError(
            "compatibility_invalid",
            f"compatibility is missing valid fields: {', '.join(missing)}",
        )

    minimum_host = compatibility["minHostVersion"]
    minimum = _version_tuple(minimum_host)
    current_host = _version_tuple(policy.host_version)
    if minimum is None:
        raise PluginPackageError(
            "compatibility_invalid", "compatibility.minHostVersion must be semantic versioning"
        )
    if current_host is None or current_host < minimum:
        raise PluginPackageError(
            "version_incompatible",
            f"compatibility.minHostVersion {minimum_host} exceeds host {policy.host_version}",
        )

    plugin_api = compatibility["pluginApi"]
    if plugin_api != policy.plugin_api:
        raise PluginPackageError(
            "version_incompatible",
            f"compatibility.pluginApi {plugin_api} is not supported by {policy.plugin_api}",
        )

    legacy = sorted(set(compatibility) & {"dire" "ctus"})
    if legacy:
        raise PluginPackageError(
            "legacy_manifest_field",
            f"unsupported legacy compatibility field: {legacy[0]}",
            path=f"compatibility.{legacy[0]}",
        )


@dataclass(frozen=True, slots=True)
class PackageFile:
    """One normalized package member."""

    path: str
    size: int
    sha256: str


@dataclass(frozen=True, slots=True)
class InspectedPluginPackage:
    """Validated metadata exposed at the package inspection seam."""

    source: Path
    manifest: dict[str, Any]
    files: tuple[PackageFile, ...]
    package_hash: str


class PluginPackageError(ValueError):
    """A stable package validation failure suitable for CLI/API reporting."""

    def __init__(self, code: str, message: str, *, path: str | None = None) -> None:
        super().__init__(message)
        self.code = code
        self.path = path

    @property
    def rpc_error_data(self) -> dict[str, str]:
        data = {"code": self.code, "recoverability": "reinstall"}
        if self.path is not None:
            data["path"] = self.path
        return data


def _normalized_package_digest(entries: list[tuple[str, bytes]]) -> str:
    digest = hashlib.sha256()
    for name, content in sorted(entries):
        encoded_name = name.encode("utf-8")
        digest.update(len(encoded_name).to_bytes(8, "big"))
        digest.update(encoded_name)
        digest.update(len(content).to_bytes(8, "big"))
        digest.update(content)
    return f"sha256:{digest.hexdigest()}"


def _canonical_json(value: Any) -> bytes:
    return json.dumps(value, ensure_ascii=False, sort_keys=True, separators=(",", ":")).encode(
        "utf-8"
    )


def _integrity_content(entries: list[tuple[str, bytes]]) -> bytes:
    files = {
        name: hashlib.sha256(content).hexdigest()
        for name, content in sorted(entries)
        if name != "integrity.json"
    }
    return _canonical_json({"algorithm": "sha256", "files": files})


def _verify_integrity(entries: list[tuple[str, bytes]], *, required: bool) -> None:
    contents = dict(entries)
    raw_integrity = contents.get("integrity.json")
    if raw_integrity is None:
        if required:
            raise PluginPackageError("integrity_missing", "archive does not contain integrity.json")
        return
    try:
        integrity = json.loads(raw_integrity.decode("utf-8"))
    except (UnicodeDecodeError, json.JSONDecodeError) as exc:
        raise PluginPackageError(
            "integrity_invalid", "integrity.json is not valid UTF-8 JSON"
        ) from exc
    if not isinstance(integrity, dict) or integrity.get("algorithm") != "sha256":
        raise PluginPackageError("integrity_invalid", "integrity.json must use sha256")
    expected = integrity.get("files")
    actual = json.loads(_integrity_content(entries))["files"]
    if expected != actual:
        raise PluginPackageError(
            "integrity_mismatch", "package contents do not match integrity.json"
        )


def _decode_json(contents: dict[str, bytes], path: str, *, code: str) -> Any:
    raw = contents.get(path)
    if raw is None:
        raise PluginPackageError(
            "missing_reference", f"referenced package file is missing: {path}", path=path
        )
    try:
        return json.loads(raw.decode("utf-8"))
    except (UnicodeDecodeError, json.JSONDecodeError) as exc:
        raise PluginPackageError(code, f"{path} is not valid UTF-8 JSON", path=path) from exc


def _require_object(value: Any, label: str) -> dict[str, Any]:
    if not isinstance(value, dict):
        raise PluginPackageError("manifest_invalid", f"{label} must be an object")
    return value


def _validate_reference(contents: dict[str, bytes], reference: Any, *, schema: bool) -> str:
    if not isinstance(reference, str):
        raise PluginPackageError("manifest_invalid", "package references must be strings")
    path = _normalize_member_path(reference)
    if path not in contents:
        raise PluginPackageError(
            "missing_reference", f"referenced package file is missing: {path}", path=path
        )
    if schema:
        document = _decode_json(contents, path, code="schema_invalid")
        if not isinstance(document, dict):
            raise PluginPackageError(
                "schema_invalid", f"{path} is not a supported JSON Schema", path=path
            )
        try:
            validate_plugin_schema_document(document, label=path)
        except PluginSchemaError as exc:
            raise PluginPackageError("schema_invalid", str(exc), path=path) from exc
    return path


def validate_plugin_manifest(entries: list[tuple[str, bytes]]) -> dict[str, Any]:
    """Validate manifest structure and all package-local references."""

    contents = dict(entries)
    manifest = _require_object(
        _decode_json(contents, "manifest.json", code="manifest_invalid"), "manifest"
    )
    if manifest.get("$schema") != "vibetable.plugin-manifest.v1":
        raise PluginPackageError("manifest_schema", "unsupported plugin manifest schema")
    plugin_id = manifest.get("pluginId")
    if not isinstance(plugin_id, str) or not _PLUGIN_ID_PATTERN.fullmatch(plugin_id):
        raise PluginPackageError("plugin_id", "pluginId must be a reverse-domain identifier")
    version = manifest.get("version")
    if not isinstance(version, str) or not _SEMVER_PATTERN.fullmatch(version):
        raise PluginPackageError("version", "version must be semantic versioning")
    display_name = manifest.get("displayName")
    if (
        not isinstance(display_name, dict)
        or not display_name
        or not all(
            isinstance(locale, str) and isinstance(text, str) and text
            for locale, text in display_name.items()
        )
    ):
        raise PluginPackageError("manifest_invalid", "displayName must contain localized text")

    permissions = _require_object(manifest.get("permissions"), "permissions")
    _validate_compatibility(manifest.get("compatibility"))
    data_permissions = permissions.get("data", [])
    if not isinstance(data_permissions, list):
        raise PluginPackageError("permissions_invalid", "permissions.data must be an array")
    for declaration in data_permissions:
        item = _require_object(declaration, "data permission")
        operations = item.get("operations")
        if (
            not isinstance(item.get("collection"), str)
            or not isinstance(operations, list)
            or not operations
        ):
            raise PluginPackageError(
                "permissions_invalid", "data permissions require a collection and operations"
            )
        if not all(
            isinstance(operation, str) and operation in {"read", "create", "update", "delete"}
            for operation in operations
        ):
            raise PluginPackageError(
                "permissions_invalid", "data permission has an unsupported operation"
            )
        fields = item.get("fields", [])
        if not isinstance(fields, list) or not all(isinstance(field, str) for field in fields):
            raise PluginPackageError(
                "permissions_invalid", "data permission fields must be strings"
            )
    file_permissions = permissions.get("files", [])
    if (
        not isinstance(file_permissions, list)
        or not all(
            isinstance(item, str) and item in {"pickRead", "pickWrite"} for item in file_permissions
        )
        or not isinstance(permissions.get("privateStorage", False), bool)
    ):
        raise PluginPackageError(
            "permissions_invalid", "files/privateStorage permission is invalid"
        )
    network = permissions.get("network", {})
    if not isinstance(network, dict):
        raise PluginPackageError(
            "permissions_invalid", "permissions.network must be an object"
        )
    domains = network.get("domains", [])
    methods = network.get("methods", ["GET"])
    if (
        not isinstance(domains, list)
        or not all(
            isinstance(domain, str)
            and domain
            and "://" not in domain
            and "/" not in domain
            for domain in domains
        )
        or not isinstance(methods, list)
        or not all(
            isinstance(method, str)
            and method in {"GET", "POST", "PUT", "PATCH", "DELETE"}
            for method in methods
        )
    ):
        raise PluginPackageError(
            "permissions_invalid",
            "network permissions require bare domains and supported methods",
        )

    raw_actions = manifest.get("actions")
    if not isinstance(raw_actions, list):
        raise PluginPackageError("manifest_invalid", "actions must be an array")
    legacy_top_level = sorted(
        key for key in ("flows", "flowBindings") if key in manifest
    )
    if legacy_top_level:
        raise PluginPackageError(
            "legacy_manifest_field",
            f"unsupported legacy manifest field: {legacy_top_level[0]}",
            path=legacy_top_level[0],
        )

    action_ids: set[str] = set()
    for raw_action in raw_actions:
        action = _require_object(raw_action, "action")
        action_id = action.get("actionId")
        mode = action.get("mode")
        risk = action.get("risk")
        if not isinstance(action_id, str) or not action_id or action_id in action_ids:
            raise PluginPackageError("action_id", "actionId must be a unique non-empty string")
        action_ids.add(action_id)
        if mode not in _MODES or risk not in _RISKS:
            raise PluginPackageError(
                "action_invalid", f"action {action_id} has invalid mode or risk"
            )
        if action.get("invocation", "manual") not in {"manual", "webhook"}:
            raise PluginPackageError(
                "action_invalid", f"action {action_id} has an invalid invocation"
            )
        placements = action.get("placements", [])
        if not isinstance(placements, list) or not all(
            isinstance(placement, str) and placement for placement in placements
        ):
            raise PluginPackageError(
                "action_invalid", f"action {action_id} placements must contain strings"
            )
        for key in _SCHEMA_REFERENCE_KEYS:
            if key in action:
                _validate_reference(contents, action[key], schema=True)
        worker_entry = action.get("workerEntry")
        if "entryFlow" in action:
            raise PluginPackageError(
                "legacy_manifest_field",
                f"action {action_id} cannot declare entryFlow",
                path=f"actions.{action_id}.entryFlow",
            )
        if not isinstance(worker_entry, str):
            raise PluginPackageError(
                "worker_entry",
                f"local action {action_id} must declare workerEntry",
            )
        _validate_reference(contents, worker_entry, schema=False)

    ui = manifest.get("ui", {})
    if not isinstance(ui, dict) or not isinstance(ui.get("customViews", []), list):
        raise PluginPackageError("ui_invalid", "ui.customViews must be an array")
    view_ids: set[str] = set()
    for raw_view in ui.get("customViews", []):
        view = _require_object(raw_view, "custom view")
        view_id = view.get("viewId")
        action_id = view.get("actionId")
        if not isinstance(view_id, str) or not view_id or view_id in view_ids:
            raise PluginPackageError("ui_invalid", "custom viewId must be unique")
        view_ids.add(view_id)
        if not isinstance(action_id, str) or action_id not in action_ids:
            raise PluginPackageError(
                "ui_invalid", f"custom view {view_id!r} must reference a declared actionId"
            )
        if "src" in view or "surfaceToken" in view:
            raise PluginPackageError(
                "ui_invalid", "custom view origin and surface token are assigned by the host"
            )
        if not isinstance(view.get("entry"), str):
            raise PluginPackageError(
                "ui_invalid", f"custom view {view_id!r} must declare an entry document"
            )
        for key in ("entry", "html", "script", "style"):
            if key in view:
                _validate_reference(contents, view[key], schema=False)
    return manifest


def _normalize_member_path(name: str) -> str:
    candidate = unicodedata.normalize("NFC", name.replace("\\", "/"))
    parts = PurePosixPath(candidate).parts
    if (
        not candidate
        or "\x00" in candidate
        or candidate.startswith("/")
        or re.match(r"^[A-Za-z]:", candidate)
        or ".." in parts
    ):
        raise PluginPackageError("unsafe_path", f"package path escapes its root: {name}", path=name)
    normalized = "/".join(part for part in parts if part not in ("", "."))
    if not normalized:
        raise PluginPackageError("unsafe_path", f"package path is empty: {name}", path=name)
    return normalized


def _check_entry_limits(metadata: list[tuple[str, int]], policy: PackagePolicy) -> None:
    if len(metadata) > policy.max_file_count:
        raise PluginPackageError(
            "file_count_limit",
            f"package contains {len(metadata)} files; limit is {policy.max_file_count}",
        )
    total = 0
    normalized_keys: set[str] = set()
    for name, size in metadata:
        duplicate_key = name.casefold()
        if duplicate_key in normalized_keys:
            raise PluginPackageError(
                "duplicate_path", f"duplicate normalized package path: {name}", path=name
            )
        normalized_keys.add(duplicate_key)
        if size > policy.max_single_file_bytes:
            raise PluginPackageError(
                "single_file_limit",
                f"package member exceeds {policy.max_single_file_bytes} bytes: {name}",
                path=name,
            )
        total += size
        if total > policy.max_uncompressed_bytes:
            raise PluginPackageError(
                "uncompressed_size_limit",
                f"package expands beyond {policy.max_uncompressed_bytes} bytes",
            )


def _read_entries(source: Path, policy: PackagePolicy) -> list[tuple[str, bytes]]:
    if not source.exists():
        raise PluginPackageError("source_not_found", f"package source does not exist: {source}")
    if source.is_dir():
        paths = [
            path
            for path in source.rglob("*")
            if not _is_local_development_path(path.relative_to(source))
        ]
        symlink = next((path for path in paths if path.is_symlink()), None)
        if symlink is not None:
            relative = symlink.relative_to(source).as_posix()
            raise PluginPackageError(
                "symbolic_link", f"symbolic links are not allowed: {relative}", path=relative
            )
        files = [path for path in paths if path.is_file()]
        metadata = [
            (_normalize_member_path(path.relative_to(source).as_posix()), path.stat().st_size)
            for path in files
        ]
        if sum(size for _, size in metadata) > policy.max_package_bytes:
            raise PluginPackageError(
                "package_size_limit", f"folder exceeds {policy.max_package_bytes} bytes"
            )
        _check_entry_limits(metadata, policy)
        return [(name, path.read_bytes()) for (name, _), path in zip(metadata, files, strict=True)]
    if source.stat().st_size > policy.max_package_bytes:
        raise PluginPackageError(
            "package_size_limit", f"archive exceeds {policy.max_package_bytes} bytes"
        )
    if not zipfile.is_zipfile(source):
        raise PluginPackageError("invalid_package", "source is neither a folder nor a ZIP package")
    with zipfile.ZipFile(source) as archive:
        members = [member for member in archive.infolist() if not member.is_dir()]
        for member in members:
            mode = member.external_attr >> 16
            if stat.S_ISLNK(mode):
                raise PluginPackageError(
                    "symbolic_link",
                    f"symbolic links are not allowed: {member.filename}",
                    path=member.filename,
                )
        metadata = [
            (_normalize_member_path(member.filename), member.file_size) for member in members
        ]
        _check_entry_limits(metadata, policy)
        entries = [
            (name, archive.read(member))
            for (name, _), member in zip(metadata, members, strict=True)
        ]
        _check_entry_limits([(name, len(content)) for name, content in entries], policy)
        return entries


def _is_local_development_path(path: Path) -> bool:
    """Exclude build inputs that must never leak into an offline package."""

    if path.parts and path.parts[0] in _LOCAL_DEVELOPMENT_DIRECTORIES:
        return True
    return len(path.parts) == 1 and path.name in _LOCAL_DEVELOPMENT_FILES


def inspect_plugin_package(
    source: str | Path, *, policy: PackagePolicy = DEFAULT_PACKAGE_POLICY
) -> InspectedPluginPackage:
    """Inspect a ``.vtplugin`` ZIP or local development folder."""

    root = Path(source)
    entries = _read_entries(root, policy)
    _verify_integrity(entries, required=root.is_file())
    digest_entries = (
        entries
        if any(name == "integrity.json" for name, _ in entries)
        else [*entries, ("integrity.json", _integrity_content(entries))]
    )
    files = tuple(
        PackageFile(path=name, size=len(content), sha256=hashlib.sha256(content).hexdigest())
        for name, content in sorted(entries)
    )
    manifest = validate_plugin_manifest(entries)
    return InspectedPluginPackage(
        source=root,
        manifest=manifest,
        files=files,
        package_hash=_normalized_package_digest(digest_entries),
    )


def read_plugin_package_member(
    source: str | Path,
    member_path: str,
    *,
    policy: PackagePolicy = DEFAULT_PACKAGE_POLICY,
) -> bytes:
    """Read one member only after applying the package's full safety policy."""

    root = Path(source)
    entries = _read_entries(root, policy)
    _verify_integrity(entries, required=root.is_file())
    normalized = _normalize_member_path(member_path)
    contents = dict(entries)
    if normalized not in contents:
        raise PluginPackageError(
            "missing_reference",
            f"referenced package file is missing: {normalized}",
            path=normalized,
        )
    return contents[normalized]


def pack_plugin(
    source: str | Path,
    output: str | Path,
    *,
    policy: PackagePolicy = DEFAULT_PACKAGE_POLICY,
) -> str:
    """Create a deterministic package and return its normalized SHA-256 hash."""

    root = Path(source)
    if not root.is_dir():
        raise PluginPackageError("invalid_source", "pack source must be a local plugin folder")
    entries = [
        (name, content) for name, content in _read_entries(root, policy) if name != "integrity.json"
    ]
    validate_plugin_manifest(entries)
    entries.append(("integrity.json", _integrity_content(entries)))
    _check_entry_limits([(name, len(content)) for name, content in entries], policy)
    destination = Path(output)
    destination.parent.mkdir(parents=True, exist_ok=True)
    with zipfile.ZipFile(destination, "w", compression=zipfile.ZIP_STORED) as archive:
        for name, content in sorted(entries):
            info = zipfile.ZipInfo(name, date_time=(1980, 1, 1, 0, 0, 0))
            info.compress_type = zipfile.ZIP_STORED
            info.create_system = 3
            info.external_attr = (stat.S_IFREG | 0o644) << 16
            archive.writestr(info, content)
    if destination.stat().st_size > policy.max_package_bytes:
        destination.unlink(missing_ok=True)
        raise PluginPackageError(
            "package_size_limit", f"archive exceeds {policy.max_package_bytes} bytes"
        )
    return _normalized_package_digest(entries)


__all__ = [
    "DEFAULT_COMPATIBILITY_POLICY",
    "DEFAULT_PACKAGE_POLICY",
    "InspectedPluginPackage",
    "PackageFile",
    "PackagePolicy",
    "PluginCompatibilityPolicy",
    "PluginPackageError",
    "inspect_plugin_package",
    "pack_plugin",
    "read_plugin_package_member",
    "validate_plugin_manifest",
]
