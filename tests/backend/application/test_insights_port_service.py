from __future__ import annotations

from typing import Any
from uuid import uuid4

import pytest

from backend.application.insights_service import (
    BUILT_IN_PANEL_MANIFEST,
    InsightsError,
    InsightsService,
    compile_dashboard_query,
    merge_global_filter,
    normalize_panel_type,
    parse_panel_type,
)
from backend.contracts.presets_versions_dashboards import (
    DashboardAggregateQuery,
    DashboardFilterState,
    DashboardFilterVariable,
    DashboardMeasure,
    DashboardPanelDraft,
    DashboardRecordQuery,
    DashboardWorkspaceParams,
    ExecuteDashboardQueryParams,
    PanelPosition,
    PresetView,
    SaveDashboardDraftParams,
)
from backend.contracts.query import FilterCondition, SortCondition

#: A valid UUID v4 string satisfying the DashboardUuid pattern.
_UUID = "11111111-1111-4111-8111-111111111111"


class FakeMetadataPort:
    def __init__(self, rows: dict[str, list[dict[str, Any]]] | None = None) -> None:
        self.rows = rows or {}
        self.calls: list[tuple[str, dict[str, Any]]] = []

    async def list_metadata(
        self,
        namespace: str,
        *,
        scope: str | None = None,
        keys: list[str] | None = None,
    ) -> list[dict[str, Any]]:
        self.calls.append(("list", {"namespace": namespace, "scope": scope, "keys": keys}))
        return list(self.rows.get(namespace, []))

    async def upsert_metadata(self, namespace: str, **kwargs: Any) -> dict[str, Any]:
        self.calls.append(("upsert", {"namespace": namespace, **kwargs}))
        return {"recordId": kwargs.get("record_id") or "generated"}

    async def delete_metadata(self, namespace: str, **kwargs: Any) -> dict[str, Any]:
        self.calls.append(("delete", {"namespace": namespace, **kwargs}))
        return {"deleted": kwargs["record_id"]}


class Page:
    def __init__(self, rows: list[dict[str, Any]]) -> None:
        self.rows = rows


class FakeQueryPort:
    """Configurable query port — every return value can be overridden per test."""

    def __init__(
        self,
        *,
        history_page: dict[str, Any] | None = None,
        preview: dict[str, Any] | None = None,
        aggregate_rows: list[dict[str, Any]] | None = None,
        page_rows: list[dict[str, Any]] | None = None,
        apply_result: dict[str, Any] | None = None,
    ) -> None:
        self.pages: list[tuple[str, dict[str, Any]]] = []
        self.aggregates: list[tuple[str, dict[str, Any]]] = []
        self.history_calls: list[tuple[str, dict[str, Any]]] = []
        self._history_page = history_page
        self._preview = preview
        self._aggregate_rows = aggregate_rows
        self._page_rows = page_rows
        self._apply_result = apply_result

    async def query_page(self, *, table_id: str, query: dict[str, Any]) -> Page:
        self.pages.append((table_id, query))
        return Page(
            list(self._page_rows if self._page_rows is not None else [{"id": "r1", "name": "Ada"}])
        )

    async def aggregate(self, *, table_id: str, query: dict[str, Any]) -> list[dict[str, Any]]:
        self.aggregates.append((table_id, query))
        return list(
            self._aggregate_rows
            if self._aggregate_rows is not None
            else [{"region": "east", "total": 12}]
        )

    async def read_history(
        self, *, collection: str, item_id: str, limit: int = 50
    ) -> dict[str, Any]:
        self.history_calls.append(
            ("read", {"collection": collection, "itemId": item_id, "limit": limit})
        )
        if self._history_page is not None:
            return self._history_page
        return {
            "changeSets": [
                {
                    "rootRevisionId": "revision-root-1",
                    "changeSetId": "change-1",
                    "recordChanges": [{"itemId": item_id, "revisionId": "revision-1"}],
                }
            ]
        }

    async def preview_history_restore(
        self, *, collection: str, item_id: str, target_revision: str
    ) -> dict[str, Any]:
        self.history_calls.append(
            (
                "preview",
                {
                    "collection": collection,
                    "itemId": item_id,
                    "targetRevision": target_revision,
                },
            )
        )
        if self._preview is not None:
            return self._preview
        return {
            "currentHash": "sha256:current",
            "scalarChanges": [{"field": "name", "before": "Current", "after": "Named version"}],
            "relationChanges": [],
            "token": "restore-token",
            "canApply": True,
        }

    async def apply_history_restore(
        self, *, collection: str, item_id: str, token: str
    ) -> dict[str, Any]:
        self.history_calls.append(
            (
                "apply",
                {"collection": collection, "itemId": item_id, "token": token},
            )
        )
        if self._apply_result is not None:
            return self._apply_result
        return {"restoredToRevision": "revision-1", "receipt": {"status": "committed"}}


@pytest.mark.asyncio
async def test_presets_use_logical_internal_metadata_namespace() -> None:
    metadata = FakeMetadataPort(
        {
            "presets": [
                {
                    "id": "p1",
                    "scope": "orders",
                    "name": "Open",
                    "presetScope": "personal",
                    "view": {"filters": [], "sorts": [], "visibleFields": ["id"]},
                }
            ]
        }
    )
    service = InsightsService(metadata_port=metadata, query_port=FakeQueryPort())

    result = await service.list_presets("orders")

    assert result.presets[0].id == "p1"
    assert metadata.calls == [("list", {"namespace": "presets", "scope": "orders", "keys": None})]


@pytest.mark.asyncio
async def test_save_preset_submits_internal_metadata_mutation() -> None:
    metadata = FakeMetadataPort()
    service = InsightsService(metadata_port=metadata, query_port=FakeQueryPort())

    saved = await service.save_preset(
        "orders",
        "Open",
        PresetView(
            visible_fields=["id", "status"],
            kind="kanban",
            title_field="title",
            group_field="status",
            cover_field="cover",
        ),
        "p1",
        "preset-op-1",
    )

    assert saved.id == "p1"
    operation, payload = metadata.calls[-1]
    assert operation == "upsert"
    assert payload["namespace"] == "presets"
    assert payload["record_id"] == "p1"
    assert payload["values"]["scope"] == "orders"
    assert payload["values"]["presetScope"] == "system"
    assert payload["values"]["view"]["kind"] == "kanban"
    assert payload["values"]["view"]["titleField"] == "title"
    assert payload["values"]["view"]["groupField"] == "status"
    assert payload["values"]["view"]["coverField"] == "cover"
    assert "token" not in repr(payload).lower()


@pytest.mark.asyncio
async def test_content_version_is_named_audit_snapshot_and_promotes_via_restore() -> None:
    metadata = FakeMetadataPort()
    query_port = FakeQueryPort()
    service = InsightsService(metadata_port=metadata, query_port=query_port)

    created = await service.create_version("orders", "row-1", "release", "Release", "version-op-1")

    operation, payload = metadata.calls[-1]
    assert operation == "upsert"
    assert payload["values"]["changeSetId"] == "change-1"
    assert payload["values"]["revisionId"] == "revision-1"
    assert "values" not in payload["values"]
    metadata.rows["content_versions"] = [
        {
            "id": created.id,
            "scope": "orders:row-1",
            "key": "release",
            "name": "Release",
            "mainHash": "sha256:current",
            "changeSetId": "change-1",
            "revisionId": "revision-1",
        }
    ]

    compared = await service.compare_version("orders", "row-1", created.id)
    promoted = await service.promote_version(
        "orders", "row-1", created.id, compared.main_hash, "promote-op-1"
    )

    assert compared.differences["name"] == {
        "main": "Current",
        "version": "Named version",
    }
    assert promoted["restoredToRevision"] == "revision-1"
    assert query_port.history_calls[-1][0] == "apply"


@pytest.mark.asyncio
async def test_preset_and_version_deletes_bind_revision_and_operation_id() -> None:
    metadata = FakeMetadataPort()
    service = InsightsService(metadata_port=metadata, query_port=FakeQueryPort())
    revision = "sha256:" + "a" * 64

    preset = await service.delete_preset("preset-1", revision, "preset-delete-1")
    version = await service.delete_version(
        "orders",
        "row-1",
        "version-1",
        revision,
        "version-delete-1",
    )

    assert preset["deleted"] == "preset-1"
    assert version["deleted"] == "version-1"
    assert metadata.calls[-2:] == [
        (
            "delete",
            {
                "namespace": "presets",
                "record_id": "preset-1",
                "expected_revision": revision,
                "idempotency_key": "preset:delete:preset-delete-1",
            },
        ),
        (
            "delete",
            {
                "namespace": "content_versions",
                "record_id": "version-1",
                "expected_revision": revision,
                "idempotency_key": "version:delete:version-delete-1",
            },
        ),
    ]


@pytest.mark.asyncio
async def test_record_dashboard_query_uses_typed_query_port() -> None:
    query_port = FakeQueryPort()
    service = InsightsService(metadata_port=FakeMetadataPort(), query_port=query_port)
    query = DashboardRecordQuery(
        collection="orders",
        fields=["id", "name"],
        filters=[FilterCondition(field="name", operator="contains", value="Ada")],
        sorts=[SortCondition(field="name", direction="asc")],
        limit=20,
    )

    result = await service.execute_dashboard_query(
        ExecuteDashboardQueryParams(panel_type="list", query=query)
    )

    assert result.rows == [{"id": "r1", "name": "Ada"}]
    table_id, wire_query = query_port.pages[-1]
    assert table_id == "orders"
    assert wire_query == {
        "keyword": None,
        "filters": [{"field": "name", "operator": "contains", "value": "Ada", "logic": "AND"}],
        "sorts": [{"field": "name", "direction": "asc", "nullsLast": True}],
        "offset": 0,
        "limit": 20,
    }


@pytest.mark.asyncio
async def test_aggregate_dashboard_query_uses_product_aggregate_ast() -> None:
    query_port = FakeQueryPort()
    service = InsightsService(metadata_port=FakeMetadataPort(), query_port=query_port)
    query = DashboardAggregateQuery(
        collection="orders",
        dimensions=["region"],
        measures=[DashboardMeasure(key="total", op="sum", field="amount")],
        limit=100,
    )

    result = await service.execute_dashboard_query(
        ExecuteDashboardQueryParams(panel_type="bar", query=query)
    )

    assert result.rows == [{"region": "east", "total": 12}]
    assert query_port.aggregates == [
        (
            "orders",
            {
                "filters": [],
                "groupBy": ["region"],
                "metrics": [{"function": "sum", "field": "amount", "alias": "total"}],
                "limit": 100,
            },
        )
    ]


@pytest.mark.asyncio
async def test_dashboard_panel_writes_are_metadata_mutations_only() -> None:
    metadata = FakeMetadataPort()
    service = InsightsService(metadata_port=metadata, query_port=FakeQueryPort())

    panel = await service.save_panel(
        "d1",
        "Revenue",
        "metric",
        PanelPosition(x=0, y=0, width=4, height=3),
        {"format": "currency"},
        {"kind": "aggregate"},
        "panel-1",
    )

    assert panel.id == "panel-1"
    assert metadata.calls[-1][1]["namespace"] == "panels"
    assert metadata.calls[-1][1]["values"]["dashboardId"] == "d1"


# ===========================================================================
# InsightsError shape + rpc_error_data
# ===========================================================================


def test_insights_error_carries_code_and_optional_field() -> None:
    plain = InsightsError("boom", code="x")
    assert plain.code == "x"
    assert plain.field is None
    assert plain.rpc_error_data == {"code": "x"}

    with_field = InsightsError("boom", code="x", field="name")
    assert with_field.rpc_error_data == {"code": "x", "field": "name"}


# ===========================================================================
# operation_id guard across preset/version write methods
# ===========================================================================


@pytest.mark.asyncio
async def test_save_preset_requires_operation_id() -> None:
    service = InsightsService(metadata_port=FakeMetadataPort(), query_port=FakeQueryPort())
    with pytest.raises(InsightsError, match="operationId is required"):
        await service.save_preset("orders", "n", PresetView(), "p1", "")


@pytest.mark.asyncio
async def test_create_version_requires_operation_id() -> None:
    service = InsightsService(metadata_port=FakeMetadataPort(), query_port=FakeQueryPort())
    with pytest.raises(InsightsError, match="operationId is required"):
        await service.create_version("orders", "row-1", "k", "n", "")


@pytest.mark.asyncio
async def test_promote_version_requires_operation_id() -> None:
    service = InsightsService(metadata_port=FakeMetadataPort(), query_port=FakeQueryPort())
    with pytest.raises(InsightsError, match="operationId is required"):
        await service.promote_version("orders", "row-1", "v1", "sha256:current", "")


# ===========================================================================
# save_version (both branches)
# ===========================================================================


@pytest.mark.asyncio
async def test_save_version_rejects_arbitrary_values() -> None:
    service = InsightsService(metadata_port=FakeMetadataPort(), query_port=FakeQueryPort())
    with pytest.raises(InsightsError, match="content versions point to audit snapshots"):
        await service.save_version("orders", "row-1", "v1", {"foo": 1}, "op")


@pytest.mark.asyncio
async def test_save_version_success_binds_audit_revision() -> None:
    metadata = FakeMetadataPort()
    service = InsightsService(metadata_port=metadata, query_port=FakeQueryPort())
    result = await service.save_version("orders", "row-1", "v1", {}, "op")
    assert result["saved"] == "v1"
    # Note: changeSetId is overwritten by _receipt_trace's empty changeSetId key.
    assert result["changeSetId"] == ""
    assert result["revisionId"] == "revision-1"
    assert result["metadataRevision"] == ""
    # receipt_trace adds status + emittedEvents (empty from the bare fake).
    assert result["status"] == ""
    assert result["emittedEvents"] == []
    op, payload = metadata.calls[-1]
    assert op == "upsert"
    assert payload["idempotency_key"] == "version:save:op"


# ===========================================================================
# list_versions
# ===========================================================================


@pytest.mark.asyncio
async def test_list_versions_returns_content_version_entries() -> None:
    metadata = FakeMetadataPort(
        {
            "content_versions": [
                {
                    "id": "v1",
                    "key": "release",
                    "name": "Release",
                    "mainHash": "sha256:current",
                    "revision": "rev-9",
                }
            ]
        }
    )
    service = InsightsService(metadata_port=metadata, query_port=FakeQueryPort())
    result = await service.list_versions("orders", "row-1")
    assert len(result.versions) == 1
    entry = result.versions[0]
    assert entry.id == "v1"
    assert entry.key == "release"
    assert entry.name == "Release"
    assert entry.main_hash == "sha256:current"
    assert entry.revision == "rev-9"


# ===========================================================================
# _latest_audit_revision error matrix (via create_version)
# ===========================================================================


@pytest.mark.asyncio
@pytest.mark.parametrize(
    "history_page",
    [
        {},
        {"changeSets": []},
        {"changeSets": "not-a-list"},
    ],
)
async def test_create_version_raises_when_audit_history_missing(
    history_page: dict[str, Any],
) -> None:
    service = InsightsService(
        metadata_port=FakeMetadataPort(),
        query_port=FakeQueryPort(history_page=history_page),
    )
    with pytest.raises(InsightsError, match="an audit revision is required"):
        await service.create_version("orders", "row-1", "k", "n", "op")


@pytest.mark.asyncio
async def test_create_version_raises_when_change_set_not_a_dict() -> None:
    service = InsightsService(
        metadata_port=FakeMetadataPort(),
        query_port=FakeQueryPort(history_page={"changeSets": ["not-a-dict"]}),
    )
    with pytest.raises(InsightsError, match="audit"):
        await service.create_version("orders", "row-1", "k", "n", "op")


@pytest.mark.asyncio
async def test_create_version_raises_when_change_set_has_no_identity() -> None:
    service = InsightsService(
        metadata_port=FakeMetadataPort(),
        query_port=FakeQueryPort(
            history_page={"changeSets": [{"rootRevisionId": "rev-1", "recordChanges": []}]}
        ),
    )
    with pytest.raises(InsightsError, match="audit"):
        await service.create_version("orders", "row-1", "k", "n", "op")


# ===========================================================================
# compare_version error branches
# ===========================================================================


@pytest.mark.asyncio
async def test_compare_version_raises_when_not_found() -> None:
    service = InsightsService(metadata_port=FakeMetadataPort(), query_port=FakeQueryPort())
    with pytest.raises(InsightsError, match="content version was not found"):
        await service.compare_version("orders", "row-1", "missing-id")


@pytest.mark.asyncio
async def test_compare_version_raises_when_revision_missing() -> None:
    metadata = FakeMetadataPort({"content_versions": [{"id": "v1", "revisionId": None}]})
    service = InsightsService(metadata_port=metadata, query_port=FakeQueryPort())
    with pytest.raises(InsightsError, match="content version has no audit revision"):
        await service.compare_version("orders", "row-1", "v1")


@pytest.mark.asyncio
async def test_compare_version_includes_relation_changes() -> None:
    metadata = FakeMetadataPort(
        {"content_versions": [{"id": "v1", "revisionId": "rev-1", "mainHash": "sha256:current"}]}
    )
    service = InsightsService(
        metadata_port=metadata,
        query_port=FakeQueryPort(
            preview={
                "currentHash": "sha256:current",
                "scalarChanges": [],
                "relationChanges": [
                    {"field": "tags", "beforeItemId": "old", "afterItemId": "new"},
                ],
                "token": "restore-token",
                "canApply": True,
            }
        ),
    )
    result = await service.compare_version("orders", "row-1", "v1")
    assert result.differences["tags"] == {"main": "old", "version": "new"}


# ===========================================================================
# promote_version error branches
# ===========================================================================


@pytest.mark.asyncio
async def test_promote_version_raises_when_not_found() -> None:
    service = InsightsService(metadata_port=FakeMetadataPort(), query_port=FakeQueryPort())
    with pytest.raises(InsightsError, match="content version was not found"):
        await service.promote_version("orders", "row-1", "missing", "sha256:current", "op")


@pytest.mark.asyncio
async def test_promote_version_raises_when_revision_missing() -> None:
    metadata = FakeMetadataPort({"content_versions": [{"id": "v1", "revisionId": ""}]})
    service = InsightsService(metadata_port=metadata, query_port=FakeQueryPort())
    with pytest.raises(InsightsError, match="content version has no audit revision"):
        await service.promote_version("orders", "row-1", "v1", "sha256:current", "op")


@pytest.mark.asyncio
async def test_promote_version_raises_on_main_hash_conflict() -> None:
    metadata = FakeMetadataPort(
        {"content_versions": [{"id": "v1", "revisionId": "rev-1", "mainHash": "sha256:old"}]}
    )
    service = InsightsService(metadata_port=metadata, query_port=FakeQueryPort())
    with pytest.raises(InsightsError, match="main item changed after the version comparison"):
        await service.promote_version("orders", "row-1", "v1", "sha256:different", "op")


@pytest.mark.asyncio
async def test_promote_version_raises_when_not_restorable() -> None:
    metadata = FakeMetadataPort(
        {"content_versions": [{"id": "v1", "revisionId": "rev-1", "mainHash": "sha256:current"}]}
    )
    service = InsightsService(
        metadata_port=metadata,
        query_port=FakeQueryPort(
            preview={
                "currentHash": "sha256:current",
                "scalarChanges": [],
                "relationChanges": [],
                "token": "restore-token",
                "canApply": False,
            }
        ),
    )
    with pytest.raises(InsightsError, match="content version cannot be promoted"):
        await service.promote_version("orders", "row-1", "v1", "sha256:current", "op")


# ===========================================================================
# Dashboard cluster: list / read / workspace / save / delete / panel
# ===========================================================================


@pytest.mark.asyncio
async def test_list_dashboards_aggregates_panels() -> None:
    metadata = FakeMetadataPort(
        {
            "dashboards": [
                {"id": "d1", "name": "Revenue", "note": "Q1"},
            ],
            "panels": [
                {
                    "id": "p1",
                    "dashboardId": "d1",
                    "name": "Total",
                    "type": "metric",
                    "position": {"x": 0, "y": 0, "width": 4, "height": 4},
                    "options": {"format": "currency"},
                    "query": {"kind": "aggregate"},
                },
                {
                    "id": "p2",
                    "dashboardId": "other",
                    "name": "Other",
                    "type": "metric",
                },
            ],
        }
    )
    service = InsightsService(metadata_port=metadata, query_port=FakeQueryPort())
    result = await service.list_dashboards(None)
    assert len(result.dashboards) == 1
    dashboard = result.dashboards[0]
    assert dashboard.id == "d1"
    assert dashboard.name == "Revenue"
    # Only panels matching dashboardId are attached.
    assert len(dashboard.panels) == 1
    assert dashboard.panels[0].id == "p1"


@pytest.mark.asyncio
async def test_read_dashboard_raises_when_not_found() -> None:
    metadata = FakeMetadataPort({"dashboards": [{"id": "d1", "name": "X"}]})
    service = InsightsService(metadata_port=metadata, query_port=FakeQueryPort())
    with pytest.raises(InsightsError, match="dashboard was not found"):
        await service.read_dashboard("missing")


@pytest.mark.asyncio
async def test_read_dashboard_workspace_binds_revision() -> None:
    metadata = FakeMetadataPort(
        {
            "dashboards": [
                {"id": "d1", "name": "Rev", "config": {"refreshInterval": 30}},
            ],
            "panels": [],
        }
    )
    service = InsightsService(metadata_port=metadata, query_port=FakeQueryPort())
    result = await service.read_dashboard_workspace("d1")
    assert result.dashboard.id == "d1"
    assert result.config.refresh_interval == 30
    assert len(result.revision) == 64


@pytest.mark.asyncio
async def test_save_dashboard_creates_or_updates_entry() -> None:
    metadata = FakeMetadataPort()
    service = InsightsService(metadata_port=metadata, query_port=FakeQueryPort())
    created = await service.save_dashboard("Revenue", "note", None)
    assert created.name == "Revenue"
    assert created.note == "note"
    assert created.id  # uuid generated
    op, payload = metadata.calls[-1]
    assert op == "upsert"
    assert payload["namespace"] == "dashboards"
    assert payload["record_id"] == created.id


@pytest.mark.asyncio
async def test_delete_dashboard_returns_id() -> None:
    metadata = FakeMetadataPort()
    service = InsightsService(metadata_port=metadata, query_port=FakeQueryPort())
    result = await service.delete_dashboard("d1")
    assert result == {"deleted": "d1"}
    op, payload = metadata.calls[-1]
    assert op == "delete"
    assert payload["idempotency_key"] == "dashboard:delete:d1"


@pytest.mark.asyncio
async def test_delete_panel_returns_id() -> None:
    metadata = FakeMetadataPort()
    service = InsightsService(metadata_port=metadata, query_port=FakeQueryPort())
    result = await service.delete_panel("d1", "p1")
    assert result == {"deleted": "p1"}


@pytest.mark.asyncio
async def test_delete_dashboard_workspace_deletes_dashboard() -> None:
    metadata = FakeMetadataPort({"dashboards": [{"id": _UUID, "name": "X"}]})
    service = InsightsService(metadata_port=metadata, query_port=FakeQueryPort())
    result = await service.delete_dashboard_workspace(DashboardWorkspaceParams(dashboard_id=_UUID))
    assert result == {"deleted": _UUID}


# ===========================================================================
# save_dashboard_draft (atomic endpoint)
# ===========================================================================


@pytest.mark.asyncio
async def test_save_dashboard_draft_requires_atomic_port() -> None:
    service = InsightsService(metadata_port=FakeMetadataPort(), query_port=FakeQueryPort())
    with pytest.raises(InsightsError, match="atomic dashboard mutation port is unavailable"):
        await service.save_dashboard_draft(
            SaveDashboardDraftParams(idempotency_key=_UUID, name="Draft")
        )


@pytest.mark.asyncio
async def test_save_dashboard_draft_rejects_non_dict_response() -> None:
    metadata: Any = FakeMetadataPort()

    async def _commit(_payload: dict[str, Any]) -> list[str]:
        return ["not-a-dict"]

    metadata.commit_dashboard = _commit
    service = InsightsService(metadata_port=metadata, query_port=FakeQueryPort())
    with pytest.raises(InsightsError, match="atomic dashboard mutation returned invalid data"):
        await service.save_dashboard_draft(
            SaveDashboardDraftParams(idempotency_key=_UUID, name="Draft")
        )


@pytest.mark.asyncio
async def test_save_dashboard_draft_success_returns_workspace() -> None:
    workspace = {
        "dashboard": {"id": _UUID, "name": "Draft"},
        "config": {},
        "revision": "a" * 64,
    }
    client_panel_ids = {"c1": _UUID}

    metadata: Any = FakeMetadataPort()

    async def _commit(_payload: dict[str, Any]) -> dict[str, Any]:
        return {"workspace": workspace, "clientPanelIds": client_panel_ids}

    metadata.commit_dashboard = _commit
    service = InsightsService(metadata_port=metadata, query_port=FakeQueryPort())
    result = await service.save_dashboard_draft(
        SaveDashboardDraftParams(idempotency_key=_UUID, name="Draft")
    )
    assert result.workspace.dashboard.id == _UUID
    assert result.client_panel_ids == client_panel_ids


# ===========================================================================
# execute_dashboard_query + save_panel bad-type guards
# ===========================================================================


@pytest.mark.asyncio
async def test_execute_dashboard_query_rejects_custom_panel_type() -> None:
    service = InsightsService(metadata_port=FakeMetadataPort(), query_port=FakeQueryPort())
    with pytest.raises(InsightsError, match="panel type"):
        await service.execute_dashboard_query(
            ExecuteDashboardQueryParams(
                panel_type="custom",
                query=DashboardRecordQuery(collection="orders", fields=["id"]),
            )
        )


@pytest.mark.asyncio
async def test_save_panel_rejects_unknown_panel_type() -> None:
    service = InsightsService(metadata_port=FakeMetadataPort(), query_port=FakeQueryPort())
    with pytest.raises(InsightsError, match="panel type"):
        await service.save_panel(
            "d1",
            "X",
            "custom",  # type: ignore[arg-type]
            PanelPosition(x=0, y=0, width=4, height=4),
            {},
            {},
            "p1",
        )


# ===========================================================================
# Sync helpers + free functions
# ===========================================================================


def test_panel_manifest_and_query_limits_are_published() -> None:
    service = InsightsService(metadata_port=FakeMetadataPort(), query_port=FakeQueryPort())
    manifest = service.panel_manifest(None)
    assert {entry.type for entry in manifest.panels} == {
        entry.type for entry in BUILT_IN_PANEL_MANIFEST
    }
    assert manifest.manifest_version

    limits = service.dashboard_query_limits(None)
    assert limits.max_concurrent_requests == 6


def test_is_known_panel_type_recognizes_manifest() -> None:
    service = InsightsService(metadata_port=FakeMetadataPort(), query_port=FakeQueryPort())
    assert service.is_known_panel_type("metric") is True
    assert service.is_known_panel_type("custom") is False
    assert service.is_known_panel_type("nope") is False


def test_normalize_panel_type_freezes_options() -> None:
    canonical, frozen = normalize_panel_type("metric", {"b": 2, "a": 1})
    assert canonical == "metric"
    assert frozen == {"a": 1, "b": 2}


def test_parse_panel_type_known_and_unknown_branches() -> None:
    known, _ = parse_panel_type("metric", {})
    assert known == "metric"

    fallback, _ = parse_panel_type("weird", {})
    assert fallback == "custom"


def test_panel_point_cap_uses_list_and_pie_bounds() -> None:
    # list panel type -> max_list_rows cap
    list_query = compile_dashboard_query(
        DashboardAggregateQuery(
            collection="orders",
            measures=[DashboardMeasure(key="m", op="count")],
            limit=10_000,
        ),
        panel_type="list",
    )
    assert list_query.max_points == 100  # max_list_rows

    # pie/donut -> max_pie_slices cap
    pie_query = compile_dashboard_query(
        DashboardAggregateQuery(
            collection="orders",
            measures=[DashboardMeasure(key="m", op="count")],
            limit=10_000,
        ),
        panel_type="pie",
    )
    assert pie_query.max_points == 50  # max_pie_slices

    donut_query = compile_dashboard_query(
        DashboardAggregateQuery(
            collection="orders",
            measures=[DashboardMeasure(key="m", op="count")],
            limit=10_000,
        ),
        panel_type="donut",
    )
    assert donut_query.max_points == 50


def test_merge_global_filter_applies_only_targeted_boundings() -> None:
    base_filter = {"field": "name", "operator": "eq", "value": "x", "logic": "AND"}
    variable = DashboardFilterVariable(
        key="region",
        type="enum",
        default_value="east",
        target_panels=["p1", "p2"],
        field_bindings={"p1": "region"},
        allowed_fields=["region"],
    )
    state = DashboardFilterState(values={"region": "west"})

    merged = merge_global_filter([base_filter], [variable], state, "p1")
    assert len(merged) == 2
    assert merged[-1] == {"field": "region", "operator": "eq", "value": "west", "logic": "AND"}

    # Panel not in targets -> variable skipped.
    merged_other = merge_global_filter([base_filter], [variable], state, "p9")
    assert merged_other == [base_filter]


def test_merge_global_filter_skips_none_value_and_missing_binding() -> None:
    base = {"field": "f", "operator": "eq", "value": 1, "logic": "AND"}
    no_value_var = DashboardFilterVariable(key="k", type="enum", field_bindings={"p": "f"})
    no_binding_var = DashboardFilterVariable(
        key="k2", type="enum", default_value="v", field_bindings={}
    )
    state = DashboardFilterState(values={})
    # no_value_var: value None -> skip; no_binding_var: no field for "p" -> skip.
    merged = merge_global_filter([base], [no_value_var, no_binding_var], state, "p")
    assert merged == [base]


def test_merge_global_filter_skips_when_field_not_in_allowed() -> None:
    base = {"field": "f", "operator": "eq", "value": 1, "logic": "AND"}
    variable = DashboardFilterVariable(
        key="k",
        type="enum",
        default_value="v",
        field_bindings={"p": "secret"},
        allowed_fields=["public"],
    )
    state = DashboardFilterState()
    merged = merge_global_filter([base], [variable], state, "p")
    assert merged == [base]


# ===========================================================================
# Receipt helpers: _receipt_revision + _string_list via enriched receipt
# ===========================================================================


@pytest.mark.asyncio
async def test_create_version_exposes_receipt_revision_and_events() -> None:
    metadata: Any = FakeMetadataPort()

    async def _upsert(_namespace: str, **kwargs: Any) -> dict[str, Any]:
        return {
            "recordId": kwargs.get("record_id", ""),
            "item": {"revision": "rev-9"},
            "status": "committed",
            "emittedEvents": ["e1", "e2"],
        }

    metadata.upsert_metadata = _upsert
    service = InsightsService(metadata_port=metadata, query_port=FakeQueryPort())
    entry = await service.create_version("orders", "row-1", "k", "n", "op")
    assert entry.revision == "rev-9"
    assert entry.emitted_events == ["e1", "e2"]


def test_dashboard_panel_draft_round_trips() -> None:
    """Smoke-test the DashboardPanelDraft contract used by save_dashboard_draft."""
    draft = DashboardPanelDraft(
        client_id=str(uuid4()),
        panel_id=_UUID,
        type="metric",
        position=PanelPosition(x=0, y=0, width=4, height=4),
        options={"format": "currency"},
    )
    assert draft.type == "metric"
    assert draft.options == {"format": "currency"}
