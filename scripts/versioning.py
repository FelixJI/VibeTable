"""VibeTable 跨组件版本读取、校验与更新工具。

``pyproject.toml`` 的 ``[project].version`` 是唯一版本源。其余文件是必须与它
保持一致的发布元数据；本模块集中维护这些目标，供发布、打包和质量脚本复用。
"""

from __future__ import annotations

import json
import os
import re
import tomllib
from dataclasses import dataclass
from pathlib import Path

SEMVER_RE = re.compile(r"^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)$")


class VersionError(ValueError):
    """版本格式、目标文件或跨组件一致性错误。"""


@dataclass(frozen=True)
class VersionSnapshot:
    expected: str
    actual: dict[str, str]

    @property
    def mismatches(self) -> dict[str, str]:
        return {name: value for name, value in self.actual.items() if value != self.expected}


def validate_version(value: str) -> str:
    """校验并返回严格的 ``MAJOR.MINOR.PATCH`` 版本。"""
    if not SEMVER_RE.fullmatch(value):
        raise VersionError(f"无效版本 {value!r}；必须使用 MAJOR.MINOR.PATCH")
    return value


def read_project_version(repo_root: Path) -> str:
    """从唯一版本源 ``pyproject.toml`` 读取项目版本。"""
    pyproject = repo_root / "pyproject.toml"
    with pyproject.open("rb") as stream:
        value = tomllib.load(stream).get("project", {}).get("version")
    if not isinstance(value, str):
        raise VersionError(f"{pyproject} 缺少 [project].version")
    return validate_version(value)


def _read_json(path: Path) -> dict:
    return json.loads(path.read_text(encoding="utf-8"))


def _discover_extension_dirs(repo_root: Path) -> list[Path]:
    """Return the ordered list of Directus extension source directories.

    G0.2: reads ``directus/extensions/manifest.json`` rather than hard-coding
    ``vibetable-bulk-mutation``. Falls back to the single directory if the manifest
    is missing (defensive).
    """
    manifest_path = repo_root / "directus" / "extensions" / "manifest.json"
    if manifest_path.is_file():
        data = _read_json(manifest_path)
        names = [ext["name"] for ext in data.get("extensions", []) if "name" in ext]
        if names:
            return [repo_root / "directus" / "extensions" / name for name in names]
    return [repo_root / "directus" / "extensions" / "vibetable-bulk-mutation"]


def _extract(pattern: str, text: str, label: str) -> str:
    match = re.search(pattern, text, flags=re.MULTILINE)
    if match is None:
        raise VersionError(f"无法从 {label} 读取版本")
    return match.group(1)


def collect_versions(repo_root: Path) -> VersionSnapshot:
    """读取所有发布组件版本；缺失目标会直接失败。"""
    expected = read_project_version(repo_root)
    web_package = _read_json(repo_root / "desktop" / "web-grid" / "package.json")
    web_lock = _read_json(repo_root / "desktop" / "web-grid" / "package-lock.json")
    extension_dirs = _discover_extension_dirs(repo_root)
    layout = _read_json(repo_root / "desktop" / "publish-layout.json")
    components = layout.get("components", {})

    backend_text = (repo_root / "backend" / "contracts" / "system.py").read_text(encoding="utf-8")
    supervisor_text = (
        repo_root
        / "desktop"
        / "src"
        / "VibeTable.Infrastructure"
        / "Backend"
        / "PythonBackendSupervisor.cs"
    ).read_text(encoding="utf-8")
    props_text = (repo_root / "desktop" / "Directory.Build.props").read_text(encoding="utf-8")

    actual = {
        "web/package.json": str(web_package.get("version", "")),
        "web/package-lock.json": str(web_lock.get("version", "")),
        "web/package-lock root": str(web_lock.get("packages", {}).get("", {}).get("version", "")),
    }
    # G0.2: read version from every declared extension's package files.
    for ext_dir in extension_dirs:
        label = f"directus/{ext_dir.name}"
        ext_package = _read_json(ext_dir / "package.json")
        ext_lock = _read_json(ext_dir / "package-lock.json")
        actual[f"{label}/package.json"] = str(ext_package.get("version", ""))
        actual[f"{label}/package-lock.json"] = str(ext_lock.get("version", ""))
        actual[f"{label}/package-lock root"] = str(
            ext_lock.get("packages", {}).get("", {}).get("version", "")
        )
    # Layout components: plural (authoritative) + singular (backward-compatible).
    plural = components.get("directusExtensions")
    if isinstance(plural, list):
        for ext in plural:
            if isinstance(ext, dict):
                actual[f"layout directusExtensions/{ext.get('name', '?')}"] = str(
                    ext.get("version", "")
                )
    actual["layout host"] = str(components.get("host", {}).get("version", ""))
    actual["layout backend"] = str(components.get("backend", {}).get("version", ""))
    actual["layout web"] = str(components.get("web", {}).get("version", ""))
    actual["layout directusExtension"] = str(
        components.get("directusExtension", {}).get("version", "")
    )
    actual["backend handshake"] = _extract(
        r'^BACKEND_VERSION:\s*Final\[str\]\s*=\s*"([^"]+)"',
        backend_text,
        "backend/contracts/system.py",
    )
    actual["desktop handshake"] = _extract(
        r'^\s*private const string ClientVersion\s*=\s*"([^"]+)";',
        supervisor_text,
        "PythonBackendSupervisor.cs",
    )
    actual["desktop assembly"] = _extract(
        r"<Version>([^<]+)</Version>", props_text, "desktop/Directory.Build.props"
    )
    return VersionSnapshot(expected=expected, actual=actual)


def check_versions(repo_root: Path) -> list[str]:
    """返回适合 CLI 展示的一致性错误列表。"""
    snapshot = collect_versions(repo_root)
    return [
        f"{name}: {actual!r}，期望 {snapshot.expected!r}"
        for name, actual in snapshot.mismatches.items()
    ]


def bump_version(current: str, part: str) -> str:
    """按 SemVer 的 major/minor/patch 递增版本。"""
    major, minor, patch = (int(value) for value in validate_version(current).split("."))
    if part == "major":
        return f"{major + 1}.0.0"
    if part == "minor":
        return f"{major}.{minor + 1}.0"
    if part == "patch":
        return f"{major}.{minor}.{patch + 1}"
    raise VersionError(f"未知递增类型: {part}")


def _replace_once(text: str, pattern: str, replacement: str, label: str) -> str:
    updated, count = re.subn(pattern, replacement, text, count=1, flags=re.MULTILINE)
    if count != 1:
        raise VersionError(f"无法更新 {label}")
    return updated


def _render_json(data: dict) -> str:
    return json.dumps(data, ensure_ascii=False, indent=2) + "\n"


def _updated_contents(repo_root: Path, version: str) -> dict[Path, str]:
    version = validate_version(version)
    changes: dict[Path, str] = {}

    pyproject = repo_root / "pyproject.toml"
    pyproject_text = pyproject.read_text(encoding="utf-8")
    project_match = re.search(r"(?ms)^\[project\]\s*$.*?(?=^\[|\Z)", pyproject_text)
    if project_match is None:
        raise VersionError("pyproject.toml 缺少 [project] 段")
    project_block = _replace_once(
        project_match.group(0),
        r'^version\s*=\s*"[^"]+"',
        f'version = "{version}"',
        "pyproject.toml [project].version",
    )
    changes[pyproject] = (
        pyproject_text[: project_match.start()]
        + project_block
        + pyproject_text[project_match.end() :]
    )

    json_targets = [
        repo_root / "desktop" / "web-grid" / "package.json",
        repo_root / "desktop" / "web-grid" / "package-lock.json",
    ]
    # G0.2: sync version in every declared extension's package files.
    for ext_dir in _discover_extension_dirs(repo_root):
        json_targets.append(ext_dir / "package.json")
        json_targets.append(ext_dir / "package-lock.json")
    for path in json_targets:
        data = _read_json(path)
        data["version"] = version
        if path.name == "package-lock.json":
            root_package = data.get("packages", {}).get("")
            if not isinstance(root_package, dict):
                raise VersionError(f"{path} 缺少 packages['']")
            root_package["version"] = version
        changes[path] = _render_json(data)

    layout_path = repo_root / "desktop" / "publish-layout.json"
    layout = _read_json(layout_path)
    components = layout.setdefault("components", {})
    for component in ("host", "backend", "web"):
        components[component] = {"version": version}
    # G0.2: write plural (authoritative) and singular (backward-compatible).
    ext_dirs = _discover_extension_dirs(repo_root)
    ext_names = [d.name for d in ext_dirs]
    components["directusExtensions"] = [{"name": name, "version": version} for name in ext_names]
    if ext_names:
        components["directusExtension"] = {"version": version}
    launch = layout.setdefault("launch", {})
    launch["directusExtensions"] = [f"directus/extensions/{name}" for name in ext_names]
    if ext_names:
        launch["directusExtension"] = f"directus/extensions/{ext_names[0]}"
    changes[layout_path] = _render_json(layout)

    backend_path = repo_root / "backend" / "contracts" / "system.py"
    changes[backend_path] = _replace_once(
        backend_path.read_text(encoding="utf-8"),
        r'^BACKEND_VERSION:\s*Final\[str\]\s*=\s*"[^"]+"',
        f'BACKEND_VERSION: Final[str] = "{version}"',
        str(backend_path),
    )

    supervisor_path = (
        repo_root
        / "desktop"
        / "src"
        / "VibeTable.Infrastructure"
        / "Backend"
        / "PythonBackendSupervisor.cs"
    )
    changes[supervisor_path] = _replace_once(
        supervisor_path.read_text(encoding="utf-8"),
        r'^(\s*private const string ClientVersion\s*=\s*)"[^"]+";',
        rf'\g<1>"{version}";',
        str(supervisor_path),
    )

    props_path = repo_root / "desktop" / "Directory.Build.props"
    props_text = props_path.read_text(encoding="utf-8")
    if "<Version>" in props_text:
        props_text = _replace_once(
            props_text,
            r"<Version>[^<]+</Version>",
            f"<Version>{version}</Version>",
            str(props_path),
        )
    else:
        props_text = _replace_once(
            props_text,
            r"(\s*</PropertyGroup>)",
            f"\n    <Version>{version}</Version>\n    <AssemblyVersion>{version}.0</AssemblyVersion>\n"
            f"    <FileVersion>{version}.0</FileVersion>\n    <InformationalVersion>{version}</InformationalVersion>\n  </PropertyGroup>",
            str(props_path),
        )
    for tag in ("AssemblyVersion", "FileVersion", "InformationalVersion"):
        tag_value = f"{version}.0" if tag != "InformationalVersion" else version
        props_text = re.sub(rf"<{tag}>[^<]+</{tag}>", f"<{tag}>{tag_value}</{tag}>", props_text)
    changes[props_path] = props_text
    return changes


def update_versions(repo_root: Path, version: str, *, dry_run: bool = False) -> list[Path]:
    """同步所有版本目标；写入失败时恢复已经替换的文件。"""
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
