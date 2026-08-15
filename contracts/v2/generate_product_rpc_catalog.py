#!/usr/bin/env python3
"""Generate typed request/event golden cases for every registered product RPC."""

from __future__ import annotations

import argparse
import ast
import datetime as dt
import enum
import inspect
import json
import types
import typing
from dataclasses import dataclass
from pathlib import Path

from pydantic import BaseModel, RootModel, TypeAdapter

import backend.__main__ as composition
from backend.contracts.product_rpc import (
    PRODUCT_RPC_REGISTRY as PRODUCT_PARAM_MODELS,
    ProductParams,
)
from backend.contracts.data_io import (
    ApplyImportResult,
    ExportResult,
    ImportPlan,
    TemplateResult,
)
from backend.contracts.grid_state import GridStateResult
from backend.contracts.generated_workbench import (
    ContentProfileDeleteResult,
    ContentProfileSnapshot,
    InterfaceDeleteResult,
    InterfaceListResult,
    InterfaceSnapshot,
    RecordDocumentLinkDeleteResult,
    RecordDocumentLinkListResult,
    RecordDocumentLinkSnapshot,
)
from backend.contracts.history import (
    HistoryPage,
    RestorePreview,
    RestoreResult,
)
from backend.contracts.lookup import (
    LookupCellValue,
    LookupListResult,
    LookupQueryResult,
)
from backend.contracts.query import (
    QueryCursorWindowResult,
    QueryPageResult,
    QueryViewResult,
)
from backend.contracts.paste import ApplyPasteResult, PastePlan
from backend.contracts.plugin import (
    ActionAvailability,
    InstallPlan,
    InteractionResolveResult,
    PluginAuditEvent,
    PluginEventEnvelope,
    PluginSnapshot,
    PluginTaskSnapshot,
    UninstallResult,
)
from backend.contracts.presets_versions_dashboards import (
    ContentVersionEntry,
    DashboardQueryLimits,
    DashboardQueryResult,
    DashboardsResult,
    DashboardWorkspaceResult,
    PanelManifestResult,
    PresetEntry,
    PresetsResult,
    SaveDashboardDraftResult,
    VersionCompareResult,
    VersionsResult,
)
from backend.contracts.relation_admin import (
    RelationCreateTargetResult,
    RelationDeltaPreview,
    RelationDeltaResult,
    RelationSearchResult,
    RelationSingleUpdateResult,
    SchemaDescribeResult,
)
from backend.contracts.schema_v2 import SchemaSnapshotV2
from backend.contracts.settings_commands import (
    CommandResult,
    CommandsResult,
    DeviceSettings,
    LaunchActionResult,
    SharedSettingsResult,
    ShortcutEntry,
    ShortcutsResult,
)
from backend.contracts.system import HandshakeResult
from backend.contracts.task import SessionPathGrant, TaskStatus

REPO_ROOT = Path(__file__).resolve().parents[2]


@dataclass(frozen=True)
class ResultSpec:
    """One method's actual public response model and representative value."""

    model_name: str
    annotation: object | None = None
    example: object | None = None
    schema: dict[str, object] | None = None


def _text(name: str) -> str:
    if name in {"field_id", "fieldId"}:
        return "fld_00000001"
    if name in {"provider_field_id", "providerFieldId"}:
        return "pb_00000001"
    if name in {"physical_name", "physicalName"}:
        return "f_00000001"
    if name in {"created_at", "createdAt", "occurred_at", "occurredAt"}:
        return "2026-07-24T08:31:00Z"
    if name in {"started_at", "startedAt", "finished_at", "finishedAt"}:
        return "2026-07-24T08:31:00Z"
    if name in {"hash", "main_hash", "mainHash", "package_hash", "packageHash"}:
        return "a" * 64
    if name in {"schema_revision", "schemaRevision"}:
        return "schema_0001"
    if name in {"data_revision", "dataRevision"}:
        return "data_0001"
    if "revision" in name.lower():
        return "a" * 64
    if name in {"version", "plugin_version", "pluginVersion"}:
        return "1.0.0"
    if name in {"project_key", "projectKey"}:
        return "local:default"
    if name in {"plugin_id", "pluginId"}:
        return "sample-plugin"
    if name in {"mime_type", "mimeType"}:
        return "text/csv"
    if name in {"digest"}:
        return "sha256:" + ("a" * 64)
    if name in {"path"}:
        return "C:/VibeTable/sample.csv"
    if name in {"kind"}:
        return "data.import"
    if name in {"format"}:
        return "csv"
    if name.endswith("_id") or name.endswith("Id") or "idempotency" in name.lower():
        return "00000000-0000-4000-8000-000000000001"
    if name in {"collection", "table_id", "tableId"}:
        return "orders"
    return "sample-value"


def _model_annotation(annotation: object) -> type[BaseModel] | None:
    origin = typing.get_origin(annotation)
    arguments = typing.get_args(annotation)
    if origin is typing.Annotated:
        return _model_annotation(arguments[0])
    if origin in (typing.Union, types.UnionType):
        for choice in arguments:
            if choice is not type(None) and (model := _model_annotation(choice)) is not None:
                return model
        return None
    if inspect.isclass(annotation) and issubclass(annotation, BaseModel):
        return annotation
    return None


def _value(
    annotation: object,
    name: str,
    model_stack: tuple[type[BaseModel], ...] = (),
) -> object:
    if name == "archive_policy":
        return {"mode": "none", "fieldId": None, "archivedValue": None}
    origin = typing.get_origin(annotation)
    arguments = typing.get_args(annotation)
    if origin is typing.Annotated:
        return _value(arguments[0], name, model_stack)
    if origin in (typing.Union, types.UnionType):
        choices = [item for item in arguments if item is not type(None)]
        return _value(choices[0], name, model_stack) if choices else None
    if origin is typing.Literal:
        return arguments[0]
    if origin is list:
        item_type = arguments[0] if arguments else typing.Any
        if (model := _model_annotation(item_type)) is not None and model in model_stack:
            return []
        return [_value(item_type, name, model_stack)]
    if origin is tuple:
        return [_value(item, name, model_stack) for item in arguments if item is not Ellipsis]
    if origin in (dict, typing.Mapping):
        return {}
    if annotation is typing.Any:
        return {}
    if inspect.isclass(annotation) and issubclass(annotation, BaseModel):
        return _model_payload(annotation, model_stack)
    if inspect.isclass(annotation) and issubclass(annotation, enum.Enum):
        return next(iter(annotation)).value
    if annotation is dt.datetime:
        return "2026-07-24T08:31:00Z"
    if annotation is dt.date:
        return "2026-07-24"
    if annotation is str:
        return _text(name)
    if annotation is int:
        return 1
    if annotation is float:
        return 1.0
    if annotation is bool:
        return False
    return {}


def _product_payload(model: type[ProductParams]) -> dict[str, object]:
    result: dict[str, object] = {}
    for name in sorted(model._required_fields):
        expected = model._field_types.get(name, (str,))
        value_type = expected[0]
        if value_type is str:
            result[name] = _text(name)
        elif value_type is int:
            result[name] = 1
        elif value_type is bool:
            result[name] = False
        elif value_type is list:
            result[name] = []
        else:
            result[name] = {}
    return model.model_validate(result).model_dump(mode="json")


def _model_payload(
    model: type[BaseModel],
    model_stack: tuple[type[BaseModel], ...] = (),
) -> dict[str, object]:
    model_stack = (*model_stack, model)
    if issubclass(model, ProductParams):
        return _product_payload(model)
    if issubclass(model, RootModel):
        value = _value(model.model_fields["root"].annotation, "root", model_stack)
        return model.model_validate(value).model_dump(mode="json")
    raw: dict[str, object] = {}
    for name, field in model.model_fields.items():
        if field.is_required() or name.endswith("_at"):
            if model.__module__ == "backend.contracts.backup" and name == "name":
                value: object = "manual_20260724_083100.zip"
            elif model.__module__ == "backend.contracts.backup" and name == "sha256":
                value = "a" * 64
            else:
                value = _value(field.annotation, name, model_stack)
            raw[field.alias or name] = value
    return model.model_validate(raw).model_dump(mode="json", by_alias=True)


def _registered_models() -> dict[str, type[BaseModel]]:
    tree = ast.parse((REPO_ROOT / "backend" / "__main__.py").read_text(encoding="utf-8"))
    result: dict[str, type[BaseModel]] = {}
    for call in ast.walk(tree):
        if (
            isinstance(call, ast.Call)
            and isinstance(call.func, ast.Attribute)
            and call.func.attr == "register"
            and len(call.args) >= 3
            and isinstance(call.args[0], ast.Constant)
            and isinstance(call.args[0].value, str)
            and isinstance(call.args[2], ast.Name)
        ):
            result[call.args[0].value] = getattr(
                composition,
                call.args[2].id,
            )
    result.update(PRODUCT_PARAM_MODELS)
    return result


def _schema_from_example(value: object) -> dict[str, object]:
    """Create a closed schema for legacy handlers whose real contract is a dict."""

    if value is None:
        return {"type": "null"}
    if isinstance(value, bool):
        return {"type": "boolean"}
    if isinstance(value, int):
        return {"type": "integer"}
    if isinstance(value, float):
        return {"type": "number"}
    if isinstance(value, str):
        return {"type": "string"}
    if isinstance(value, list):
        return {
            "type": "array",
            "items": _schema_from_example(value[0]) if value else {},
        }
    if isinstance(value, dict):
        if not value:
            return {"type": "object", "additionalProperties": True}
        return {
            "type": "object",
            "additionalProperties": False,
            "required": sorted(value),
            "properties": {key: _schema_from_example(item) for key, item in sorted(value.items())},
        }
    raise TypeError(f"unsupported response example value: {type(value)!r}")


def _sanitize_schema(value: object) -> object:
    """Keep executable wire constraints while removing docs/provider vocabulary."""

    if isinstance(value, list):
        return [_sanitize_schema(item) for item in value]
    if not isinstance(value, dict):
        return value
    result: dict[str, object] = {}
    for key, item in value.items():
        if key in {"description", "title"} and isinstance(item, str):
            continue
        if key == "examples" and isinstance(item, list):
            continue
        if key == "enum" and isinstance(item, list):
            retired_provider = "".join(["di", "rectus"])
            item = [
                candidate
                for candidate in item
                if str(candidate).casefold() not in {retired_provider, "pocketbase"}
            ]
        result[key] = _sanitize_schema(item)
    return result


def _manual(
    name: str,
    example: object,
    schema: dict[str, object] | None = None,
) -> ResultSpec:
    return ResultSpec(
        model_name=name,
        example=example,
        schema=schema or _schema_from_example(example),
    )


def _typed(annotation: object, name: str | None = None) -> ResultSpec:
    if name is None:
        name = getattr(annotation, "__name__", str(annotation).replace(" ", ""))
    return ResultSpec(model_name=name, annotation=annotation)


def _result_specs(fixtures: Path) -> dict[str, ResultSpec]:
    table = json.loads((fixtures / "table-definition.json").read_text(encoding="utf-8"))
    mutation_receipt = json.loads((fixtures / "mutation-receipt.json").read_text(encoding="utf-8"))
    restore_result = _model_payload(RestoreResult)
    table_schema = {"$ref": "#/$defs/TableDefinition"}
    receipt_schema = {"$ref": "#/$defs/MutationReceipt"}
    delete_trace = {
        "deleted": "00000000-0000-4000-8000-000000000001",
        "revision": "revision-1",
        "changeSetId": "change-set-1",
        "emittedEvents": ["data.changed"],
    }
    query_page = {
        "rows": [{"id": "row-1", "name": "Ada"}],
        "offset": 0,
        "limit": 50,
        "filteredRows": 1,
        "totalRows": 1,
        "snapshot": {
            "snapshotId": "snapshot-1",
            "digest": "a" * 64,
            "databaseId": "local",
            "table": "orders",
            "schemaRevision": "schema_0001",
            "dataRevision": 1,
            "normalizedQuery": {
                "keyword": "",
                "filters": [],
                "sorts": [],
                "offset": 0,
                "limit": 50,
            },
        },
    }
    query_view = {
        "page": query_page,
        "groupRows": [
            {
                "key": ["east", "open"],
                "count": 3,
                "summaries": [30],
                "parentCount": 9,
                "parentSummaries": [90],
            }
        ],
        "groupOffset": 0,
        "groupLimit": 100,
        "hasMoreGroups": False,
    }
    specs: dict[str, ResultSpec] = {
        "command.list": _typed(CommandsResult),
        "command.run": _typed(CommandResult),
        "data.applyImport": _typed(ApplyImportResult),
        "data.export": _typed(ExportResult),
        "data.generateTemplate": _typed(TemplateResult),
        "data.previewImport": _typed(ImportPlan),
        "events.reconcile": _manual(
            "RealtimeReconcileResult",
            {
                "tableId": "orders",
                "clientSchemaRevision": "schema_0001",
                "clientDataRevision": "data_0001",
                "currentSchemaRevision": "schema_0001",
                "currentDataRevision": "data_0002",
                "action": "refresh-data",
            },
        ),
        "file.applyHostChange": _manual(
            "MutationReceipt",
            mutation_receipt,
            receipt_schema,
        ),
        "file.list": _manual(
            "ManagedAttachmentListResult",
            {
                "attachments": [
                    {
                        "contractVersion": "2.0",
                        "tableId": "orders",
                        "recordId": "order-1",
                        "fieldId": "invoice",
                        "storedName": "invoice_01.pdf",
                        "originalName": "invoice.pdf",
                        "mimeType": "application/pdf",
                        "size": 128,
                        "sha256": "a" * 64,
                        "downloadCapability": "capability-1",
                        "thumbnails": [
                            {
                                "variant": "small",
                                "downloadCapability": "capability-thumb-1",
                            }
                        ],
                    }
                ]
            },
        ),
        "file.saveHostFile": _manual(
            "SaveHostFileResult",
            {"contractVersion": "2.0", "saved": True, "bytes": 128},
        ),
        "file.token": _manual(
            "FileTokenResult",
            {"contractVersion": "2.0", "downloadCapability": "capability-1"},
        ),
        "formula.preview": _manual(
            "FormulaPreviewResult",
            {"values": {"subtotal": 12.5}},
        ),
        "formula.draft.validate": _manual(
            "FormulaDraftValidationResult",
            {
                "canonicalSource": 'relationSum(f_lines, "f_amount")',
                "resultType": "number",
                "dependencies": ["fld_lines"],
                "relationAggregatePaths": ["f_lines.f_amount"],
            },
        ),
        "formula.validate": _manual(
            "FormulaValidationResult",
            {
                "formulas": [
                    {
                        "fieldId": "subtotal",
                        "canonicalSource": "quantity * unit_price",
                        "astHash": "a" * 64,
                        "dependencies": ["quantity", "unit_price"],
                    }
                ]
            },
        ),
        "gridState.get": _typed(GridStateResult),
        "gridState.save": _typed(GridStateResult),
        "history.applyRestore": _typed(RestoreResult),
        "history.previewRestore": _typed(RestorePreview),
        "history.read": _typed(HistoryPage),
        "insights.dashboardQueryLimits": _typed(DashboardQueryLimits),
        "insights.deleteDashboardWorkspace": _manual(
            "DeleteDashboardWorkspaceResult",
            {"deleted": "dashboard-1"},
        ),
        "insights.executeDashboardQuery": _typed(DashboardQueryResult),
        "insights.listDashboards": _typed(DashboardsResult),
        "insights.panelManifest": _typed(PanelManifestResult),
        "insights.readDashboardWorkspace": _typed(DashboardWorkspaceResult),
        "insights.saveDashboardDraft": _typed(SaveDashboardDraftResult),
        "interface.commit": _typed(InterfaceSnapshot),
        "interface.delete": _typed(InterfaceDeleteResult),
        "interface.list": _typed(InterfaceListResult),
        "interface.load": _typed(InterfaceSnapshot),
        "contentProfile.commit": _typed(ContentProfileSnapshot),
        "contentProfile.delete": _typed(ContentProfileDeleteResult),
        "contentProfile.load": _typed(ContentProfileSnapshot),
        "lookup.list": _typed(LookupListResult),
        "lookup.query": _typed(LookupQueryResult),
        "lookup.valuePage": _typed(LookupCellValue),
        "mutation.apply": _manual("MutationReceipt", mutation_receipt, receipt_schema),
        "mutation.preview": _manual(
            "MutationPreviewResult",
            {"Definition": table, "Operations": []},
            {
                "type": "object",
                "additionalProperties": False,
                "required": ["Definition", "Operations"],
                "properties": {
                    "Definition": table_schema,
                    "Operations": {"type": "array", "items": {"type": "object"}},
                },
            },
        ),
        "path.registerExportTarget": _typed(SessionPathGrant),
        "path.registerImportSource": _typed(SessionPathGrant),
        "path.requestExportTarget": _typed(SessionPathGrant),
        "path.requestImportSource": _typed(SessionPathGrant),
        "path.resolveGrant": _typed(SessionPathGrant),
        "plugin.cancelTask": _typed(PluginTaskSnapshot),
        "plugin.commitInstall": _typed(PluginSnapshot),
        "plugin.describeAction": _typed(ActionAvailability),
        "plugin.getTask": _typed(PluginTaskSnapshot),
        "plugin.inspectInstall": _typed(InstallPlan),
        "plugin.listAudit": _typed(list[PluginAuditEvent], "PluginAuditEventList"),
        "plugin.listCatalog": _typed(list[PluginSnapshot], "PluginSnapshotList"),
        "plugin.listPendingCleanup": _typed(
            list[PluginAuditEvent],
            "PluginAuditEventList",
        ),
        "plugin.resolveFile": _typed(bool, "Boolean"),
        "plugin.resolveInteraction": _typed(InteractionResolveResult),
        "plugin.rollback": _typed(PluginSnapshot),
        "plugin.setEnabled": _typed(PluginSnapshot),
        "plugin.startAction": _typed(PluginTaskSnapshot),
        "plugin.uninstall": _typed(UninstallResult),
        "plugin.upgrade": _typed(PluginSnapshot),
        "preset.delete": _manual("DeletePresetResult", delete_trace),
        "preset.list": _typed(PresetsResult),
        "preset.save": _typed(PresetEntry),
        "query.page": _manual(
            "QueryPageResult",
            query_page,
            TypeAdapter(QueryPageResult).json_schema(by_alias=True),
        ),
        "query.cursorOpen": _manual(
            "QueryCursorWindowResult",
            {
                "rows": [{"id": "row-1"}],
                "nextCursor": "opaque-cursor",
                "hasMore": True,
                "filteredRows": 2,
                "totalRows": 2,
                "querySnapshot": query_page["snapshot"],
            },
            TypeAdapter(QueryCursorWindowResult).json_schema(by_alias=True),
        ),
        "query.cursorFetch": _manual(
            "QueryCursorWindowResult",
            {
                "rows": [{"id": "row-2"}],
                "nextCursor": None,
                "hasMore": False,
                "filteredRows": 2,
                "totalRows": 2,
                "querySnapshot": query_page["snapshot"],
            },
            TypeAdapter(QueryCursorWindowResult).json_schema(by_alias=True),
        ),
        "query.view": _manual(
            "QueryViewResult",
            query_view,
            TypeAdapter(QueryViewResult).json_schema(by_alias=True),
        ),
        "query.readRows": _manual(
            "QueryRowsResult",
            {"rows": [{"id": "row-1", "name": "Ada"}]},
        ),
        "query.validateSnapshot": _manual(
            "QuerySnapshotValidationResult",
            {
                "valid": False,
                "reason": "application_write",
                "currentSchemaRevision": "schema_0001",
                "currentDataRevision": 2,
            },
        ),
        "recordDocumentLink.commit": _typed(RecordDocumentLinkSnapshot),
        "recordDocumentLink.delete": _typed(RecordDocumentLinkDeleteResult),
        "recordDocumentLink.list": _typed(RecordDocumentLinkListResult),
        "recordDocumentLink.repair": _typed(RecordDocumentLinkSnapshot),
        "relation.applyDelta": _typed(RelationDeltaResult),
        "relation.createTarget": _typed(RelationCreateTargetResult),
        "relation.previewDelta": _typed(RelationDeltaPreview),
        "relation.searchTargets": _typed(RelationSearchResult),
        "relation.updateSingle": _typed(RelationSingleUpdateResult),
        "schema.table.create": _manual(
            "SchemaTableCreateReceipt",
            {
                "contract": "vibetable.schema.v2",
                "operationId": "operation-create-table-12345678",
                "tableId": "tbl_1234567890abcdefghij",
                "displayName": "订单",
                "schemaRevision": "schema_0001",
            },
            {
                "type": "object",
                "additionalProperties": False,
                "required": ["contract", "operationId", "tableId", "displayName", "schemaRevision"],
                "properties": {
                    "contract": {"const": "vibetable.schema.v2"},
                    "operationId": {"type": "string", "minLength": 1},
                    "tableId": {"type": "string", "minLength": 1},
                    "displayName": {"type": "string", "minLength": 1},
                    "schemaRevision": {"type": "string", "minLength": 1},
                },
            },
        ),
        "schema.delete": _manual(
            "SchemaDeleteResult",
            {"deleted": True, "tableId": "orders"},
        ),
        "schema.describe": _typed(SchemaDescribeResult),
        "schema.getTable": _typed(SchemaSnapshotV2),
        "schema.list": _manual(
            "SchemaListResult",
            {"tables": [{"tableId": "orders", "displayName": "Orders", "kind": "base"}]},
            {
                "type": "object",
                "additionalProperties": False,
                "required": ["tables"],
                "properties": {
                    "tables": {
                        "type": "array",
                        "items": {
                            "type": "object",
                            "additionalProperties": False,
                            "required": ["tableId", "displayName", "kind"],
                            "properties": {
                                "tableId": {"type": "string", "minLength": 1},
                                "displayName": {"type": "string", "minLength": 1},
                                "kind": {"enum": ["base", "view"]},
                            },
                        },
                    },
                },
            },
        ),
        "settings.readDevice": _typed(DeviceSettings),
        "settings.readShared": _typed(SharedSettingsResult),
        "settings.saveDevice": _typed(DeviceSettings),
        "shortcut.delete": _manual(
            "DeleteShortcutResult",
            {"deleted": "shortcut-1"},
        ),
        "shortcut.launch": _typed(LaunchActionResult),
        "shortcut.list": _typed(ShortcutsResult),
        "shortcut.save": _typed(ShortcutEntry),
        "system.handshake": _typed(HandshakeResult),
        "table.applyPaste": _typed(ApplyPasteResult),
        "table.previewPaste": _typed(PastePlan),
        "task.cancel": _typed(TaskStatus),
        "task.create": _typed(TaskStatus),
        "task.status": _typed(TaskStatus),
        "version.compare": _typed(VersionCompareResult),
        "version.create": _typed(ContentVersionEntry),
        "version.delete": _manual("DeleteVersionResult", delete_trace),
        "version.list": _typed(VersionsResult),
        "version.promote": _manual(
            "PromoteVersionResult",
            {
                "promoted": "version-1",
                "restoredToRevision": "revision-1",
                "result": restore_result,
            },
        ),
        "version.save": _manual(
            "SaveVersionResult",
            {
                "saved": "version-1",
                "changeSetId": "change-set-1",
                "revisionId": "revision-1",
                "metadataRevision": "metadata-1",
                "revision": "revision-1",
                "emittedEvents": ["data.changed"],
            },
        ),
    }
    return specs


def _result_payload(spec: ResultSpec) -> tuple[object, dict[str, object]]:
    if spec.annotation is None:
        assert spec.schema is not None
        return spec.example, spec.schema
    adapter = TypeAdapter(spec.annotation)
    raw = _value(spec.annotation, "result")
    validated = adapter.validate_python(raw)
    payload = adapter.dump_python(validated, mode="json", by_alias=True)
    schema = typing.cast(
        dict[str, object],
        adapter.json_schema(by_alias=True, mode="serialization"),
    )
    return payload, typing.cast(dict[str, object], _sanitize_schema(schema))


def _event_cases(fixtures: Path, topics: list[str]) -> list[dict[str, object]]:
    typed = {
        "data.changed": json.loads(
            (fixtures / "data-changed-event.json").read_text(encoding="utf-8")
        ),
        "task.changed": json.loads(
            (fixtures / "task-changed-event.json").read_text(encoding="utf-8")
        ),
    }
    result: list[dict[str, object]] = []
    for topic in topics:
        event = typed.get(topic)
        if event is None:
            event = PluginEventEnvelope(
                event_type=topic,
                project_key="local:default",
                entity_id=f"{topic}-entity",
                revision=1,
                snapshot={"status": "ready"},
            ).model_dump(mode="json", by_alias=True)
        result.append({"topic": topic, "event": event})
    return result


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--check", action="store_true", help="fail if the catalog is stale")
    args = parser.parse_args()

    path = Path(__file__).with_name("fixtures") / "product-rpc-catalog.json"
    models = _registered_models()
    workspace_catalog_methods = {
        "field.change.apply",
        "field.change.cancel",
        "field.change.plan",
        "field.change.status",
        "field.recycleBin.list",
        "field.settings.describe",
    }
    methods = sorted(set(models) - workspace_catalog_methods)
    result_specs = _result_specs(path.parent)
    missing_results = sorted(set(methods) - result_specs.keys())
    stale_results = sorted(result_specs.keys() - set(methods))
    if missing_results or stale_results:
        raise RuntimeError(
            "RPC response model registry is not exhaustive: "
            f"missing={missing_results}, stale={stale_results}"
        )
    topics = [
        "data.changed",
        "plugin.catalog.changed",
        "plugin.file.requested",
        "plugin.interaction.requested",
        "plugin.task.changed",
        "task.changed",
    ]
    catalog: dict[str, object] = {
        "contractVersion": "2.0",
        "rpcMethods": methods,
        "eventTopics": topics,
        "requestEnvelope": {
            "jsonrpc": "2.0",
            "id": "request-1",
            "method": "schema.list",
            "params": {},
        },
        "successEnvelope": {
            "jsonrpc": "2.0",
            "id": "request-1",
            "result": {},
        },
        "errorEnvelope": {
            "jsonrpc": "2.0",
            "id": "request-1",
            "error": {
                "code": "request.invalid",
                "message": "Request validation failed.",
                "data": {"path": "params"},
            },
        },
        "rpcCases": [],
    }
    rpc_cases = typing.cast(list[dict[str, object]], catalog["rpcCases"])
    for index, method in enumerate(methods, start=1):
        request_id = f"rpc-{index:03d}"
        model = models[method]
        params = _model_payload(model)
        result_spec = result_specs[method]
        result, result_schema = _result_payload(result_spec)
        # Validate the serialized golden through the actual method DTO before
        # writing it. CI repeats this validation to prevent stale fixtures.
        model.model_validate(params)
        rpc_cases.append(
            {
                "method": method,
                "paramsModel": model.__name__,
                "resultModel": result_spec.model_name,
                "resultSchema": result_schema,
                "request": {
                    "jsonrpc": "2.0",
                    "id": request_id,
                    "method": method,
                    "params": params,
                },
                "success": {
                    "jsonrpc": "2.0",
                    "id": request_id,
                    "result": result,
                },
                "error": {
                    "jsonrpc": "2.0",
                    "id": request_id,
                    "error": {
                        "code": "request.invalid",
                        "message": f"{method} request validation failed.",
                        "data": {
                            "method": method,
                            "path": "params",
                            "retryable": False,
                        },
                    },
                },
            }
        )
    catalog["eventCases"] = _event_cases(path.parent, topics)
    rendered = json.dumps(catalog, ensure_ascii=False, indent=2) + "\n"
    if args.check:
        current = path.read_text(encoding="utf-8") if path.exists() else ""
        if current != rendered:
            raise SystemExit(f"{path.relative_to(REPO_ROOT)} is stale")
        return
    path.write_text(rendered, encoding="utf-8", newline="\n")


if __name__ == "__main__":
    main()
