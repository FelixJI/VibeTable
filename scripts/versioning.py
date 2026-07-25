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
    with (repo_root / "pyproject.toml").open("rb") as stream:
        value = tomllib.load(stream).get("project", {}).get("version")
    if not isinstance(value, str):
        raise VersionError("pyproject.toml is missing [project].version")
    return validate_version(value)


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
    web_package = _json(repo_root / "desktop" / "web-grid" / "package.json")
    web_lock = _json(repo_root / "desktop" / "web-grid" / "package-lock.json")
    layout = _json(repo_root / "desktop" / "publish-layout.json")
    components = layout.get("components", {})
    backend = (repo_root / "backend" / "contracts" / "system.py").read_text(encoding="utf-8")
    supervisor = (
        repo_root
        / "desktop"
        / "src"
        / "VibeTable.Infrastructure"
        / "Backend"
        / "PythonBackendSupervisor.cs"
    ).read_text(encoding="utf-8")
    props = (repo_root / "desktop" / "Directory.Build.props").read_text(encoding="utf-8")
    actual = {
        "web/package.json": str(web_package.get("version", "")),
        "web/package-lock.json": str(web_lock.get("version", "")),
        "web/package-lock root": str(web_lock.get("packages", {}).get("", {}).get("version", "")),
        "layout host": str(components.get("host", {}).get("version", "")),
        "layout backend": str(components.get("backend", {}).get("version", "")),
        "layout web": str(components.get("web", {}).get("version", "")),
        "layout sidecar": str(components.get("sidecar", {}).get("version", "")),
        "backend handshake": _extract(
            r'^BACKEND_VERSION:\s*Final\[str\]\s*=\s*"([^"]+)"',
            backend,
            "backend handshake",
        ),
        "desktop handshake": _extract(
            r'^\s*private const string ClientVersion\s*=\s*"([^"]+)";',
            supervisor,
            "desktop handshake",
        ),
        "desktop assembly": _extract(
            r"<Version>([^<]+)</Version>",
            props,
            "desktop assembly",
        ),
    }
    return VersionSnapshot(expected=expected, actual=actual)


def check_versions(repo_root: Path) -> list[str]:
    snapshot = collect_versions(repo_root)
    return [
        f"{name}: {actual!r}, expected {snapshot.expected!r}"
        for name, actual in snapshot.mismatches.items()
    ]


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
    pyproject = repo_root / "pyproject.toml"
    source = pyproject.read_text(encoding="utf-8")
    changes[pyproject] = _replace_once(
        source,
        r'^(version\s*=\s*)"[^"]+"',
        rf'\g<1>"{version}"',
        "pyproject version",
    )
    for path in (
        repo_root / "desktop" / "web-grid" / "package.json",
        repo_root / "desktop" / "web-grid" / "package-lock.json",
    ):
        value = _json(path)
        value["version"] = version
        if path.name == "package-lock.json":
            root_package = value.get("packages", {}).get("")
            if not isinstance(root_package, dict):
                raise VersionError(f"{path} is missing packages['']")
            root_package["version"] = version
        changes[path] = _render_json(value)
    layout_path = repo_root / "desktop" / "publish-layout.json"
    layout = _json(layout_path)
    components = layout.setdefault("components", {})
    for name in ("host", "backend", "web"):
        components[name] = {"version": version}
    sidecar = components.setdefault("sidecar", {})
    sidecar["version"] = version
    changes[layout_path] = _render_json(layout)
    backend_path = repo_root / "backend" / "contracts" / "system.py"
    changes[backend_path] = _replace_once(
        backend_path.read_text(encoding="utf-8"),
        r'^(BACKEND_VERSION:\s*Final\[str\]\s*=\s*)"[^"]+"',
        rf'\g<1>"{version}"',
        "backend handshake",
    )
    supervisor = (
        repo_root
        / "desktop"
        / "src"
        / "VibeTable.Infrastructure"
        / "Backend"
        / "PythonBackendSupervisor.cs"
    )
    changes[supervisor] = _replace_once(
        supervisor.read_text(encoding="utf-8"),
        r'^(\s*private const string ClientVersion\s*=\s*)"[^"]+";',
        rf'\g<1>"{version}";',
        "desktop handshake",
    )
    props = repo_root / "desktop" / "Directory.Build.props"
    value = props.read_text(encoding="utf-8")
    value = _replace_once(
        value,
        r"<Version>[^<]+</Version>",
        f"<Version>{version}</Version>",
        "desktop version",
    )
    value = re.sub(
        r"<AssemblyVersion>[^<]+</AssemblyVersion>",
        f"<AssemblyVersion>{version}.0</AssemblyVersion>",
        value,
    )
    value = re.sub(
        r"<FileVersion>[^<]+</FileVersion>",
        f"<FileVersion>{version}.0</FileVersion>",
        value,
    )
    value = re.sub(
        r"<InformationalVersion>[^<]+</InformationalVersion>",
        f"<InformationalVersion>{version}</InformationalVersion>",
        value,
    )
    changes[props] = value
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
