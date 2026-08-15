"""Security-boundary tests for the provider-neutral local plugin Worker."""

from __future__ import annotations

import asyncio
import base64
import shutil
from dataclasses import dataclass
from pathlib import Path
from types import SimpleNamespace
from typing import Any, Never

import pytest

from backend.contracts.data_profile import CollectionProfile
from backend.contracts.plugin import (
    ConfirmationPreview,
    PluginPrivateSetting,
)
from backend.infrastructure.plugin_worker import (
    InMemoryPluginWorkerAdapter,
    NodePluginWorkerAdapter,
    PluginWorkerError,
    _ResolvedWorker,
)
from tests.backend.schema_v2_fixtures import field_v2, snapshot_v2


class FakePluginStore:
    def __init__(self, package: Path, *, permissions: dict[str, Any]) -> None:
        manifest = SimpleNamespace(
            plugin_id="com.example.safe-worker",
            permissions=permissions,
            actions=[
                SimpleNamespace(
                    action_id="safe-action",
                    worker_entry="dist/worker.js",
                )
            ],
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

    def get_installation(self, project_key: str, plugin_id: str) -> Any | None:
        if (project_key, plugin_id) == (
            "project-a",
            "com.example.safe-worker",
        ):
            return self.installation
        return None

    def list_package_revisions(
        self,
        project_key: str,
        plugin_id: str,
    ) -> list[Any]:
        assert (project_key, plugin_id) == (
            "project-a",
            "com.example.safe-worker",
        )
        return [self.revision]

    def get_private_setting(
        self,
        project_key: str,
        plugin_id: str,
        setting_key: str,
    ) -> PluginPrivateSetting | None:
        return self.settings.get((project_key, plugin_id, setting_key))

    def save_private_setting(
        self,
        setting: PluginPrivateSetting,
        *,
        expected_revision: int | None,
    ) -> PluginPrivateSetting:
        key = (setting.project_key, setting.plugin_id, setting.setting_key)
        current = self.settings.get(key)
        assert expected_revision == (current.revision if current else None)
        self.settings[key] = setting
        return setting


@dataclass(frozen=True)
class FakeQueryPage:
    rows: list[dict[str, object]]


class FakeProductReadClient:
    def __init__(self) -> None:
        self.calls: list[tuple[str, dict[str, Any]]] = []

    def __getattr__(self, name: str) -> Never:
        raise AssertionError(f"plugin read port probed an undeclared member: {name}")

    async def query_page(self, *, table_id: str, query: dict[str, Any]) -> FakeQueryPage:
        self.calls.append((table_id, query))
        return FakeQueryPage(rows=[{"id": "1", "title": "safe"}])

    async def describe_table(self, table_id: str) -> dict[str, Any]:
        raise AssertionError(f"static plugin read port requested schema for {table_id}")


class DynamicSchemaClient(FakeProductReadClient):
    def __init__(self) -> None:
        super().__init__()
        self.revision = 6

    async def describe_table(self, table_id: str) -> dict[str, Any]:
        assert table_id == "articles"
        self.revision += 1
        fields = [field_v2("title")]
        if self.revision >= 8:
            fields.append(field_v2("note"))
        return snapshot_v2("articles", fields, revision=f"schema-{self.revision}")


class HangingSchemaClient(FakeProductReadClient):
    async def describe_table(self, table_id: str) -> dict[str, Any]:
        assert table_id == "articles"
        await asyncio.Future()
        raise AssertionError("unreachable")


class FakeFileAdapter:
    available = True

    def __init__(self) -> None:
        self.written: bytes | None = None

    async def pick_read(
        self,
        execution: dict[str, Any],
        options: dict[str, Any],
    ) -> dict[str, Any]:
        assert execution["runId"] == "run-1"
        assert options == {"mediaTypes": ["text/plain"]}
        return {
            "grantId": "read-1",
            "displayName": "input.txt",
            "mediaType": "text/plain",
        }

    async def pick_write(
        self,
        execution: dict[str, Any],
        options: dict[str, Any],
    ) -> dict[str, Any]:
        assert execution["runId"] == "run-1"
        assert options["suggestedName"] == "output.txt"
        return {"grantId": "write-1", "displayName": "output.txt"}

    def read(
        self,
        execution: dict[str, Any],
        grant_id: str,
    ) -> dict[str, str]:
        assert execution["runId"] == "run-1"
        assert grant_id == "read-1"
        return {"base64": base64.b64encode(b"hello").decode("ascii")}

    def write(
        self,
        execution: dict[str, Any],
        grant_id: str,
        encoded: str,
    ) -> None:
        assert execution["runId"] == "run-1"
        assert grant_id == "write-1"
        self.written = base64.b64decode(encoded)


class FakeReporter:
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
        "locale": "en",
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


def _require_node() -> None:
    if shutil.which("node") is None:
        pytest.skip("Node.js is not installed")


@pytest.mark.asyncio
async def test_dynamic_worker_profile_refreshes_after_schema_change() -> None:
    client = DynamicSchemaClient()
    adapter = NodePluginWorkerAdapter(
        store=SimpleNamespace(),
        profiles={},
        client=client,
    )

    first = await adapter._profile("articles")
    second = await adapter._profile("articles")

    assert first.schema_revision == "schema-7"
    assert first.update_fields == ["f_title000"]
    assert second.schema_revision == "schema-8"
    assert second.update_fields == ["f_title000", "f_note0000"]


@pytest.mark.asyncio
async def test_dynamic_worker_profile_reuses_matching_context_revision() -> None:
    client = DynamicSchemaClient()
    adapter = NodePluginWorkerAdapter(
        store=SimpleNamespace(),
        profiles={},
        client=client,
    )

    first = await adapter._profile(
        "articles",
        expected_schema_revision="schema-7",
    )
    second = await adapter._profile(
        "articles",
        expected_schema_revision="schema-7",
    )

    assert first is second
    assert client.revision == 7


@pytest.mark.asyncio
async def test_dynamic_worker_profile_refresh_is_time_bounded() -> None:
    adapter = NodePluginWorkerAdapter(
        store=SimpleNamespace(),
        profiles={},
        client=HangingSchemaClient(),
        timeout_seconds=0.01,
    )

    with pytest.raises(PluginWorkerError, match="schema validation timed out"):
        await adapter._profile("articles")


@pytest.mark.asyncio
async def test_worker_exposes_only_scoped_product_read_and_private_storage(
    tmp_path: Path,
) -> None:
    _require_node()
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
    store = FakePluginStore(
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
    client = FakeProductReadClient()
    adapter = NodePluginWorkerAdapter(
        store=store,
        profiles={
            "articles": CollectionProfile(
                collection="articles",
                fields=["id", "title"],
                archive_field=None,
                date_updated_field=None,
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
    assert client.calls == [
        (
            "articles",
            {
                "keyword": None,
                "filters": [],
                "limit": 25,
                "offset": 0,
                "sorts": [],
            },
        )
    ]
    setting = store.get_private_setting(
        "project-a",
        "com.example.safe-worker",
        "preference",
    )
    assert setting is not None
    assert setting.value == "compact"


@pytest.mark.asyncio
async def test_worker_rejects_node_globals_and_undeclared_product_data(
    tmp_path: Path,
) -> None:
    _require_node()
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
        store=FakePluginStore(
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
                fields=["id", "title"],
                archive_field=None,
                date_updated_field=None,
            ),
            "secrets": CollectionProfile(
                collection="secrets",
                fields=["id", "password"],
                archive_field=None,
                date_updated_field=None,
            ),
        },
        client=FakeProductReadClient(),
        timeout_seconds=3,
    )

    with pytest.raises(PluginWorkerError, match="not declared"):
        await adapter.run(
            "dist/worker.js",
            _context(),
            {},
            execution=_execution(),
        )


@pytest.mark.asyncio
async def test_worker_supports_declared_file_and_structured_ui_capabilities(
    tmp_path: Path,
) -> None:
    _require_node()
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
          const progress = await capabilities.ui.reportProgress({
            current: 1, total: 1, message: "saved"
          });
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
    file_adapter = FakeFileAdapter()
    reporter = FakeReporter()
    execution = {
        **_execution(),
        "runId": "run-1",
        "_hostReporter": reporter,
        "_hostCancel": SimpleNamespace(cancelled=True),
    }
    adapter = NodePluginWorkerAdapter(
        store=FakePluginStore(
            package,
            permissions={
                "data": [],
                "files": ["pickRead", "pickWrite"],
                "privateStorage": False,
            },
        ),
        profiles={},
        client=FakeProductReadClient(),
        file_adapter=file_adapter,
        timeout_seconds=3,
    )

    result = await adapter.run(
        "dist/worker.js",
        _context(),
        {},
        execution=execution,
    )

    assert result["summary"] == "copied 5"
    assert file_adapter.written == b"hello"
    assert reporter.updates == [{"done": 1, "total": 1, "message": "saved"}]


@pytest.mark.asyncio
async def test_worker_blocks_function_constructor_escape(tmp_path: Path) -> None:
    _require_node()
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
        store=FakePluginStore(
            package,
            permissions={"data": [], "privateStorage": False},
        ),
        profiles={},
        client=FakeProductReadClient(),
        timeout_seconds=3,
    )

    result = await adapter.run(
        "dist/worker.js",
        _context(),
        {},
        execution=_execution(),
    )

    assert result["status"] == "success"
    assert result["summary"] == "closed"


@pytest.mark.asyncio
async def test_worker_times_out_and_terminates_infinite_code(tmp_path: Path) -> None:
    _require_node()
    package = _package(
        tmp_path,
        "export async function run() { while (true) {} }",
    )
    adapter = NodePluginWorkerAdapter(
        store=FakePluginStore(
            package,
            permissions={"data": [], "privateStorage": False},
        ),
        profiles={},
        client=FakeProductReadClient(),
        timeout_seconds=0.2,
    )

    with pytest.raises(PluginWorkerError, match="timed out"):
        await adapter.run(
            "dist/worker.js",
            _context(),
            {},
            execution=_execution(),
        )


# ===========================================================================
# Pure-Python unit tests (no Node required) for the closed capability surface.
# These cover the validation, dispatch, and grant logic that does not depend on
# a live subprocess.
# ===========================================================================


def _resolved(permissions: dict[str, Any] | None = None) -> _ResolvedWorker:
    return _ResolvedWorker(
        project_key="project-a",
        plugin_id="com.example.safe-worker",
        permissions=permissions or {},
        source="// worker",
    )


def _make_adapter(**kwargs: Any) -> NodePluginWorkerAdapter:
    """Build an adapter without invoking Node — store/client are unused for most tests.

    Callers may override any keyword (store, profiles, client, file_adapter, …).
    """
    defaults: dict[str, Any] = {
        "store": SimpleNamespace(),
        "profiles": {},
        "client": SimpleNamespace(),
        "node_executable": "node",
    }
    defaults.update(kwargs)
    return NodePluginWorkerAdapter(**defaults)


# ---------------------------------------------------------------------------
# Constructor validation
# ---------------------------------------------------------------------------


@pytest.mark.parametrize(
    ("kwargs", "match"),
    [
        ({"timeout_seconds": 0}, "timeout must be positive"),
        ({"max_concurrency": 0}, "concurrency must be at least one"),
        ({"max_message_bytes": 512}, "message limit is too small"),
        ({"max_capability_calls": 0}, "capability-call limit must be at least one"),
    ],
)
def test_adapter_rejects_invalid_configuration(kwargs: dict[str, Any], match: str) -> None:
    with pytest.raises(ValueError, match=match):
        _make_adapter(**kwargs)


# ---------------------------------------------------------------------------
# _dispatch_capability error branches (pure-Python, no Node)
# ---------------------------------------------------------------------------


@pytest.mark.asyncio
async def test_dispatch_rejects_direct_data_mutate() -> None:
    adapter = _make_adapter()
    with pytest.raises(PluginWorkerError, match="cannot write directly"):
        await adapter._dispatch_capability(_resolved(), {}, {}, "data.mutate", {})


@pytest.mark.asyncio
async def test_dispatch_rejects_progress_without_reporter() -> None:
    adapter = _make_adapter()
    with pytest.raises(PluginWorkerError, match="progress reporter is unavailable"):
        await adapter._dispatch_capability(
            _resolved(), {}, {}, "ui.reportProgress", {"current": 1, "total": 2}
        )


@pytest.mark.asyncio
async def test_dispatch_rejects_unknown_capability() -> None:
    adapter = _make_adapter()
    with pytest.raises(PluginWorkerError, match="is unavailable"):
        await adapter._dispatch_capability(_resolved(), {}, {}, "magic.power", {})


@pytest.mark.asyncio
async def test_dispatch_context_read_returns_context() -> None:
    adapter = _make_adapter()
    ctx = {"collection": "orders"}
    result = await adapter._dispatch_capability(_resolved(), ctx, {}, "context.read", None)
    assert result is ctx


# ---------------------------------------------------------------------------
# _file_capability error matrix
# ---------------------------------------------------------------------------


@pytest.mark.asyncio
async def test_file_capability_rejects_when_adapter_missing() -> None:
    adapter = _make_adapter()  # no file_adapter
    with pytest.raises(PluginWorkerError, match="file picker is unavailable"):
        await adapter._file_capability(_resolved(), {}, "file.read", {})


@pytest.mark.asyncio
async def test_file_capability_rejects_when_adapter_unavailable() -> None:
    adapter = _make_adapter(file_adapter=SimpleNamespace(available=False))
    with pytest.raises(PluginWorkerError, match="file picker is unavailable"):
        await adapter._file_capability(_resolved(), {}, "file.read", {})


@pytest.mark.asyncio
async def test_file_capability_rejects_non_dict_args() -> None:
    adapter = _make_adapter(file_adapter=FakeFileAdapter())
    with pytest.raises(PluginWorkerError, match="arguments must be an object"):
        await adapter._file_capability(_resolved(), {}, "file.read", "not-a-dict")


@pytest.mark.asyncio
async def test_file_capability_pick_read_rejects_undeclared() -> None:
    adapter = _make_adapter(file_adapter=FakeFileAdapter())
    resolved = _resolved({"files": []})
    with pytest.raises(PluginWorkerError, match=r"did not declare file.pickRead"):
        await adapter._file_capability(resolved, {}, "file.pickRead", {})


@pytest.mark.asyncio
async def test_file_capability_pick_write_rejects_undeclared() -> None:
    adapter = _make_adapter(file_adapter=FakeFileAdapter())
    resolved = _resolved({"files": []})
    with pytest.raises(PluginWorkerError, match=r"did not declare file.pickWrite"):
        await adapter._file_capability(resolved, {}, "file.pickWrite", {})


@pytest.mark.asyncio
async def test_file_capability_read_rejects_missing_grant_id() -> None:
    adapter = _make_adapter(file_adapter=FakeFileAdapter())
    resolved = _resolved({"files": ["pickRead"]})
    with pytest.raises(PluginWorkerError, match="grantId is required"):
        await adapter._file_capability(resolved, {}, "file.read", {})


@pytest.mark.asyncio
async def test_file_capability_read_rejects_undeclared_pick_read() -> None:
    adapter = _make_adapter(file_adapter=FakeFileAdapter())
    resolved = _resolved({"files": []})
    with pytest.raises(PluginWorkerError, match=r"did not declare file.pickRead"):
        await adapter._file_capability(resolved, {}, "file.read", {"grantId": "g1"})


@pytest.mark.asyncio
async def test_file_capability_write_rejects_undeclared_pick_write() -> None:
    adapter = _make_adapter(file_adapter=FakeFileAdapter())
    resolved = _resolved({"files": []})
    with pytest.raises(PluginWorkerError, match=r"did not declare file.pickWrite"):
        await adapter._file_capability(resolved, {}, "file.write", {"grantId": "g1"})


@pytest.mark.asyncio
async def test_file_capability_write_rejects_non_string_base64() -> None:
    adapter = _make_adapter(file_adapter=FakeFileAdapter())
    resolved = _resolved({"files": ["pickWrite"]})
    with pytest.raises(PluginWorkerError, match="file content is required"):
        await adapter._file_capability(resolved, {}, "file.write", {"grantId": "g1", "base64": 123})


# ---------------------------------------------------------------------------
# _data_read validation + query_page fallback branch
# ---------------------------------------------------------------------------


@pytest.mark.asyncio
async def test_data_read_rejects_non_dict_request() -> None:
    adapter = _make_adapter()
    with pytest.raises(PluginWorkerError, match="request must be an object"):
        await adapter._data_read(_resolved(), {}, "not-a-dict")


@pytest.mark.asyncio
async def test_data_read_rejects_unsupported_fields() -> None:
    adapter = _make_adapter()
    with pytest.raises(PluginWorkerError, match="unsupported fields"):
        await adapter._data_read(_resolved(), {}, {"collection": "x", "evil": True})


@pytest.mark.asyncio
async def test_data_read_rejects_empty_collection() -> None:
    adapter = _make_adapter()
    with pytest.raises(PluginWorkerError, match="collection is required"):
        await adapter._data_read(_resolved(), {}, {"collection": ""})


@pytest.mark.asyncio
async def test_data_read_rejects_non_list_fields() -> None:
    adapter = _make_adapter(
        profiles={
            "orders": CollectionProfile(
                collection="orders",
                fields=["id", "name"],
                archive_field=None,
                date_updated_field=None,
            )
        }
    )
    resolved = _resolved(
        {"data": [{"collection": "orders", "operations": ["read"], "fields": ["$configured"]}]}
    )
    with pytest.raises(PluginWorkerError, match="fields must be a string array"):
        await adapter._data_read(resolved, {}, {"collection": "orders", "fields": "id"})


@pytest.mark.asyncio
async def test_data_read_rejects_undeclared_fields() -> None:
    adapter = _make_adapter(
        profiles={
            "orders": CollectionProfile(
                collection="orders",
                fields=["id", "name"],
                archive_field=None,
                date_updated_field=None,
            )
        }
    )
    resolved = _resolved(
        {"data": [{"collection": "orders", "operations": ["read"], "fields": ["id"]}]}
    )
    with pytest.raises(PluginWorkerError, match="not declared"):
        await adapter._data_read(resolved, {}, {"collection": "orders", "fields": ["secret"]})


@pytest.mark.asyncio
async def test_data_read_rejects_non_empty_filter() -> None:
    adapter = _make_adapter(
        profiles={
            "orders": CollectionProfile(
                collection="orders",
                fields=["id"],
                archive_field=None,
                date_updated_field=None,
            )
        }
    )
    resolved = _resolved(
        {"data": [{"collection": "orders", "operations": ["read"], "fields": ["id"]}]}
    )
    with pytest.raises(PluginWorkerError, match="filter is unavailable"):
        await adapter._data_read(
            resolved, {}, {"collection": "orders", "fields": ["id"], "filter": {"a": 1}}
        )


@pytest.mark.asyncio
async def test_data_read_rejects_non_integer_page_size() -> None:
    adapter = _make_adapter(
        profiles={
            "orders": CollectionProfile(
                collection="orders",
                fields=["id"],
                archive_field=None,
                date_updated_field=None,
            )
        }
    )
    resolved = _resolved(
        {"data": [{"collection": "orders", "operations": ["read"], "fields": ["id"]}]}
    )
    with pytest.raises(PluginWorkerError, match="pageSize must be an integer"):
        await adapter._data_read(
            resolved, {}, {"collection": "orders", "fields": ["id"], "pageSize": "10"}
        )


@pytest.mark.asyncio
async def test_data_read_rejects_invalid_cursor_type() -> None:
    adapter = _make_adapter(
        profiles={
            "orders": CollectionProfile(
                collection="orders",
                fields=["id"],
                archive_field=None,
                date_updated_field=None,
            )
        }
    )
    resolved = _resolved(
        {"data": [{"collection": "orders", "operations": ["read"], "fields": ["id"]}]}
    )
    with pytest.raises(PluginWorkerError, match="cursor is invalid"):
        await adapter._data_read(
            resolved, {}, {"collection": "orders", "fields": ["id"], "cursor": [1, 2]}
        )


# ---------------------------------------------------------------------------
# _storage error branches
# ---------------------------------------------------------------------------


def test_storage_rejects_undeclared_private_storage() -> None:
    adapter = _make_adapter()
    with pytest.raises(PluginWorkerError, match="did not declare privateStorage"):
        adapter._storage(_resolved(), "storage.private.get", {"key": "k"})


def test_storage_rejects_non_dict_args() -> None:
    adapter = _make_adapter()
    resolved = _resolved({"privateStorage": True})
    with pytest.raises(PluginWorkerError, match="key is required"):
        adapter._storage(resolved, "storage.private.get", "not-a-dict")


def test_storage_rejects_invalid_key_format() -> None:
    adapter = _make_adapter()
    resolved = _resolved({"privateStorage": True})
    with pytest.raises(PluginWorkerError, match="key is invalid"):
        adapter._storage(resolved, "storage.private.get", {"key": "bad key!"})


# ---------------------------------------------------------------------------
# _validate_mutation_plan branches
# ---------------------------------------------------------------------------


def _mutation_plan(
    *,
    collection: str = "orders",
    affected_count: int = 1,
    operations: list[dict[str, Any]] | None = None,
) -> dict[str, Any]:
    return {
        "contract": "vibetable.mutation-plan.v1",
        "collection": collection,
        "operations": operations or [{"kind": "create", "values": {"name": "x"}}],
        "preview": ConfirmationPreview(affected_count=affected_count).model_dump(
            by_alias=True, mode="json"
        ),
    }


def _orders_profile() -> CollectionProfile:
    """A minimal writable profile so _profile returns without describe_table."""
    return CollectionProfile(
        collection="orders",
        fields=["id", "name"],
        create_fields=["name"],
        update_fields=["name"],
        archive_field=None,
        date_updated_field=None,
    )


@pytest.mark.asyncio
async def test_validate_mutation_plan_rejects_invalid_plan() -> None:
    adapter = _make_adapter(profiles={"orders": _orders_profile()})
    with pytest.raises(PluginWorkerError, match="invalid mutation plan"):
        await adapter._validate_mutation_plan(_resolved(), {}, {"contract": "wrong"})


@pytest.mark.asyncio
async def test_validate_mutation_plan_rejects_count_mismatch() -> None:
    adapter = _make_adapter(profiles={"orders": _orders_profile()})
    with pytest.raises(PluginWorkerError, match="affectedCount must match"):
        await adapter._validate_mutation_plan(_resolved(), {}, _mutation_plan(affected_count=5))


@pytest.mark.asyncio
async def test_validate_mutation_plan_rejects_undeclared_collection() -> None:
    adapter = _make_adapter(profiles={"orders": _orders_profile()})
    resolved = _resolved({"data": []})  # no write grant
    with pytest.raises(PluginWorkerError, match="not declared for mutation"):
        await adapter._validate_mutation_plan(resolved, {}, _mutation_plan())


@pytest.mark.asyncio
async def test_validate_mutation_plan_rejects_undeclared_operation() -> None:
    adapter = _make_adapter(profiles={"orders": _orders_profile()})
    resolved = _resolved(
        {"data": [{"collection": "orders", "operations": ["update"], "fields": ["name"]}]}
    )
    plan = _mutation_plan(operations=[{"kind": "create", "values": {"name": "x"}}])
    with pytest.raises(PluginWorkerError, match="was not declared"):
        await adapter._validate_mutation_plan(resolved, {}, plan)


@pytest.mark.asyncio
async def test_validate_mutation_plan_rejects_undeclared_fields() -> None:
    adapter = _make_adapter(profiles={"orders": _orders_profile()})
    resolved = _resolved(
        {"data": [{"collection": "orders", "operations": ["create"], "fields": ["name"]}]}
    )
    plan = _mutation_plan(operations=[{"kind": "create", "values": {"secret": "x"}}])
    with pytest.raises(PluginWorkerError, match="fields were not declared"):
        await adapter._validate_mutation_plan(resolved, {}, plan)


# ---------------------------------------------------------------------------
# _resolve error matrix
# ---------------------------------------------------------------------------


def test_resolve_rejects_missing_project_key() -> None:
    adapter = _make_adapter()
    with pytest.raises(PluginWorkerError, match="no projectKey"):
        adapter._resolve("worker.js", {"projectKey": ""}, _execution())


def test_resolve_rejects_non_dict_execution() -> None:
    adapter = _make_adapter()
    with pytest.raises(PluginWorkerError, match="execution identity is unavailable"):
        adapter._resolve("worker.js", {"projectKey": "p"}, None)  # type: ignore[arg-type]


def test_resolve_rejects_incomplete_execution_fields(tmp_path: Path) -> None:
    adapter = _make_adapter(store=FakePluginStore(tmp_path, permissions={}))
    ctx = {"projectKey": "project-a"}
    with pytest.raises(PluginWorkerError, match="identity is invalid"):
        adapter._resolve("worker.js", ctx, {**_execution(), "pluginId": ""})


def test_resolve_rejects_project_key_mismatch(tmp_path: Path) -> None:
    adapter = _make_adapter(store=FakePluginStore(tmp_path, permissions={}))
    ctx = {"projectKey": "project-a"}
    with pytest.raises(PluginWorkerError, match="project identity does not match"):
        adapter._resolve("worker.js", ctx, {**_execution(), "projectKey": "other"})


def test_resolve_rejects_missing_installation(tmp_path: Path) -> None:
    adapter = _make_adapter(store=FakePluginStore(tmp_path, permissions={}))
    ctx = {"projectKey": "project-x"}
    exec_bad = {**_execution(), "projectKey": "project-x"}
    with pytest.raises(PluginWorkerError, match="installation is unavailable"):
        adapter._resolve("worker.js", ctx, exec_bad)


def test_resolve_rejects_stale_package_identity(tmp_path: Path) -> None:
    store = FakePluginStore(tmp_path, permissions={})
    adapter = _make_adapter(store=store)
    ctx = {"projectKey": "project-a"}
    exec_stale = {**_execution(), "packageHash": "sha256:different"}
    with pytest.raises(PluginWorkerError, match="package identity is stale"):
        adapter._resolve("worker.js", ctx, exec_stale)


def test_resolve_rejects_unowned_worker_entry(tmp_path: Path) -> None:
    store = FakePluginStore(tmp_path, permissions={})
    adapter = _make_adapter(store=store)
    ctx = {"projectKey": "project-a"}
    with pytest.raises(PluginWorkerError, match="does not own"):
        adapter._resolve("dist/other.js", ctx, _execution())


def test_resolve_rejects_unloadable_worker_entry(tmp_path: Path) -> None:
    store = FakePluginStore(tmp_path, permissions={})
    # Point local_path at a non-existent directory.
    store.revision.local_path = str(tmp_path / "missing")
    adapter = _make_adapter(store=store)
    ctx = {"projectKey": "project-a"}
    with pytest.raises(PluginWorkerError, match="could not be loaded"):
        adapter._resolve("dist/worker.js", ctx, _execution())


def test_resolve_rejects_oversized_worker_entry(tmp_path: Path) -> None:
    # Create a real package whose worker entry exceeds the minimum byte limit.
    big_source = "export async function run() { return {}; }\n" + "// " + ("x" * 1200)
    package = _package(tmp_path / "big", big_source)
    store = FakePluginStore(package, permissions={})
    adapter = _make_adapter(store=store, max_message_bytes=1024)
    ctx = {"projectKey": "project-a"}
    with pytest.raises(PluginWorkerError, match="exceeds the size limit"):
        adapter._resolve("dist/worker.js", ctx, _execution())


# ---------------------------------------------------------------------------
# Static grant helpers
# ---------------------------------------------------------------------------


def test_read_grant_returns_none_when_data_not_a_list() -> None:
    assert NodePluginWorkerAdapter._read_grant({"data": "x"}, {}, "orders") is None


def test_read_grant_matches_active_collection() -> None:
    grant = {"collection": "$active", "operations": ["read"], "fields": ["id"]}
    result = NodePluginWorkerAdapter._read_grant(
        {"data": [grant]}, {"collection": "orders"}, "orders"
    )
    assert result is grant


def test_write_grant_returns_none_when_data_not_a_list() -> None:
    assert NodePluginWorkerAdapter._write_grant({"data": "x"}, {}, "orders") is None


def test_write_grant_matches_create_or_update() -> None:
    grant = {"collection": "orders", "operations": ["create"], "fields": ["name"]}
    result = NodePluginWorkerAdapter._write_grant({"data": [grant]}, {}, "orders")
    assert result is grant


def test_write_grant_skips_grants_without_create_or_update() -> None:
    grant = {"collection": "orders", "operations": ["read"], "fields": ["id"]}
    assert NodePluginWorkerAdapter._write_grant({"data": [grant]}, {}, "orders") is None


def test_allowed_fields_returns_intersection_for_explicit_list() -> None:
    grant = {"fields": ["id", "secret"]}
    profile = SimpleNamespace(fields=["id", "name"])
    result = NodePluginWorkerAdapter._allowed_fields(grant, profile)
    assert result == {"id"}


def test_allowed_fields_returns_empty_when_not_a_list() -> None:
    grant = {"fields": "id"}
    profile = SimpleNamespace(fields=["id"])
    assert NodePluginWorkerAdapter._allowed_fields(grant, profile) == set()


# ---------------------------------------------------------------------------
# Additional edge branches
# ---------------------------------------------------------------------------


@pytest.mark.asyncio
async def test_file_capability_tolerates_non_list_files_permission() -> None:
    """When permissions['files'] is not a list, declared falls back to []."""
    adapter = _make_adapter(file_adapter=FakeFileAdapter())
    resolved = _resolved({"files": "not-a-list"})
    with pytest.raises(PluginWorkerError, match=r"did not declare file.pickRead"):
        await adapter._file_capability(resolved, {}, "file.pickRead", {})


@pytest.mark.asyncio
async def test_profile_rejects_collection_outside_schema() -> None:
    class BadClient:
        async def describe_table(self, table_id: str) -> dict[str, Any]:
            return {"tableId": table_id, "fields": "not-a-list"}

    adapter = _make_adapter(profiles={}, client=BadClient())
    with pytest.raises(PluginWorkerError, match="outside the product schema"):
        await adapter._profile("unknown-collection")


def test_resolve_rejects_missing_current_revision(tmp_path: Path) -> None:
    store = FakePluginStore(tmp_path, permissions={})
    # Revision exists but state is not "current".
    store.revision.state = "stale"
    adapter = _make_adapter(store=store)
    ctx = {"projectKey": "project-a"}
    with pytest.raises(PluginWorkerError, match="revision is unavailable"):
        adapter._resolve("dist/worker.js", ctx, _execution())


def test_read_grant_skips_grant_without_read_operation() -> None:
    grant = {"collection": "orders", "operations": ["create"], "fields": ["id"]}
    result = NodePluginWorkerAdapter._read_grant({"data": [grant]}, {}, "orders")
    assert result is None


def test_write_grant_skips_grant_with_non_list_operations() -> None:
    grant = {"collection": "orders", "operations": "create", "fields": ["name"]}
    result = NodePluginWorkerAdapter._write_grant({"data": [grant]}, {}, "orders")
    assert result is None


# ---------------------------------------------------------------------------
# _bounded_json error branches
# ---------------------------------------------------------------------------


def test_bounded_json_rejects_non_serializable_value() -> None:
    adapter = _make_adapter()
    with pytest.raises(PluginWorkerError, match="is not valid JSON"):
        adapter._bounded_json(object(), "test-label")


def test_bounded_json_rejects_oversized_value() -> None:
    adapter = _make_adapter()
    with pytest.raises(PluginWorkerError, match="exceeds the size limit"):
        adapter._bounded_json("x" * 200, "test-label", limit=8)


# ---------------------------------------------------------------------------
# cancel + InMemoryPluginWorkerAdapter
# ---------------------------------------------------------------------------


@pytest.mark.asyncio
async def test_cancel_returns_false_for_unknown_run() -> None:
    adapter = _make_adapter()
    assert await adapter.cancel("unknown-run") is False


@pytest.mark.asyncio
async def test_in_memory_adapter_prepare_appends_execution_and_trace() -> None:
    trace: list[str] = []
    adapter = InMemoryPluginWorkerAdapter(trace=trace)
    result = await adapter.prepare(
        "dist/worker.js", {"projectKey": "p"}, {}, execution={"runId": "r1"}
    )
    assert result == {}
    assert "worker.prepare" in trace
    assert adapter.executions == [{"runId": "r1"}]


@pytest.mark.asyncio
async def test_in_memory_adapter_prepare_returns_seeded_result() -> None:
    adapter = InMemoryPluginWorkerAdapter(prepare_results={"dist/worker.js": {"ok": True}})
    result = await adapter.prepare("dist/worker.js", {}, {})
    assert result == {"ok": True}


@pytest.mark.asyncio
async def test_in_memory_adapter_run_appends_trace() -> None:
    trace: list[str] = []
    adapter = InMemoryPluginWorkerAdapter(run_results={"dist/worker.js": {"ok": True}}, trace=trace)
    result = await adapter.run("dist/worker.js", {}, {})
    assert trace == ["worker.run"]
    assert result == {"ok": True}


def test_in_memory_adapter_is_available() -> None:
    assert InMemoryPluginWorkerAdapter().available is True
