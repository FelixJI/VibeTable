from __future__ import annotations

import json
from pathlib import Path

import pytest

from scripts import build_next
from scripts.versioning import bump_version, check_versions, read_project_version, update_versions

REPO_ROOT = Path(__file__).resolve().parent.parent


def test_repository_versions_are_consistent() -> None:
    assert check_versions(REPO_ROOT) == []


@pytest.mark.parametrize(
    ("part", "expected"),
    [("major", "2.0.0"), ("minor", "1.3.0"), ("patch", "1.2.4")],
)
def test_semver_bump(part: str, expected: str) -> None:
    assert bump_version("1.2.3", part) == expected


def test_version_dry_run_lists_targets_without_writing() -> None:
    original = read_project_version(REPO_ROOT)
    changed = update_versions(REPO_ROOT, "9.8.7", dry_run=True)
    assert REPO_ROOT / "pyproject.toml" in changed
    assert REPO_ROOT / "desktop" / "publish-layout.json" in changed
    assert read_project_version(REPO_ROOT) == original


def test_manifest_contains_all_components_at_project_version() -> None:
    paths = build_next.RepoPaths.default(REPO_ROOT)
    assert paths.staging_root.parent == paths.publish_root.parent
    manifest = json.loads(build_next.render_manifest(paths))
    version = read_project_version(REPO_ROOT)
    # Scalar components must match the project version.
    assert manifest["components"]["host"] == {"version": version}
    assert manifest["components"]["backend"] == {"version": version}
    assert manifest["components"]["web"] == {"version": version}
    assert manifest["components"]["localDirectus"] == {"version": version}
    # Singular (backward-compatible) first extension.
    assert manifest["components"]["directusExtension"] == {"version": version}
    # Plural (authoritative): every extension from the manifest.
    ext_names = [d.name for d in paths.directus_extension_dirs]
    assert manifest["components"]["directusExtensions"] == [
        {"name": name, "version": version} for name in ext_names
    ]
    # Launch paths.
    assert manifest["launch"]["directusExtension"] == f"directus/extensions/{ext_names[0]}"
    assert manifest["launch"]["directusExtensions"] == [
        f"directus/extensions/{name}" for name in ext_names
    ]
    # local-directus ships source-only; node_modules is pulled at first launch.
    # The host drives Directus directly now, so there is no packaged-runner
    # launch path in the manifest (localDirectusRunner was removed).
    assert manifest["launch"]["localDirectus"] == "local-directus"
    assert "localDirectusRunner" not in manifest["launch"]


def test_release_mode_rejects_skipping_directus() -> None:
    with pytest.raises(SystemExit) as exc_info:
        build_next.parse_args(["--release", "--skip-directus"])
    assert exc_info.value.code == 2


def test_release_mode_rejects_skipping_local_directus() -> None:
    with pytest.raises(SystemExit) as exc_info:
        build_next.parse_args(["--release", "--skip-local-directus"])
    assert exc_info.value.code == 2


def test_local_directus_stage_ships_source_only(tmp_path: Path) -> None:
    """The local-directus stage copies the launcher source but never node_modules
    or any per-machine runtime artifact (those are pulled online at first launch)."""
    paths = build_next.RepoPaths.default(REPO_ROOT)
    stage = paths.staging_mirror()
    build_next._build_local_directus_stage(stage, skip=False)

    target = stage.local_directus_publish_dir
    # The host drives Directus directly, so only the npm manifest, lockfile and
    # env template are shipped (no run.py/install.py launcher).
    assert (target / "package.json").is_file()
    assert (target / ".env.template").is_file()
    assert not (target / "run.py").exists()
    assert not (target / "install.py").exists()
    # Per-machine / downloaded artifacts must NOT leak into the installer.
    assert not (target / "node_modules").exists()
    assert not (target / ".env").exists()
    assert not (target / "data").exists()
    assert not (target / ".npm-cache").exists()


def test_verify_stage_accepts_source_only_local_directus(tmp_path: Path) -> None:
    """The verifier must enforce the same launcher-free contract as staging."""
    defaults = build_next.RepoPaths.default(REPO_ROOT)
    paths = build_next.RepoPaths(
        repo_root=defaults.repo_root,
        web_grid_dir=defaults.web_grid_dir,
        directus_extension_dirs=defaults.directus_extension_dirs,
        local_directus_source_dir=defaults.local_directus_source_dir,
        desktop_csproj=defaults.desktop_csproj,
        backend_main=defaults.backend_main,
        staging_root=tmp_path / "staging",
        scratch_root=tmp_path / "scratch",
        publish_root=tmp_path / "publish",
    )
    build_next._build_local_directus_stage(paths, skip=False)
    paths.manifest_path.write_text(build_next.render_manifest(paths), encoding="utf-8")

    build_next._verify_stage(
        paths,
        skip_web=True,
        skip_backend=True,
        skip_desktop=True,
        skip_directus=True,
        skip_local_directus=False,
    )


def test_portable_node_runtime_contains_npm_cli() -> None:
    assert (REPO_ROOT / "runtime" / "node" / "node.exe").is_file()
    assert (
        REPO_ROOT / "runtime" / "node" / "node_modules" / "npm" / "bin" / "npm-cli.js"
    ).is_file()


def test_build_executor_prefers_x64_dotnet_when_available() -> None:
    resolved = build_next._resolve_executable("dotnet")
    if build_next.PREFERRED_DOTNET.is_file():
        assert resolved == str(build_next.PREFERRED_DOTNET)


def test_pyinstaller_build_uses_onedir_and_hidden_imports(tmp_path: Path) -> None:
    """PyInstaller onedir command pins the entrypoint, name, hidden imports."""
    paths = build_next.RepoPaths.default(REPO_ROOT)
    command = build_next.build_pyinstaller_backend_command(paths, tmp_path)
    assert "--onedir" in command
    assert "--console" in command
    assert "--name" in command
    # The entrypoint is backend/__main__.py (the BFF).
    assert command[-1] == str(REPO_ROOT / "backend" / "__main__.py")
    for hidden in build_next.BACKEND_HIDDEN_IMPORTS:
        assert hidden in command, f"hidden import {hidden} missing from command"
    excluded = {
        command[index + 1]
        for index, value in enumerate(command[:-1])
        if value == "--exclude-module"
    }
    assert excluded == build_next._DEV_PACKAGES_FORBIDDEN_IN_BUNDLE
    # pydantic data files collected so the bundle is self-contained.
    assert "--collect-data" in command
    assert "pydantic" in command


# ---------------------------------------------------------------------------
# G0.2: multi-extension manifest support
# ---------------------------------------------------------------------------


def test_extension_manifest_discovers_declared_extensions() -> None:
    """The version-controlled manifest must list at least the bulk-mutation extension."""
    from scripts.extension_manifest import list_extensions

    entries = list_extensions(REPO_ROOT)
    assert len(entries) >= 1
    names = [e.name for e in entries]
    assert "vibetable-bulk-mutation" in names
    # Every declared extension must have a real source directory.
    for entry in entries:
        ext_dir = REPO_ROOT / "directus" / "extensions" / entry.name
        assert (ext_dir / "package.json").is_file(), f"extension {entry.name} missing package.json"


def test_build_paths_include_every_manifest_extension() -> None:
    """RepoPaths must discover one extension dir per manifest entry."""
    paths = build_next.RepoPaths.default(REPO_ROOT)
    from scripts.extension_manifest import extension_names

    expected = extension_names(REPO_ROOT)
    actual = [d.name for d in paths.directus_extension_dirs]
    assert actual == expected


def test_multi_extension_manifest_with_test_fixture(tmp_path: Path) -> None:
    """Adding a second extension to a temp manifest makes both discoverable.

    Validates the G0.2 gate: 'with only the old bulk extension, the build
    output is consistent with current behavior; after adding a test extension
    fixture, all extensions can be discovered.'
    """
    from scripts.extension_manifest import ExtensionEntry, list_extensions

    # Build a minimal temp repo with just the manifest + a fixture extension.
    # We do NOT copy the real extension tree (it has node_modules with very
    # long paths that break Windows copytree); instead we create synthetic
    # source-only extension dirs.
    tmp_repo = tmp_path / "repo"
    tmp_ext_root = tmp_repo / "directus" / "extensions"
    tmp_ext_root.mkdir(parents=True)

    # Synthetic bulk-mutation (source-only: just package.json).
    bulk_dir = tmp_ext_root / "vibetable-bulk-mutation"
    bulk_dir.mkdir()
    (bulk_dir / "package.json").write_text(
        json.dumps({"name": "vibetable-bulk-mutation", "version": "1.0.0"}), encoding="utf-8"
    )

    # Synthetic workspace-index fixture.
    fixture_dir = tmp_ext_root / "vibetable-workspace-index"
    fixture_dir.mkdir()
    (fixture_dir / "package.json").write_text(
        json.dumps({"name": "vibetable-workspace-index", "version": "1.0.0"}), encoding="utf-8"
    )

    # Manifest declaring both extensions.
    manifest = {
        "formatVersion": 1,
        "extensions": [
            {
                "name": "vibetable-bulk-mutation",
                "type": "endpoint",
                "source": "src/index.ts",
                "entry": "dist/index.js",
                "directusHost": "^12.0.0",
                "capability": "vibetable-bulk-mutation.v1",
                "stage": "B2",
                "description": "Bulk mutation endpoint.",
            },
            {
                "name": "vibetable-workspace-index",
                "type": "endpoint",
                "source": "src/index.ts",
                "entry": "dist/index.js",
                "directusHost": "^12.0.0",
                "capability": "vibetable-workspace-index.v1",
                "stage": "G3",
                "description": "Workspace index endpoint.",
            },
        ],
    }
    (tmp_ext_root / "manifest.json").write_text(
        json.dumps(manifest, indent=2, ensure_ascii=False) + "\n", encoding="utf-8"
    )

    entries = list_extensions(tmp_repo)
    assert [e.name for e in entries] == ["vibetable-bulk-mutation", "vibetable-workspace-index"]
    assert isinstance(entries[1], ExtensionEntry)
    assert entries[1].capability == "vibetable-workspace-index.v1"
