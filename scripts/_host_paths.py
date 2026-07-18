"""Derive WPF host build/output paths from MSBuild project metadata.

The host's assembly name and target framework live in
``desktop/src/VibeTable.Desktop/VibeTable.Desktop.csproj`` (and the inherited
``desktop/Directory.Build.props``). Several scripts and tests need the resulting
``bin/<config>/<tfm>/<assembly>.exe`` path; centralizing the derivation here
keeps them in sync when the TFM or assembly name changes.

This module is pure: every function takes ``repo_root`` explicitly instead of
baking in a ``__file__``-relative root, because call sites compute the repo
root at different depths (``scripts/dev.py`` uses ``parents[1]``, the e2e test
under ``tests/e2e/`` uses ``parents[2]``). Parsing is intentionally a small
stdlib regex over static ``<Tag>value</Tag>`` props — MSBuild conditionals are
not supported (and not used by our props).
"""

from __future__ import annotations

import re
from pathlib import Path

HOST_PROJECT_NAME = "VibeTable.Desktop"
DEFAULT_TFM = "net10.0-windows"


def read_msbuild_property(name: str, *files: Path, fallback: str) -> str:
    """Return the first non-empty ``<name>...</name>`` value found in *files*.

    Files are scanned in order so an explicit project value wins over one
    inherited from ``Directory.Build.props``. *fallback* is returned when no
    file defines the property (or no file exists).
    """
    pattern = re.compile(rf"<{re.escape(name)}>\s*([^<]+?)\s*</{name}>")
    for f in files:
        if f.is_file():
            m = pattern.search(f.read_text(encoding="utf-8", errors="replace"))
            if m and m.group(1).strip():
                return m.group(1).strip()
    return fallback


def host_project_dir(repo_root: Path, host_project: Path | None = None) -> Path:
    """Return the WPF host project directory (default: desktop/src/<HOST_PROJECT_NAME>)."""
    if host_project is not None:
        return host_project
    return repo_root / "desktop" / "src" / HOST_PROJECT_NAME


def _props_files(repo_root: Path, host_project: Path | None) -> tuple[Path, Path]:
    """Return ``(csproj, directory_build_props)`` for the host project.

    Explicit csproj values win (scanned first); ``Directory.Build.props`` is the
    inherited fallback. The csproj path assumes the conventional ``<dir>.csproj``
    naming when multiple csproj files are absent — callers may pass an explicit
    ``host_project`` whose name differs; the csproj is then ``<dir>/<dir>.csproj``.
    """
    project = host_project_dir(repo_root, host_project)
    csproj = project / f"{project.name}.csproj"
    props = repo_root / "desktop" / "Directory.Build.props"
    return csproj, props


def host_assembly_name(repo_root: Path, host_project: Path | None = None) -> str:
    """Return the host's assembly name (exe stem).

    Reads ``<AssemblyName>`` from the csproj/props; falls back to the project
    directory name, which matches the MSBuild default when the property is unset.
    """
    csproj, props = _props_files(repo_root, host_project)
    project = host_project_dir(repo_root, host_project)
    return read_msbuild_property("AssemblyName", csproj, props, fallback=project.name)


def host_target_framework(repo_root: Path, host_project: Path | None = None) -> str:
    """Return the host's ``<TargetFramework>`` (e.g. ``net10.0-windows``)."""
    csproj, props = _props_files(repo_root, host_project)
    return read_msbuild_property("TargetFramework", csproj, props, fallback=DEFAULT_TFM)


def host_bin_exe(
    repo_root: Path,
    *,
    config: str = "Release",
    host_project: Path | None = None,
) -> Path:
    """Return ``<host_project>/bin/<config>/<tfm>/<assembly>.exe``.

    All three segments (assembly, tfm) are derived from project metadata so the
    path stays correct when the csproj/props change.
    """
    project = host_project_dir(repo_root, host_project)
    tfm = host_target_framework(repo_root, host_project)
    assembly = host_assembly_name(repo_root, host_project)
    return project / "bin" / config / tfm / f"{assembly}.exe"
