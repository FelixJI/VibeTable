"""Unified VibeTable Next build orchestrator (Task 11).

Produces a single ``dist/VibeTable.Next/`` layout containing the .NET host,
Python backend, web-grid, deployable Directus extension, and the local
Directus 12 runtime source (single-machine). All components are stamped from
``pyproject.toml`` and recorded in ``publish-layout.json``.

Design contract (verbatim from the Task 11 brief):

* Pure command-construction functions return command arrays; they do NOT
  execute anything. The tests assert the arrays byte-for-byte against the
  brief, so the exact flags must match:

      npm ci
      npm run build
      dotnet publish desktop/src/VibeTable.Desktop/VibeTable.Desktop.csproj
          --configuration Release --runtime win-x64 --self-contained true
      python -m PyInstaller --onedir --name vibetable-backend
          --hidden-import=<runtime-submodule> backend/__main__.py

* ``--skip-web`` / ``--skip-backend`` / ``--skip-desktop`` / ``--skip-directus``
  / ``--skip-local-directus`` are dev flags. They are REJECTED when combined
  with ``--release`` (release builds must never ship a partial layout).

* The build is ATOMIC with respect to the published directory: every stage
  runs into ``dist/.VibeTable.Next.staging`` first; only after every requested stage
  succeeds AND the manifest verifies do we atomically replace
  ``dist/VibeTable.Next`` (rename/replace). On any failure the previously
  published directory is left untouched.

* The version manifest is ``publish-layout.json`` with
  ``protocolVersion=1.0``, WebView2 SDK ``1.0.4078.44``, Tabulator ``6.5.2``,
  and relative launch paths. The host reads it at startup and refuses mixed
  versions.

This module is import-safe: importing it has no side effects. ``main()``
does the work.
"""

from __future__ import annotations

import argparse
import json
import os
import shutil
import subprocess
import sys
from dataclasses import dataclass, field
from pathlib import Path

try:
    from scripts._host_paths import host_assembly_name
    from scripts.extension_manifest import list_extensions
    from scripts.versioning import check_versions, read_project_version
except ModuleNotFoundError:  # direct execution: python scripts/build_next.py
    from _host_paths import host_assembly_name
    from extension_manifest import list_extensions
    from versioning import check_versions, read_project_version

# ---------------------------------------------------------------------------
# Pinned protocol/toolchain versions. Application component versions are read
# from pyproject.toml and verified by scripts.versioning before any build work.
# ---------------------------------------------------------------------------

PROTOCOL_VERSION = "1.0"
WEBVIEW2_SDK = "1.0.4078.44"
TABULATOR_VERSION = "6.5.2"

HOST_EXE_NAME = "VibeTable.Next.exe"
BACKEND_EXE_NAME = "vibetable-backend.exe"
BACKEND_DIR_NAME = "backend"
WEB_GRID_DIR_NAME = "web-grid"
LOCAL_DIRECTUS_DIR_NAME = "local-directus"
MANIFEST_NAME = "publish-layout.json"
PREFERRED_DOTNET = Path(r"C:\Program Files\dotnet\dotnet.exe")

# Hidden imports PyInstaller cannot always infer statically for the backend's
# runtime dependencies. These are the transitive submodules the BFF actually
# needs; listed explicitly so a cold PyInstaller bundle resolves them without
# running the app first.
BACKEND_HIDDEN_IMPORTS = (
    "pydantic",
    "pydantic.deprecated.decorator",
    "openpyxl",
    "openpyxl.workbook",
    "websockets",
)

#: Dev/test packages that must never be bundled into the shipped backend. The
#: post-build verify scans the onedir's _internal/ for these as a guard against
#: an accidental collect-all.
_DEV_PACKAGES_FORBIDDEN_IN_BUNDLE = frozenset({"mypy", "numpy", "pandas", "pytest", "_pytest"})

#: Dev-only skip flags. They MUST be rejected when combined with --release.
DEV_SKIP_FLAGS = (
    "--skip-web",
    "--skip-backend",
    "--skip-desktop",
    "--skip-directus",
    "--skip-local-directus",
)


# ---------------------------------------------------------------------------
# Repo path bundle
# ---------------------------------------------------------------------------


@dataclass
class RepoPaths:
    """All the absolute paths ``build_next`` cares about, in one place."""

    repo_root: Path
    web_grid_dir: Path  # desktop/web-grid (source)
    directus_extension_dirs: list[Path]  # Directus extension sources (multi)
    local_directus_source_dir: Path  # scripts/local_directus (source)
    desktop_csproj: Path  # desktop/src/VibeTable.Desktop/VibeTable.Desktop.csproj
    backend_main: Path  # backend/__main__.py
    staging_root: Path  # dist/.VibeTable.Next.staging (same-parent atomic swap)
    scratch_root: Path  # build/next-scratch (intermediate build output)
    publish_root: Path  # dist/VibeTable.Next
    host_exe: Path = field(init=False)
    backend_dir: Path = field(init=False)
    web_grid_publish_dir: Path = field(init=False)
    directus_extensions_publish_dir: Path = field(init=False)
    local_directus_publish_dir: Path = field(init=False)
    manifest_path: Path = field(init=False)

    def __post_init__(self) -> None:
        # Sibling paths INSIDE the publish/staging layout.
        self.host_exe = self.publish_root / HOST_EXE_NAME
        self.backend_dir = self.publish_root / BACKEND_DIR_NAME
        self.web_grid_publish_dir = self.publish_root / WEB_GRID_DIR_NAME
        self.directus_extensions_publish_dir = self.publish_root / "directus" / "extensions"
        self.local_directus_publish_dir = self.publish_root / LOCAL_DIRECTUS_DIR_NAME
        self.manifest_path = self.publish_root / MANIFEST_NAME

    @property
    def directus_extension_publish_dirs(self) -> list[Path]:
        """Per-extension publish targets, one per manifest entry."""
        return [self.directus_extensions_publish_dir / d.name for d in self.directus_extension_dirs]

    @classmethod
    def default(cls, repo_root: Path) -> RepoPaths:
        repo_root = repo_root.resolve()
        extensions_root = repo_root / "directus" / "extensions"
        extension_dirs = [extensions_root / e.name for e in list_extensions(repo_root)]
        return cls(
            repo_root=repo_root,
            web_grid_dir=repo_root / "desktop" / "web-grid",
            directus_extension_dirs=extension_dirs,
            local_directus_source_dir=repo_root / "scripts" / "local_directus",
            desktop_csproj=(
                repo_root / "desktop" / "src" / "VibeTable.Desktop" / "VibeTable.Desktop.csproj"
            ),
            backend_main=repo_root / "backend" / "__main__.py",
            staging_root=repo_root / "dist" / ".VibeTable.Next.staging",
            scratch_root=repo_root / "build" / "next-scratch",
            publish_root=repo_root / "dist" / "VibeTable.Next",
        )

    def staging_mirror(self) -> RepoPaths:
        """Return a copy whose publish_* targets point at the staging root.

        Used while staging: we build into ``staging_root`` (treating it as
        the publish layout), verify it, then atomically swap it into place.
        The ``scratch_root`` is carried over so intermediate PyInstaller/dotnet
        output stays OUT of the staging (publish) subtree.
        """
        return RepoPaths(
            repo_root=self.repo_root,
            web_grid_dir=self.web_grid_dir,
            directus_extension_dirs=list(self.directus_extension_dirs),
            local_directus_source_dir=self.local_directus_source_dir,
            desktop_csproj=self.desktop_csproj,
            backend_main=self.backend_main,
            staging_root=self.staging_root,
            scratch_root=self.scratch_root,
            publish_root=self.staging_root,
        )


# ---------------------------------------------------------------------------
# Step 1: pure command-construction functions (NO execution)
# ---------------------------------------------------------------------------


def build_npm_ci_command(paths: RepoPaths) -> list[str]:
    """Return the ``npm ci`` command array.

    Run from ``desktop/web-grid`` (the caller supplies the cwd). ``npm ci``
    takes no project-specific arguments — the pinned versions live in
    ``package-lock.json``.
    """
    return ["npm", "ci"]


def build_npm_build_command(paths: RepoPaths) -> list[str]:
    """Return the ``npm run build`` command array.

    Runs ``tsc --noEmit && vite build`` (per ``desktop/web-grid/package.json``)
    and emits ``desktop/web-grid/dist/``.
    """
    return ["npm", "run", "build"]


def build_dotnet_publish_command(
    paths: RepoPaths, output_dir: str | os.PathLike[str] | None = None
) -> list[str]:
    """Return the ``dotnet publish`` command array for the WPF host.

    Brief verbatim base flags::

        dotnet publish desktop/src/VibeTable.Desktop/VibeTable.Desktop.csproj
            --configuration Release --runtime win-x64 --self-contained true

    When ``output_dir`` is supplied (staging), ``--output <dir>`` is appended
    so the publish result lands inside the staging tree.
    """
    cmd: list[str] = [
        "dotnet",
        "publish",
        str(paths.desktop_csproj),
        "--configuration",
        "Release",
        "--runtime",
        "win-x64",
        "--self-contained",
        "true",
    ]
    if output_dir is not None:
        cmd += ["--output", os.fsdecode(Path(output_dir))]
    return cmd


def build_pyinstaller_backend_command(
    paths: RepoPaths, output_dir: str | os.PathLike[str]
) -> list[str]:
    """Return the PyInstaller onedir command array for the backend.

    Produces a self-contained ``vibetable-backend/`` directory (onedir) holding
    ``vibetable-backend.exe`` + ``_internal/``. The host spawns that exe as its
    BFF child. Uses the SAME interpreter that invoked this script
    (``sys.executable``) so the build is reproducible.

    ``--onedir`` (not ``--onefile``) is deliberate: the host treats the backend
    as a directory it locates by manifest path, and a directory layout is also
    what the package-integrity checks (and debugging) need.
    """
    out = os.fsdecode(Path(output_dir))
    workpath = os.fsdecode(Path(output_dir).parent / "_pyinstaller_build")
    command: list[str] = [
        sys.executable,
        "-m",
        "PyInstaller",
        "--noconfirm",
        "--clean",
        "--distpath",
        out,
        "--workpath",
        workpath,
        "--specpath",
        workpath,
        "--name",
        BACKEND_EXE_NAME[:-4],  # strip ".exe"; PyInstaller appends it
        "--onedir",
        # Console build: the backend is a stdio JSON-RPC server; it must never
        # pop a window, and a console subsystem would do that on Windows.
        "--console",
    ]
    for hidden in BACKEND_HIDDEN_IMPORTS:
        command.extend(["--hidden-import", hidden])
    # Collect pydantic's data files (py.typed) so the bundle is self-contained.
    command.extend(["--collect-data", "pydantic"])
    # The entrypoint: backend/__main__.py (run as a module via -m in dev).
    command.append(str(paths.backend_main))
    return command


# ---------------------------------------------------------------------------
# Step 3: manifest
# ---------------------------------------------------------------------------


def render_manifest(paths: RepoPaths) -> str:
    """Render the publish-layout manifest as a JSON string.

    The manifest is the single source of truth for the host: it records the
    component versions, the pinned protocol/SDK/library versions, and the
    RELATIVE launch paths the host resolves against ``AppContext.BaseDirectory``.

    G0.2: emits ``directusExtensions[]`` (plural) listing every extension from
    the extension manifest, and retains ``directusExtension`` (singular) for
    one release cycle of backward-compatible reads by older hosts.
    """
    version = read_project_version(paths.repo_root)
    extension_names = [d.name for d in paths.directus_extension_dirs]
    components: dict[str, object] = {
        "host": {"version": version},
        "backend": {"version": version},
        "web": {"version": version},
        "localDirectus": {"version": version},
    }
    # Plural: every extension in the manifest, each with its own version.
    components["directusExtensions"] = [
        {"name": name, "version": version} for name in extension_names
    ]
    # Singular (backward-compatible): the first extension. Older hosts that
    # only know ``directusExtension`` continue to resolve during the cutover.
    if extension_names:
        components["directusExtension"] = {"version": version}

    launch: dict[str, object] = {
        "backend": f"{BACKEND_DIR_NAME}/{BACKEND_EXE_NAME}",
        "webGrid": WEB_GRID_DIR_NAME,
        # Plural: relative paths to every extension's deployable directory.
        "directusExtensions": [f"directus/extensions/{name}" for name in extension_names],
        # local-directus ships the npm manifest + lockfile + env template only.
        # node_modules is pulled at first launch by the host's package manager
        # (app-private, online) so the installer stays small and native modules
        # are built against the customer's Node ABI.
        "localDirectus": LOCAL_DIRECTUS_DIR_NAME,
    }
    # Singular launch path (backward-compatible): first extension.
    if extension_names:
        launch["directusExtension"] = f"directus/extensions/{extension_names[0]}"

    data = {
        "protocolVersion": PROTOCOL_VERSION,
        "components": components,
        "webview2": {"sdk": WEBVIEW2_SDK},
        "webGrid": {"tabulator": TABULATOR_VERSION},
        # Relative paths — host resolves them against the exe directory.
        "launch": launch,
    }
    return json.dumps(data, indent=2) + "\n"


def write_manifest(paths: RepoPaths) -> Path:
    """Write the manifest into ``paths.manifest_path`` and return it."""
    paths.manifest_path.parent.mkdir(parents=True, exist_ok=True)
    paths.manifest_path.write_text(render_manifest(paths), encoding="utf-8")
    return paths.manifest_path


# ---------------------------------------------------------------------------
# Arg parsing (with the dev-skip-vs-release guard)
# ---------------------------------------------------------------------------


def _build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        prog="build_next.py",
        description=(
            "Build the unified VibeTable.Next layout (host + backend + web-grid) from one version set."
        ),
    )
    parser.add_argument(
        "--release",
        action="store_true",
        help=(
            "Release mode: every stage must run and succeed. The dev skip "
            "flags are rejected in this mode."
        ),
    )
    parser.add_argument(
        "--skip-web",
        action="store_true",
        help=("DEV ONLY: skip the web-grid npm ci/build stage. Rejected with --release."),
    )
    parser.add_argument(
        "--skip-backend",
        action="store_true",
        help=("DEV ONLY: skip the PyInstaller backend stage. Rejected with --release."),
    )
    parser.add_argument(
        "--skip-desktop",
        action="store_true",
        help=("DEV ONLY: skip the dotnet publish host stage. Rejected with --release."),
    )
    parser.add_argument(
        "--skip-directus",
        action="store_true",
        help=("DEV ONLY: skip the Directus extension stage. Rejected with --release."),
    )
    parser.add_argument(
        "--skip-local-directus",
        action="store_true",
        help=("DEV ONLY: skip the local-Directus runtime stage. Rejected with --release."),
    )
    parser.add_argument(
        "--keep-staging",
        action="store_true",
        help=("DEV ONLY: keep build/next-staging after a successful swap (useful for diffing)."),
    )
    return parser


def parse_args(argv: list[str] | None = None) -> argparse.Namespace:
    """Parse argv, enforcing: dev-skip flags are rejected with --release.

    Raises ``SystemExit(2)`` (argparse error) if any ``--skip-*`` flag is
    combined with ``--release``.
    """
    parser = _build_parser()
    ns = parser.parse_args(argv)
    if ns.release:
        offenders = [
            f
            for f, on in (
                ("--skip-web", ns.skip_web),
                ("--skip-backend", ns.skip_backend),
                ("--skip-desktop", ns.skip_desktop),
                ("--skip-directus", ns.skip_directus),
                ("--skip-local-directus", ns.skip_local_directus),
            )
            if on
        ]
        if offenders:
            parser.error(
                "release builds must not skip any stage; remove " + " and ".join(offenders)
            )
    return ns


# ---------------------------------------------------------------------------
# Step 2: atomic staging build
# ---------------------------------------------------------------------------


class BuildError(RuntimeError):
    """Raised when a build stage fails. The previous layout is preserved."""


def _resolve_executable(name: str) -> str:
    """Resolve a bare command name to an executable path.

    On Windows, ``npm`` / ``dotnet`` are typically shipped as ``.cmd`` shims;
    ``subprocess.run`` does NOT consult PATHEXT for bare names, so a naive
    ``["npm", "ci"]`` raises ``FileNotFoundError``. ``shutil.which`` DOES
    respect PATHEXT, so we use it to find the real launcher. The command
    ARRAY stays verbatim per the brief; only the executor resolves the binary.

    For absolute paths or ``sys.executable`` we return as-is.
    """
    if name.lower() == "dotnet" and PREFERRED_DOTNET.is_file():
        return str(PREFERRED_DOTNET)
    if os.path.sep in name or (os.path.altsep and os.path.altsep in name):
        return name  # already a path
    resolved = shutil.which(name)
    return resolved or name


def _run(cmd: list[str], cwd: Path) -> None:
    """Run ``cmd`` in ``cwd``, streaming output to the parent stdout/stderr.

    Raises :class:`BuildError` on non-zero exit so callers can leave the
    previously published directory untouched. The first element of ``cmd``
    is resolved via PATHEXT (Windows) so ``npm`` / ``dotnet`` shims work
    without changing the verbatim command array.
    """
    resolved = [_resolve_executable(cmd[0]), *cmd[1:]] if cmd else cmd
    print(f"[build_next] $ {' '.join(cmd)}  (cwd={cwd})", flush=True)
    try:
        subprocess.run(resolved, cwd=str(cwd), check=True)
    except subprocess.CalledProcessError as exc:
        raise BuildError(f"command failed (exit {exc.returncode}): {' '.join(cmd)}") from exc
    except FileNotFoundError as exc:
        raise BuildError(f"command not found: {cmd[0] if cmd else '?'}") from exc


def _build_web_stage(stage: RepoPaths, src_web_grid: Path, skip: bool) -> None:
    """npm ci + npm run build, then copy dist/ into ``stage.web_grid_publish_dir``."""
    if skip:
        print("[build_next] skipping web-grid stage (--skip-web)")
        return
    if not src_web_grid.is_dir():
        raise BuildError(f"web-grid source missing: {src_web_grid}")
    _run(build_npm_ci_command(stage), cwd=src_web_grid)
    _run(build_npm_build_command(stage), cwd=src_web_grid)
    dist = src_web_grid / "dist"
    if not dist.is_dir():
        raise BuildError(f"npm build did not produce {dist} — web-grid stage incomplete")
    # Copy the built web assets into the staging publish layout.
    if stage.web_grid_publish_dir.exists():
        shutil.rmtree(stage.web_grid_publish_dir)
    shutil.copytree(dist, stage.web_grid_publish_dir)
    print(
        f"[build_next] web-grid copied -> {stage.web_grid_publish_dir}",
        flush=True,
    )


def _build_directus_stage(stage: RepoPaths, skip: bool) -> None:
    """构建并复制可直接部署的 Directus 扩展目录（多扩展）。"""
    if skip:
        print("[build_next] skipping Directus extension stage (--skip-directus)")
        return
    if not stage.directus_extension_dirs:
        raise BuildError("no Directus extensions declared in the extension manifest")
    # Shared runtime resources consumed by both the packaged backend and the
    # local Directus bootstrapper. Shipping only the extension(s) made a
    # release unable to load its capability contract or seed a blank instance.
    # These are copied once, outside the per-extension loop.
    publish_directus_root = stage.directus_extensions_publish_dir.parent
    directus_root = stage.directus_extension_dirs[0].parents[1]
    for resource_dir in ("blueprints", "capabilities"):
        source_resource = directus_root / resource_dir
        target_resource = publish_directus_root / resource_dir
        if target_resource.exists():
            shutil.rmtree(target_resource)
        shutil.copytree(source_resource, target_resource)

    for source in stage.directus_extension_dirs:
        package_json = source / "package.json"
        if not package_json.is_file():
            raise BuildError(f"Directus extension package missing: {package_json}")
        print(f"[build_next] building Directus extension: {source.name}", flush=True)
        _run(build_npm_ci_command(stage), cwd=source)
        _run(build_npm_build_command(stage), cwd=source)
        dist = source / "dist"
        if not dist.is_dir():
            raise BuildError(f"Directus extension {source.name!r} build did not produce {dist}")

        target = stage.directus_extensions_publish_dir / source.name
        if target.exists():
            shutil.rmtree(target)
        target.mkdir(parents=True)
        shutil.copy2(package_json, target / "package.json")
        shutil.copytree(dist, target / "dist")
        readme = source / "README.md"
        if readme.is_file():
            shutil.copy2(readme, target / "README.md")
        print(f"[build_next] Directus extension staged -> {target}", flush=True)


# Files shipped from scripts/local_directus into the installer. The runtime's
# per-machine artifacts (downloaded/compiled at first launch) are deliberately
# excluded so the installer stays small and native modules target the
# customer's own Node ABI.
_LOCAL_DIRECTUS_SHIPPED_FILES = (
    "package.json",
    "package-lock.json",
    ".env.template",
)


def _build_local_directus_stage(stage: RepoPaths, skip: bool) -> None:
    """Stage the local-Directus runtime source into ``local-directus/``.

    Ships only what the host needs to introduce Directus at first launch: the
    npm manifest + lockfile + env template. The host's package manager pulls
    Directus online into this app-private directory; no run.py/install.py is
    shipped (the host drives Directus directly). ``node_modules`` and every
    runtime artifact (``.npm-cache``, ``.npm-prefix``, ``data/``, ``uploads/``,
    ``extensions/``, ``.env``, markers) are excluded.
    """
    if skip:
        print("[build_next] skipping local-directus stage (--skip-local-directus)")
        return
    source = stage.local_directus_source_dir
    if not source.is_dir():
        raise BuildError(f"local-directus source missing: {source}")
    package_json = source / "package.json"
    if not package_json.is_file():
        raise BuildError(f"local-directus source incomplete (need package.json): {source}")

    target = stage.local_directus_publish_dir
    if target.exists():
        shutil.rmtree(target)
    target.mkdir(parents=True)
    for name in _LOCAL_DIRECTUS_SHIPPED_FILES:
        src_file = source / name
        if src_file.is_file():
            shutil.copy2(src_file, target / name)
    print(
        f"[build_next] local-directus staged -> {target} "
        f"(source-only; node_modules pulled at first launch)",
        flush=True,
    )


def _build_backend_stage(stage: RepoPaths, skip: bool) -> None:
    """PyInstaller onedir backend -> ``stage.backend_dir/vibetable-backend.exe``.

    PyInstaller emits ``vibetable-backend/`` (the onedir: exe + ``_internal/``)
    under the distpath; we relocate it to ``backend/`` so the host finds
    ``backend/vibetable-backend.exe``.
    """
    if skip:
        print("[build_next] skipping backend stage (--skip-backend)")
        return
    if not stage.backend_main.is_file():
        raise BuildError(f"backend entrypoint missing: {stage.backend_main}")
    # Build into a SCRATCH dir (sibling of staging, NOT inside the publish
    # subtree) then relocate to <publish>/backend so the scratch artifacts
    # never leak into the published layout.
    scratch = stage.scratch_root / "_backend_build"
    scratch.mkdir(parents=True, exist_ok=True)
    _run(
        build_pyinstaller_backend_command(stage, output_dir=scratch),
        cwd=stage.repo_root,
    )
    produced = scratch / BACKEND_EXE_NAME[:-4]  # "vibetable-backend" (no .exe)
    if not produced.is_dir():
        raise BuildError(f"PyInstaller did not produce {produced} — backend stage incomplete")
    # Relocate to <publish>/backend so the manifest's relative path resolves.
    target = stage.backend_dir
    if target.exists():
        shutil.rmtree(target)
    shutil.move(str(produced), str(target))
    # Sanity: the exe must be where the manifest says it is.
    if not (target / BACKEND_EXE_NAME).is_file():
        raise BuildError(f"expected {target / BACKEND_EXE_NAME} after PyInstaller relocate")
    print(f"[build_next] backend staged -> {target}", flush=True)


def _build_desktop_stage(stage: RepoPaths, skip: bool) -> None:
    """dotnet publish self-contained -> ``stage.publish_root``."""
    if skip:
        print("[build_next] skipping desktop stage (--skip-desktop)")
        return
    if not stage.desktop_csproj.is_file():
        raise BuildError(f"desktop csproj missing: {stage.desktop_csproj}")
    # Publish into a SCRATCH dir (sibling of staging, NOT inside the publish
    # subtree) so the raw dotnet output doesn't pollute the published layout.
    # We then relocate only the host exe (+ WebView2 runtime siblings) the
    # manifest expects. dotnet names the host exe after the assembly
    # (resolved from the csproj); we rename to VibeTable.Next.exe.
    out = stage.scratch_root / "_desktop_publish"
    out.mkdir(parents=True, exist_ok=True)
    _run(
        build_dotnet_publish_command(stage, output_dir=out),
        cwd=stage.repo_root,
    )
    # Locate the produced host exe. The primary candidate is the assembly
    # name (resolved from project metadata); HOST_EXE_NAME is kept as a
    # defensive fallback for any renamed publish output.
    primary_name = f"{host_assembly_name(stage.repo_root, stage.desktop_csproj.parent)}.exe"
    host_src = None
    for candidate_name in (primary_name, HOST_EXE_NAME):
        cand = out / candidate_name
        if cand.is_file():
            host_src = cand
            break
    if host_src is None:
        raise BuildError(f"dotnet publish did not produce a host exe under {out}")
    target = stage.publish_root / HOST_EXE_NAME
    target.parent.mkdir(parents=True, exist_ok=True)
    shutil.copy2(host_src, target)
    # Carry the WebView2 runtime & supporting files alongside the host so the
    # self-contained app actually runs. Copy the whole publish output except
    # the host exe we already moved (avoid double-copy).
    for item in out.iterdir():
        if item.name == host_src.name:
            continue
        dest = stage.publish_root / item.name
        if dest.exists():
            continue
        if item.is_dir():
            shutil.copytree(item, dest)
        else:
            shutil.copy2(item, dest)
    print(f"[build_next] host staged -> {target}", flush=True)


def _verify_stage(
    stage: RepoPaths,
    skip_web: bool,
    skip_backend: bool,
    skip_desktop: bool,
    skip_directus: bool,
    skip_local_directus: bool,
) -> None:
    """Assert every required artifact exists before the atomic swap."""
    if not skip_desktop and not stage.host_exe.is_file():
        raise BuildError(f"missing host exe: {stage.host_exe}")
    if not skip_backend and not (stage.backend_dir / BACKEND_EXE_NAME).is_file():
        raise BuildError(f"missing backend exe: {stage.backend_dir / BACKEND_EXE_NAME}")
    if not skip_backend:
        # PyInstaller onedir nests packages under _internal/. Scan it for the
        # dev/test packages that must never ship (PyInstaller only bundles what
        # is imported, but this is a belt-and-braces guard against an accidental
        # collect-all that drags them in).
        forbidden = set()
        scan_root = stage.backend_dir / "_internal"
        if scan_root.is_dir():
            for child in scan_root.iterdir():
                if child.name in _DEV_PACKAGES_FORBIDDEN_IN_BUNDLE:
                    forbidden.add(child.name)
        if forbidden:
            raise BuildError(
                "backend contains forbidden development/optional packages: "
                + ", ".join(sorted(forbidden))
            )
    if not skip_web and not stage.web_grid_publish_dir.is_dir():
        raise BuildError(f"missing web-grid dir: {stage.web_grid_publish_dir}")
    if not skip_directus:
        for ext_source, ext_target in zip(
            stage.directus_extension_dirs, stage.directus_extension_publish_dirs, strict=True
        ):
            extension_entry = ext_target / "dist" / "index.js"
            if not extension_entry.is_file():
                raise BuildError(
                    f"missing Directus extension entry ({ext_source.name}): {extension_entry}"
                )
        directus_root = stage.directus_extensions_publish_dir.parent
        required_resources = (
            directus_root / "blueprints" / "vibetable-empty.json",
            directus_root / "capabilities" / "vibetable-empty-capabilities.json",
        )
        for resource in required_resources:
            if not resource.is_file():
                raise BuildError(f"missing Directus runtime resource: {resource}")
    if not skip_local_directus:
        run_py = stage.local_directus_publish_dir / "run.py"
        if not run_py.is_file():
            raise BuildError(f"missing local-directus run.py: {run_py}")
        # node_modules must NOT be shipped (it is pulled online at first launch).
        node_modules = stage.local_directus_publish_dir / "node_modules"
        if node_modules.is_dir():
            raise BuildError(
                f"local-directus must ship source-only: {node_modules} leaked into the layout"
            )
        if not skip_backend and not (stage.backend_dir / BACKEND_EXE_NAME).is_file():
            raise BuildError("local-directus packaged runner requires the backend executable")
    if not stage.manifest_path.is_file():
        raise BuildError(f"missing manifest: {stage.manifest_path}")
    # Manifest must round-trip with the pinned versions.
    data = json.loads(stage.manifest_path.read_text(encoding="utf-8"))
    if data.get("protocolVersion") != PROTOCOL_VERSION:
        raise BuildError(f"manifest protocolVersion mismatch: {data.get('protocolVersion')!r}")
    if data.get("webview2", {}).get("sdk") != WEBVIEW2_SDK:
        raise BuildError("manifest WebView2 SDK version drifted")
    if data.get("webGrid", {}).get("tabulator") != TABULATOR_VERSION:
        raise BuildError("manifest Tabulator version drifted")
    expected_version = read_project_version(stage.repo_root)
    components = data.get("components", {})
    # Scalar components must all match the project version.
    component_versions = {
        name: details.get("version")
        for name, details in components.items()
        if isinstance(details, dict)
    }
    expected_scalar_components = {"host", "backend", "web", "localDirectus"}
    # ``directusExtension`` (singular) is backward-compatible and optional;
    # the authoritative form is ``directusExtensions[]`` (plural).
    if "directusExtension" in component_versions:
        expected_scalar_components = expected_scalar_components | {"directusExtension"}
    missing_scalars = expected_scalar_components - set(component_versions)
    if missing_scalars:
        raise BuildError(f"manifest component set is incomplete (missing: {missing_scalars})")
    if any(value != expected_version for value in component_versions.values()):
        raise BuildError("manifest component version drifted")
    # G0.2: verify every declared extension in directusExtensions[] matches.
    plural = components.get("directusExtensions")
    if not isinstance(plural, list) or not plural:
        raise BuildError("manifest must contain a non-empty directusExtensions[] array")
    for ext in plural:
        if not isinstance(ext, dict) or ext.get("version") != expected_version:
            raise BuildError(f"manifest directusExtensions entry version drifted: {ext!r}")
    expected_ext_names = [d.name for d in stage.directus_extension_dirs]
    actual_ext_names = [ext.get("name") for ext in plural]
    if actual_ext_names != expected_ext_names:
        raise BuildError(
            f"manifest directusExtensions names mismatch: "
            f"{actual_ext_names} != {expected_ext_names}"
        )


def _atomic_swap(staging: Path, publish: Path) -> None:
    """Atomically replace ``publish`` with ``staging``.

    Strategy: rename the staging dir to the publish path. If a previous
    publish dir exists, move it aside FIRST (to a sibling backup), then
    rename staging into place, then delete the backup. The window during
    which no publish dir exists is bounded by a single rename — on Windows
    ``os.replace`` is atomic for directories on the same volume.

    On any exception the previously-published directory is left untouched
    (the backup is restored if the final rename failed).
    """
    publish.parent.mkdir(parents=True, exist_ok=True)
    backup: Path | None = None
    if publish.exists():
        backup = publish.with_name(publish.name + ".prev")
        if backup.exists():
            shutil.rmtree(backup)
        os.replace(publish, backup)
    try:
        os.replace(staging, publish)
    except OSError:
        # Restore the previous publish dir if we displaced it.
        if backup is not None and backup.exists():
            os.replace(backup, publish)
        raise
    if backup is not None and backup.exists():
        shutil.rmtree(backup, ignore_errors=True)


def run_build(
    paths: RepoPaths,
    *,
    release: bool,
    skip_web: bool,
    skip_backend: bool,
    skip_desktop: bool,
    skip_directus: bool,
    skip_local_directus: bool,
    keep_staging: bool,
) -> int:
    """Run the full staged build and atomic swap. Returns a process exit code."""
    try:
        version_errors = check_versions(paths.repo_root)
    except (OSError, ValueError) as exc:
        print(f"[build_next] unable to validate versions: {exc}", file=sys.stderr)
        return 1
    if version_errors:
        print("[build_next] version consistency check failed:", file=sys.stderr)
        for error in version_errors:
            print(f"  - {error}", file=sys.stderr)
        return 1

    # Always start from clean staging + scratch roots so partial leftovers
    # never leak into a "successful" publish.
    for root in (paths.staging_root, paths.scratch_root):
        if root.exists():
            shutil.rmtree(root)
        root.mkdir(parents=True, exist_ok=True)

    stage = paths.staging_mirror()
    try:
        _build_web_stage(stage, paths.web_grid_dir, skip=skip_web)
        _build_directus_stage(stage, skip=skip_directus)
        _build_local_directus_stage(stage, skip=skip_local_directus)
        _build_backend_stage(stage, skip=skip_backend)
        _build_desktop_stage(stage, skip=skip_desktop)
        write_manifest(stage)
        _verify_stage(
            stage,
            skip_web=skip_web,
            skip_backend=skip_backend,
            skip_desktop=skip_desktop,
            skip_directus=skip_directus,
            skip_local_directus=skip_local_directus,
        )
    except BuildError as exc:
        print(f"[build_next] FAILED, leaving dist/VibeTable.Next untouched: {exc}", file=sys.stderr)
        return 1

    # All stages verified -> atomic swap into dist/VibeTable.Next.
    try:
        _atomic_swap(paths.staging_root, paths.publish_root)
    except OSError as exc:
        print(f"[build_next] atomic swap failed: {exc}", file=sys.stderr)
        return 1

    # Drop the scratch tree (intermediate build output) on success unless the
    # caller asked to keep it for debugging.
    if not keep_staging and paths.scratch_root.exists():
        shutil.rmtree(paths.scratch_root, ignore_errors=True)

    print(
        f"[build_next] OK -> published {paths.publish_root} "
        f"(host={not skip_desktop}, backend={not skip_backend}, "
        f"web={not skip_web}, directus={not skip_directus}, "
        f"localDirectus={not skip_local_directus}, release={release})",
        flush=True,
    )
    return 0


# ---------------------------------------------------------------------------
# Entry point
# ---------------------------------------------------------------------------


def main(argv: list[str] | None = None) -> int:
    ns = parse_args(argv)
    paths = RepoPaths.default(Path(__file__).resolve().parent.parent)
    return run_build(
        paths,
        release=ns.release,
        skip_web=ns.skip_web,
        skip_backend=ns.skip_backend,
        skip_desktop=ns.skip_desktop,
        skip_directus=ns.skip_directus,
        skip_local_directus=ns.skip_local_directus,
        keep_staging=ns.keep_staging,
    )


if __name__ == "__main__":
    sys.exit(main())
