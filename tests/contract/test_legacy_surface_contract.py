from __future__ import annotations

import json
from pathlib import Path

from qa import legacy_surface_check


def _manifest() -> dict:
    return json.loads(legacy_surface_check.DEFAULT_MANIFEST.read_text(encoding="utf-8"))


def test_manifest_lists_every_required_legacy_surface_category() -> None:
    manifest = _manifest()
    assert {
        "forbiddenPaths",
        "forbiddenRpcMethods",
        "forbiddenDtoFields",
        "forbiddenSourceLiterals",
        "forbiddenJsonValues",
        "forbiddenJsonMembers",
        "currentCatalogs",
        "catalogsWithLegacyDtoFieldGuard",
        "allowedDetectionPrimitives",
    } <= manifest.keys()
    assert {
        "backup.list",
        "backup.create",
        "backup.delete",
        "backup.restore",
        "backup.openFolder",
        "workspace.linkDocument",
        "workspace.publishIndexBatch",
        "workspace.readDocumentHistory",
        "workspace.readDocuments",
        "workspace.readFolder",
        "workspace.registerDocument",
        "workspace.unlinkDocument",
    } <= set(manifest["forbiddenRpcMethods"])
    assert {"mainHead", "mainHash", "schemeId", "schemeName"} <= set(manifest["forbiddenDtoFields"])
    forbidden_sources = {
        entry["path"]: set(entry["literals"]) for entry in manifest["forbiddenSourceLiterals"]
    }
    assert {
        "BACKUP_PATH",
        "list_backups",
        "create_backup",
        "delete_backup",
        "restore_backup",
    } <= forbidden_sources["backend/adapters/pocketbase/client.py"]
    assert {"ReadPageForRemoteAsync", "client-mode", "LoadedRows"} <= forbidden_sources[
        "desktop/src/VibeTable.Desktop/Services/TableWorkspaceService.cs"
    ]
    assert {
        "ReadTablePageAsync",
        "QueryTablePageAsync",
        "QueryTableViewAsync",
    } <= forbidden_sources["desktop/src/VibeTable.Desktop/Services/ITableRpcGateway.cs"]
    assert {"TableQuery query"} <= forbidden_sources[
        "desktop/src/VibeTable.Desktop/Services/GridStateCoordinator.cs"
    ]
    assert {'"table.pageRequested"', '"client"'} <= forbidden_sources[
        "desktop/web-grid/src/contracts/index.ts"
    ]
    assert {"shouldUseRemoteMode", "25_000"} <= forbidden_sources[
        "desktop/web-grid/src/grid/queryAdapter.ts"
    ]
    assert (
        "ResolveBackupRoot"
        in forbidden_sources["desktop/src/VibeTable.Infrastructure/LaunchPaths.cs"]
    )
    assert {
        "desktop/src/VibeTable.Desktop/Services/ProductDataRootManager.cs",
        "desktop/src/VibeTable.Infrastructure/Workspace/WorkspaceMountStore.cs",
        "desktop/tests/VibeTable.Desktop.Tests/ProductDataRootManagerTests.cs",
        "desktop/tests/VibeTable.Infrastructure.Tests/Workspace/WorkspaceMountStoreTests.cs",
        "sidecar/internal/workspacemigrator",
    } <= set(manifest["forbiddenPaths"])
    publish_layout_members = {
        entry["pointer"]: set(entry["members"])
        for entry in manifest["forbiddenJsonMembers"]
        if entry["path"] == "desktop/publish-layout.json"
    }
    assert {"rootPolicy", "defaultBase", "fallbackBase", "relativePath"} <= (
        publish_layout_members["/data"]
    )
    allowed_paths = {entry["path"] for entry in manifest["allowedDetectionPrimitives"]}
    assert {
        "sidecar/internal/filehistory/materializer.go",
        "desktop/src/VibeTable.Infrastructure/Workspace/WorkspaceLayout.cs",
    } <= allowed_paths


def test_checker_is_precise_and_respects_detection_allowlist(tmp_path: Path) -> None:
    (tmp_path / "contracts/v2").mkdir(parents=True)
    (tmp_path / "scripts").mkdir()
    (tmp_path / "scripts/release.py").write_text(
        "def verify_upgrade_backup(): pass\n",
        encoding="utf-8",
    )
    (tmp_path / "sidecar/internal/filehistory").mkdir(parents=True)
    (tmp_path / "sidecar/internal/filehistory/materializer.go").write_text(
        "package filehistory\n",
        encoding="utf-8",
    )
    workspace_layout = (
        tmp_path / "desktop/src/VibeTable.Infrastructure/Workspace/WorkspaceLayout.cs"
    )
    workspace_layout.parent.mkdir(parents=True)
    workspace_layout.write_text("// detector\n", encoding="utf-8")
    (tmp_path / "contracts/v2/fixtures").mkdir()
    catalog = tmp_path / "contracts/v2/fixtures/rpc-catalog.json"
    catalog.write_text(
        '{"methods":["snapshot.list"],"fixtures":[]}\n',
        encoding="utf-8",
    )
    (tmp_path / "contracts/v2/fixtures/product-rpc-catalog.json").write_text(
        '{"methods":["data.previewImport"],"fixtures":[]}\n',
        encoding="utf-8",
    )
    (tmp_path / "qa").mkdir()
    (tmp_path / "qa/handoff_dependencies.json").write_text(
        '{"capabilities":{"RELEASE":["release.workspace-snapshot-v2"]}}\n',
        encoding="utf-8",
    )
    (tmp_path / "desktop").mkdir(exist_ok=True)
    (tmp_path / "desktop/publish-layout.json").write_text(
        '{"components":{"sidecar":{"contractVersion":"2.0"}},"data":{}}\n',
        encoding="utf-8",
    )
    manifest = _manifest()
    manifest["forbiddenPaths"] = ["backend/contracts/backup.py"]
    manifest["forbiddenSourceLiterals"] = []
    manifest_path = tmp_path / "contracts/v2/legacy-surface.json"
    manifest_path.write_text(json.dumps(manifest), encoding="utf-8")

    assert legacy_surface_check.check(tmp_path, manifest_path) == []

    catalog.write_text(
        '{"methods":["snapshot.list"],"fixtures":[{"schemeId":"legacy"}]}\n',
        encoding="utf-8",
    )
    assert legacy_surface_check.check(tmp_path, manifest_path) == [
        "legacy DTO field remains in current catalog "
        "contracts/v2/fixtures/rpc-catalog.json: schemeId"
    ]
    catalog.write_text(
        '{"methods":["snapshot.list"],"fixtures":[]}\n',
        encoding="utf-8",
    )
    publish_layout = tmp_path / "desktop/publish-layout.json"
    publish_layout.write_text(
        '{"components":{"sidecar":{"contractVersion":"2.0"}},"data":{"rootPolicy":"global"}}\n',
        encoding="utf-8",
    )
    errors = legacy_surface_check.check(tmp_path, manifest_path)
    assert any("desktop/publish-layout.json" in error and "rootPolicy" in error for error in errors)
    publish_layout.write_text(
        '{"components":{"sidecar":{"contractVersion":"2.0"}},"data":{}}\n',
        encoding="utf-8",
    )

    forbidden = tmp_path / "backend/contracts/backup.py"
    forbidden.parent.mkdir(parents=True)
    forbidden.write_text("# retired\n", encoding="utf-8")
    assert legacy_surface_check.check(tmp_path, manifest_path) == [
        "legacy product path still exists: backend/contracts/backup.py"
    ]


def test_current_v2_catalog_has_no_legacy_rpc() -> None:
    manifest = _manifest()
    forbidden = set(manifest["forbiddenRpcMethods"])
    forbidden_fields = set(manifest["forbiddenDtoFields"])
    field_guard_catalogs = set(manifest["catalogsWithLegacyDtoFieldGuard"])
    for relative in manifest["currentCatalogs"]:
        payload = json.loads(
            (legacy_surface_check.PROJECT_ROOT / relative).read_text(encoding="utf-8")
        )
        assert forbidden.isdisjoint(legacy_surface_check._catalog_methods(payload))
        if relative in field_guard_catalogs:
            assert forbidden_fields.isdisjoint(legacy_surface_check._json_member_names(payload))
