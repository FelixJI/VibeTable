from __future__ import annotations

import base64
import json
import shutil
from pathlib import Path
from types import SimpleNamespace
from typing import Any

import pytest

from backend.adapters.directus.profile import CollectionProfile
from backend.contracts.plugin import MutationPlan, PluginPrivateSetting
from backend.infrastructure.plugin_worker import (
    DirectusBulkMutationAdapter,
    NodePluginWorkerAdapter,
    PluginWorkerError,
)


class _Store:
    def __init__(self, package: Path, *, permissions: dict[str, Any]) -> None:
        manifest = SimpleNamespace(
            plugin_id="com.example.safe-worker",
            permissions=permissions,
            actions=[SimpleNamespace(action_id="safe-action", worker_entry="dist/worker.js")],
        )
        self.installation = SimpleNamespace(
            plugin_id=manifest.plugin_id,
            version="1.0.0",
            package_hash="sha256:test",
            manifest=manifest,
        )
        self.revision = SimpleNamespace(
            version="1.0.0",
            package_hash="sha256:test",
            local_path=str(package),
            state="current",
        )
        self.settings: dict[tuple[str, str, str], PluginPrivateSetting] = {}

    def list_installations(self, project_key: str) -> list[Any]:
        return [self.installation] if project_key == "project-a" else []

    def get_installation(self, project_key: str, plugin_id: str) -> Any | None:
        if (project_key, plugin_id) == (
            "project-a",
            "com.example.safe-worker",
        ):
            return self.installation
        return None

    def list_package_revisions(self, project_key: str, plugin_id: str) -> list[Any]:
        assert (project_key, plugin_id) == ("project-a", "com.example.safe-worker")
        return [self.revision]

    def get_private_setting(
        self, project_key: str, plugin_id: str, setting_key: str
    ) -> PluginPrivateSetting | None:
        return self.settings.get((project_key, plugin_id, setting_key))

    def save_private_setting(
        self,
        setting: PluginPrivateSetting,
        *,
        expected_revision: int | None = None,
    ) -> PluginPrivateSetting:
        key = (setting.project_key, setting.plugin_id, setting.setting_key)
        current = self.settings.get(key)
        assert expected_revision == (current.revision if current else None)
        self.settings[key] = setting
        return setting


class _Client:
    def __init__(self) -> None:
        self.calls: list[tuple[Any, Any, list[str]]] = []

    async def read_items_with_fields(
        self, profile: Any, query: Any, fields: list[str]
    ) -> tuple[list[dict[str, Any]], dict[str, Any], object]:
        self.calls.append((profile, query, fields))
        return ([{"id": "1", "title": "safe"}], {}, object())


class _Auth:
    async def access_token(self) -> str:
        return "secret-token"


class _Transport:
    def __init__(self) -> None:
        self.requests: list[dict[str, Any]] = []

    async def request(self, method: str, path: str, **kwargs: Any) -> dict[str, Any]:
        self.requests.append({"method": method, "path": path, **kwargs})
        return {
            "data": {
                "createdRowKeys": ["1"],
                "updatedRowKeys": ["2"],
                "skippedRowKeys": [],
                "conflicts": [],
            }
        }


class _FileAdapter:
    available = True

    def __init__(self) -> None:
        self.written: bytes | None = None

    async def pick_read(self, execution: dict[str, Any], options: dict[str, Any]) -> dict[str, Any]:
        assert execution["runId"] == "run-1"
        assert options == {"mediaTypes": ["text/plain"]}
        return {"grantId": "read-1", "displayName": "input.txt", "mediaType": "text/plain"}

    async def pick_write(
        self, execution: dict[str, Any], options: dict[str, Any]
    ) -> dict[str, Any]:
        assert execution["runId"] == "run-1"
        assert options["suggestedName"] == "output.txt"
        return {"grantId": "write-1", "displayName": "output.txt"}

    def read(self, execution: dict[str, Any], grant_id: str) -> dict[str, str]:
        assert execution["runId"] == "run-1"
        assert grant_id == "read-1"
        return {"base64": base64.b64encode(b"hello").decode("ascii")}

    def write(self, execution: dict[str, Any], grant_id: str, encoded: str) -> None:
        assert execution["runId"] == "run-1"
        assert grant_id == "write-1"
        self.written = base64.b64decode(encoded)


class _Reporter:
    def __init__(self) -> None:
        self.updates: list[dict[str, Any]] = []

    async def report(self, **kwargs: Any) -> None:
        self.updates.append(kwargs)


def _package(tmp_path: Path, source: str) -> Path:
    package = tmp_path / "plugin"
    worker = package / "dist" / "worker.js"
    worker.parent.mkdir(parents=True)
    worker.write_text(source, encoding="utf-8")
    return package


def _context() -> dict[str, Any]:
    return {
        "contract": "vibetable.command-context.v1",
        "projectKey": "project-a",
        "collection": "articles",
        "selectedKeys": [],
        "querySnapshot": None,
        "locale": "zh-CN",
        "theme": "light",
        "density": "comfortable",
        "user": {},
        "hostVersion": "1.0.0",
    }


def _execution() -> dict[str, Any]:
    return {
        "projectKey": "project-a",
        "pluginId": "com.example.safe-worker",
        "pluginVersion": "1.0.0",
        "packageHash": "sha256:test",
        "actionId": "safe-action",
        "context": _context(),
    }


@pytest.mark.asyncio
async def test_node_worker_exposes_only_scoped_read_context_and_private_storage(
    tmp_path: Path,
) -> None:
    if shutil.which("node") is None:
        pytest.skip("Node.js is not installed")
    package = _package(
        tmp_path,
        """
        export async function run(input, capabilities, signal) {
          signal.throwIfAborted();
          const context = await capabilities.context.read();
          await capabilities.storage.set("preference", input.preference);
          const stored = await capabilities.storage.get("preference");
          const page = await capabilities.data.read({
            collection: context.collection,
            fields: ["title"],
            pageSize: 25,
          });
          return {
            contract: "vibetable.plugin-result.v1",
            status: "success",
            summary: stored,
            metrics: [{ label: "rows", value: page.items.length }],
            warnings: [],
          };
        }
        """,
    )
    store = _Store(
        package,
        permissions={
            "data": [
                {
                    "collection": "$active",
                    "operations": ["read"],
                    "fields": ["$configured"],
                }
            ],
            "privateStorage": True,
        },
    )
    client = _Client()
    adapter = NodePluginWorkerAdapter(
        store=store,
        profiles={
            "articles": CollectionProfile(
                collection="articles",
                fields=["id", "title", "date_updated"],
                archive_field=None,
            )
        },
        client=client,
        timeout_seconds=3,
    )

    result = await adapter.run(
        "dist/worker.js",
        _context(),
        {"preference": "compact"},
        execution=_execution(),
    )

    assert result["summary"] == "compact"
    assert result["metrics"] == [{"label": "rows", "value": 1}]
    assert client.calls[0][1].limit == 25
    assert client.calls[0][2] == ["title"]
    setting = store.get_private_setting("project-a", "com.example.safe-worker", "preference")
    assert setting is not None
    assert setting.value == "compact"


@pytest.mark.asyncio
async def test_node_worker_rejects_node_globals_and_undeclared_data(tmp_path: Path) -> None:
    if shutil.which("node") is None:
        pytest.skip("Node.js is not installed")
    package = _package(
        tmp_path,
        """
        export async function run(_input, capabilities) {
          if (typeof process !== "undefined" || typeof fetch !== "undefined") {
            throw new Error("ambient Node or network capability leaked");
          }
          return capabilities.data.read({
            collection: "secrets",
            fields: ["password"],
            pageSize: 1,
          });
        }
        """,
    )
    adapter = NodePluginWorkerAdapter(
        store=_Store(
            package,
            permissions={
                "data": [
                    {
                        "collection": "$active",
                        "operations": ["read"],
                        "fields": ["$configured"],
                    }
                ],
                "privateStorage": False,
            },
        ),
        profiles={
            "articles": CollectionProfile(
                collection="articles",
                fields=["id", "title", "date_updated"],
                archive_field=None,
            ),
            "secrets": CollectionProfile(
                collection="secrets",
                fields=["id", "password", "date_updated"],
                archive_field=None,
            ),
        },
        client=_Client(),
        timeout_seconds=3,
    )

    with pytest.raises(PluginWorkerError, match="not declared"):
        await adapter.run("dist/worker.js", _context(), {}, execution=_execution())


@pytest.mark.asyncio
async def test_node_worker_supports_declared_file_and_structured_ui_capabilities(
    tmp_path: Path,
) -> None:
    if shutil.which("node") is None:
        pytest.skip("Node.js is not installed")
    package = _package(
        tmp_path,
        """
        export async function run(_input, capabilities) {
          const source = await capabilities.file.pickRead({ mediaTypes: ["text/plain"] });
          const content = await source.read();
          const target = await capabilities.file.pickWrite({
            suggestedName: "output.txt",
            mediaType: "text/plain",
          });
          await target.write(content);
          const progress = await capabilities.ui.reportProgress({ current: 1, total: 1, message: "saved" });
          if (!progress.cancelRequested) throw new Error("cancellation was not projected");
          const result = {
            contract: "vibetable.plugin-result.v1",
            status: "success",
            summary: `copied ${content.length}`,
            warnings: [],
          };
          await capabilities.ui.emitResult(result);
          return result;
        }
        """,
    )
    file_adapter = _FileAdapter()
    reporter = _Reporter()
    execution = {
        **_execution(),
        "runId": "run-1",
        "_hostReporter": reporter,
        "_hostCancel": SimpleNamespace(cancelled=True),
    }
    adapter = NodePluginWorkerAdapter(
        store=_Store(
            package,
            permissions={
                "data": [],
                "files": ["pickRead", "pickWrite"],
                "privateStorage": False,
            },
        ),
        profiles={},
        client=_Client(),
        file_adapter=file_adapter,
        timeout_seconds=3,
    )

    result = await adapter.run("dist/worker.js", _context(), {}, execution=execution)

    assert result["summary"] == "copied 5"
    assert file_adapter.written == b"hello"
    assert reporter.updates == [{"done": 1, "total": 1, "message": "saved"}]


@pytest.mark.asyncio
async def test_node_worker_blocks_function_constructor_escape(tmp_path: Path) -> None:
    if shutil.which("node") is None:
        pytest.skip("Node.js is not installed")
    package = _package(
        tmp_path,
        """
        export async function run(_input, capabilities) {
          let escaped = false;
          try {
            const hostProcess = capabilities.context.read.constructor("return process")();
            escaped = Boolean(hostProcess?.env);
          } catch (_error) {}
          return {
            contract: "vibetable.plugin-result.v1",
            status: escaped ? "error" : "success",
            summary: escaped ? "sandbox escaped" : "closed",
            warnings: [],
          };
        }
        """,
    )
    adapter = NodePluginWorkerAdapter(
        store=_Store(package, permissions={"data": [], "privateStorage": False}),
        profiles={},
        client=_Client(),
        timeout_seconds=3,
    )

    result = await adapter.run("dist/worker.js", _context(), {}, execution=_execution())

    assert result["status"] == "success"
    assert result["summary"] == "closed"


@pytest.mark.asyncio
async def test_node_worker_times_out_and_terminates_infinite_code(tmp_path: Path) -> None:
    if shutil.which("node") is None:
        pytest.skip("Node.js is not installed")
    package = _package(
        tmp_path,
        "export async function run() { while (true) {} }",
    )
    adapter = NodePluginWorkerAdapter(
        store=_Store(package, permissions={"data": [], "privateStorage": False}),
        profiles={},
        client=_Client(),
        timeout_seconds=0.2,
    )

    with pytest.raises(PluginWorkerError, match="timed out"):
        await adapter.run("dist/worker.js", _context(), {}, execution=_execution())


@pytest.mark.asyncio
async def test_directus_bulk_adapter_maps_plan_without_exposing_token() -> None:
    transport = _Transport()
    profile = CollectionProfile(
        collection="articles",
        fields=["id", "title", "date_updated"],
        create_fields=["title"],
        update_fields=["title"],
        archive_field=None,
    )
    adapter = DirectusBulkMutationAdapter(
        transport=transport,
        auth=_Auth(),  # type: ignore[arg-type]
        profiles={"articles": profile},
    )
    plan = MutationPlan.model_validate(
        {
            "contract": "vibetable.mutation-plan.v1",
            "collection": "articles",
            "operations": [
                {"kind": "create", "values": {"title": "one"}},
                {
                    "kind": "update",
                    "primaryKey": "2",
                    "expectedDateUpdated": "rev-1",
                    "values": {"title": "two"},
                },
            ],
            "preview": {"affectedCount": 2},
            "idempotencyKey": "plugin-run-1",
        }
    )

    result = await adapter.apply(plan)

    request = transport.requests[0]
    assert request["path"] == "/vibetable-bulk-mutation/apply"
    assert request["headers"] == {"Idempotency-Key": "plugin-run-1"}
    assert request["json_body"]["operations"][1]["expectedDateUpdated"] == "rev-1"
    assert "secret-token" not in json.dumps(request["json_body"])
    assert result["contract"] == "vibetable.plugin-result.v1"
    assert result["refresh"] == {"collections": ["articles"]}


@pytest.mark.asyncio
async def test_directus_bulk_adapter_rejects_fields_outside_profile() -> None:
    adapter = DirectusBulkMutationAdapter(
        transport=_Transport(),
        auth=_Auth(),  # type: ignore[arg-type]
        profiles={
            "articles": CollectionProfile(
                collection="articles",
                fields=["id", "title", "date_updated"],
                create_fields=["title"],
                archive_field=None,
            )
        },
    )
    plan = MutationPlan.model_validate(
        {
            "collection": "articles",
            "operations": [{"kind": "create", "values": {"admin": True}}],
            "preview": {"affectedCount": 1},
        }
    )

    with pytest.raises(ValueError, match="not allowed"):
        await adapter.apply(plan)
