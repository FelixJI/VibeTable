"""Cross-component application and pinned sidecar release versions."""

from __future__ import annotations

import hashlib
import json
import os
import re
import tomllib
from dataclasses import dataclass
from pathlib import Path

SEMVER_RE = re.compile(r"^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)$")
VERSION_SOURCE = Path("backend/_version.py")


class VersionError(ValueError):
    """Version metadata is missing, malformed, or inconsistent."""


@dataclass(frozen=True)
class VersionSnapshot:
    expected: str
    actual: dict[str, str]

    @property
    def mismatches(self) -> dict[str, str]:
        return {name: value for name, value in self.actual.items() if value != self.expected}


@dataclass(frozen=True)
class ReleaseVersions:
    app: str
    pocketbase: str
    cel: str
    contract: str
    schema: str
    migration_hash: str


def validate_version(value: str) -> str:
    if not SEMVER_RE.fullmatch(value):
        raise VersionError(f"invalid version {value!r}; expected MAJOR.MINOR.PATCH")
    return value


def read_project_version(repo_root: Path) -> str:
    source = (repo_root / VERSION_SOURCE).read_text(encoding="utf-8")
    return validate_version(
        _extract(
            r'^__version__\s*=\s*"([^"]+)"',
            source,
            "backend._version.__version__",
        )
    )


def _json(path: Path) -> dict:
    value = json.loads(path.read_text(encoding="utf-8"))
    if not isinstance(value, dict):
        raise VersionError(f"{path} must contain a JSON object")
    return value


def _extract(pattern: str, text: str, label: str) -> str:
    match = re.search(pattern, text, flags=re.MULTILINE)
    if match is None:
        raise VersionError(f"unable to read {label}")
    return match.group(1)


def collect_release_versions(repo_root: Path) -> ReleaseVersions:
    buildinfo = (repo_root / "sidecar" / "internal" / "buildinfo" / "info.go").read_text(
        encoding="utf-8"
    )
    migration_manifest = repo_root / "sidecar" / "migrations" / "manifest.json"
    return ReleaseVersions(
        app=read_project_version(repo_root),
        pocketbase=_extract(
            r'^\s*PocketBaseVersion\s*=\s*"([^"]+)"',
            buildinfo,
            "PocketBase sidecar version",
        ),
        cel=_extract(
            r'^\s*CELVersion\s*=\s*"([^"]+)"',
            buildinfo,
            "CEL sidecar version",
        ),
        contract=_extract(
            r'^\s*ContractVersion\s*=\s*"([^"]+)"',
            buildinfo,
            "sidecar contract version",
        ),
        schema=_extract(
            r'^\s*SchemaVersion\s*=\s*"([^"]+)"',
            buildinfo,
            "sidecar schema version",
        ),
        migration_hash=hashlib.sha256(migration_manifest.read_bytes()).hexdigest(),
    )


def collect_versions(repo_root: Path) -> VersionSnapshot:
    expected = read_project_version(repo_root)
    layout = _json(repo_root / "desktop" / "publish-layout.json")
    components = layout.get("components", {})
    actual = {
        "layout host": str(components.get("host", {}).get("version", "")),
        "layout backend": str(components.get("backend", {}).get("version", "")),
        "layout web": str(components.get("web", {}).get("version", "")),
        "layout sidecar": str(components.get("sidecar", {}).get("version", "")),
    }
    return VersionSnapshot(expected=expected, actual=actual)


def check_release_dependency_versions(repo_root: Path) -> list[str]:
    versions = collect_release_versions(repo_root)
    go_mod = (repo_root / "sidecar" / "go.mod").read_text(encoding="utf-8")
    errors: list[str] = []
    for module, expected in (
        ("github.com/pocketbase/pocketbase", f"v{versions.pocketbase}"),
        ("github.com/google/cel-go", f"v{versions.cel}"),
    ):
        match = re.search(
            rf"^\s*{re.escape(module)}\s+(v[^\s]+)(?:\s+//.*)?$",
            go_mod,
            flags=re.MULTILINE,
        )
        actual = match.group(1) if match is not None else "missing"
        if actual != expected:
            errors.append(
                f"sidecar go.mod dependency version mismatch: {module} "
                f"(expected {expected}, got {actual})"
            )
    return errors


def check_versions(repo_root: Path) -> list[str]:
    snapshot = collect_versions(repo_root)
    errors = [
        f"{name}: {actual!r}, expected {snapshot.expected!r}"
        for name, actual in snapshot.mismatches.items()
    ]
    errors.extend(check_release_dependency_versions(repo_root))
    pyproject = (repo_root / "pyproject.toml").read_text(encoding="utf-8")
    if 'dynamic = ["version"]' not in pyproject or (
        'version = {attr = "backend._version.__version__"}' not in pyproject
    ):
        errors.append("pyproject.toml must derive version from backend._version.__version__")
    backend_contract = (repo_root / "backend" / "contracts" / "system.py").read_text(
        encoding="utf-8"
    )
    if (
        "from backend._version import __version__" not in backend_contract
        or "BACKEND_VERSION: Final[str] = __version__" not in backend_contract
    ):
        errors.append("backend handshake must derive version from backend._version")
    props = (repo_root / "desktop" / "Directory.Build.props").read_text(encoding="utf-8")
    if (
        "backend\\_version.py" not in props
        or "System.Text.RegularExpressions.Regex" not in props
        or re.search(r"<Version>\d+\.\d+\.\d+</Version>", props)
    ):
        errors.append("desktop assembly must derive version from backend/_version.py")
    supervisor = (
        repo_root
        / "desktop"
        / "src"
        / "VibeTable.Infrastructure"
        / "Backend"
        / "PythonBackendSupervisor.cs"
    ).read_text(encoding="utf-8")
    if "ApplicationVersion.FromAssembly" not in supervisor:
        errors.append("desktop backend handshake must use assembly informational version")
    for relative in (
        Path("desktop/web-grid/package.json"),
        Path("desktop/web-grid/package-lock.json"),
    ):
        package = _json(repo_root / relative)
        if "version" in package or (
            relative.name == "package-lock.json"
            and "version" in package.get("packages", {}).get("", {})
        ):
            errors.append(f"{relative.as_posix()} must not duplicate the application version")
    with (repo_root / "uv.lock").open("rb") as stream:
        uv_lock = tomllib.load(stream)
    editable_package = next(
        (
            package
            for package in uv_lock.get("package", [])
            if package.get("name") == "vibetable"
            and package.get("source", {}).get("editable") == "."
        ),
        None,
    )
    if editable_package is None or "version" in editable_package:
        errors.append("uv.lock editable package must derive the dynamic application version")
    return errors


def bump_version(current: str, part: str) -> str:
    major, minor, patch = (int(value) for value in validate_version(current).split("."))
    if part == "major":
        return f"{major + 1}.0.0"
    if part == "minor":
        return f"{major}.{minor + 1}.0"
    if part == "patch":
        return f"{major}.{minor}.{patch + 1}"
    raise VersionError(f"unknown version part: {part}")


def _replace_once(text: str, pattern: str, replacement: str, label: str) -> str:
    updated, count = re.subn(
        pattern,
        replacement,
        text,
        count=1,
        flags=re.MULTILINE,
    )
    if count != 1:
        raise VersionError(f"unable to update {label}")
    return updated


def _render_json(value: dict) -> str:
    return json.dumps(value, ensure_ascii=False, indent=2) + "\n"


def _updated_contents(repo_root: Path, version: str) -> dict[Path, str]:
    version = validate_version(version)
    changes: dict[Path, str] = {}
    version_source = repo_root / VERSION_SOURCE
    changes[version_source] = _replace_once(
        version_source.read_text(encoding="utf-8"),
        r'^(__version__\s*=\s*)"[^"]+"',
        rf'\g<1>"{version}"',
        "application version source",
    )
    layout_path = repo_root / "desktop" / "publish-layout.json"
    layout = _json(layout_path)
    components = layout.setdefault("components", {})
    for name in ("host", "backend", "web"):
        components[name] = {"version": version}
    sidecar = components.setdefault("sidecar", {})
    sidecar["version"] = version
    changes[layout_path] = _render_json(layout)
    return changes


def update_versions(
    repo_root: Path,
    version: str,
    *,
    dry_run: bool = False,
) -> list[Path]:
    changes = _updated_contents(repo_root.resolve(), version)
    changed = [
        path for path, content in changes.items() if path.read_text(encoding="utf-8") != content
    ]
    if dry_run:
        return changed
    originals = {path: path.read_text(encoding="utf-8") for path in changed}
    replaced: list[Path] = []
    try:
        for path in changed:
            temporary = path.with_name(path.name + ".vibetable-version.tmp")
            temporary.write_text(changes[path], encoding="utf-8", newline="")
            os.replace(temporary, path)
            replaced.append(path)
    except OSError:
        for path in reversed(replaced):
            path.write_text(originals[path], encoding="utf-8", newline="")
        raise
    return changed
