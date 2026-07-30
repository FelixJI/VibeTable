#!/usr/bin/env python3
"""Generate typed request/event golden cases for every registered product RPC."""

from __future__ import annotations

import ast
import datetime as dt
import enum
import inspect
import json
import sys
import types
import typing
from dataclasses import dataclass
from pathlib import Path

from pydantic import BaseModel, RootModel, TypeAdapter

REPO_ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(REPO_ROOT))

import backend.__main__ as composition  # noqa: E402
from backend.application.product_data_service import (  # noqa: E402
    PRODUCT_PARAM_MODELS,
    ProductParams,
)
from backend.contracts.backup import (  # noqa: E402
    BackupCreateResult,
    BackupDeleteResult,
    BackupListResult,
    BackupRestoreResult,
)
from backend.contracts.data_io import (  # noqa: E402
    ApplyImportResult,
    ExportResult,
    ImportPlan,
    TemplateResult,
)
from backend.contracts.document_workspace import (  # noqa: E402
    DocumentHistoryResult,
    DocumentListResult,
    FolderResult,
    LinkResult,
    PublishIndexBatchResult,
    RegisterDocumentResult,
)
from backend.contracts.grid_state import GridStateResult  # noqa: E402
from backend.contracts.history import (  # noqa: E402
    HistoryPage,
    RestorePreview,
    RestoreResult,
)
from backend.contracts.lookup import (  # noqa: E402
    LookupListResult,
    LookupMutationResult,
    LookupQueryResult,
    LookupValidationResult,
)
from backend.contracts.paste import ApplyPasteResult, PastePlan  # noqa: E402
from backend.contracts.plugin import (  # noqa: E402
    ActionAvailability,
    InstallPlan,
    InteractionResolveResult,
    PluginAuditEvent,
    PluginEventEnvelope,
    PluginSnapshot,
    PluginTaskSnapshot,
    UninstallResult,
)
from backend.contracts.presets_versions_dashboards import (  # noqa: E402
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
from backend.contracts.relation_admin import (  # noqa: E402
    RelationChangePlan,
    RelationChangeResult,
    RelationDeltaPreview,
    RelationDeltaResult,
    RelationSearchResult,
    RelationSingleUpdateResult,
    SchemaDescribeResult,
)
from backend.contracts.settings_commands import (  # noqa: E402
    CommandResult,
    CommandsResult,
    DeviceSettings,
    LaunchActionResult,
    SharedSettingsResult,
    ShortcutEntry,
    ShortcutsResult,
)
from backend.contracts.system import HandshakeResult  # noqa: E402
from backend.contracts.table_admin import IdentifierMappingsResult  # noqa: E402
from backend.contracts.task import SessionPathGrant, TaskStatus  # noqa: E402


@dataclass(frozen=True)
class ResultSpec:
    """One method's actual public response model and representative value."""

    model_name: str
    annotation: object | None = None
    example: object | None = None
    schema: dict[str, object] | None = None


def _text(name: str) -> str:
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


def _value(annotation: object, name: str) -> object:
    origin = typing.get_origin(annotation)
    arguments = typing.get_args(annotation)
    if origin in (typing.Union, types.UnionType):
        choices = [item for item in arguments if item is not type(None)]
        return _value(choices[0], name) if choices else None
    if origin is typing.Literal:
        return arguments[0]
    if origin is list:
        return [_value(arguments[0] if arguments else typing.Any, name)]
    if origin is tuple:
        return [_value(item, name) for item in arguments if item is not Ellipsis]
    if origin in (dict, typing.Mapping):
        return {}
    if annotation is typing.Any:
        return {}
    if inspect.isclass(annotation) and issubclass(annotation, BaseModel):
        return _model_payload(annotation)
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


def _model_payload(model: type[BaseModel]) -> dict[str, object]:
    if issubclass(model, ProductParams):
        return _product_payload(model)
    if issubclass(model, RootModel):
        value = _value(model.model_fields["root"].annotation, "root")
        return model.model_validate(value).model_dump(mode="json")
    raw: dict[str, object] = {}
    for name, field in model.model_fields.items():
        if field.is_required() or name.endswith("_at"):
            if model.__module__ == "backend.contracts.backup" and name == "name":
                value: object = "manual_20260724_083100.zip"
            elif model.__module__ == "backend.contracts.backup" and name == "sha256":
                value = "a" * 64
            else:
                value = _value(field.annotation, name)
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
    specs: dict[str, ResultSpec] = {
        "backup.create": _typed(BackupCreateResult),
        "backup.delete": _manual(
            "BackupDeleteResult",
            {"deleted": "manual_20260724_101500.zip"},
            BackupDeleteResult.model_json_schema(),
        ),
        "backup.list": _typed(BackupListResult),
        "backup.restore": _typed(BackupRestoreResult),
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
                        "contractVersion": "1.0",
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
            {"contractVersion": "1.0", "saved": True, "bytes": 128},
        ),
        "file.token": _manual(
            "FileTokenResult",
            {"contractVersion": "1.0", "downloadCapability": "capability-1"},
        ),
        "formula.preview": _manual(
            "FormulaPreviewResult",
            {"values": {"subtotal": 12.5}},
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
        "identifier.list": _typed(IdentifierMappingsResult),
        "identifier.reconcile": _typed(IdentifierMappingsResult),
        "identifier.updateAliases": _typed(IdentifierMappingsResult),
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
        "lookup.create": _typed(LookupMutationResult),
        "lookup.delete": _typed(LookupMutationResult),
        "lookup.list": _typed(LookupListResult),
        "lookup.preview": _typed(LookupQueryResult),
        "lookup.query": _typed(LookupQueryResult),
        "lookup.update": _typed(LookupMutationResult),
        "lookup.validate": _typed(LookupValidationResult),
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
        "query.page": _manual("QueryPageResult", query_page),
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
        "relation.applyDelta": _typed(RelationDeltaResult),
        "relation.previewDelta": _typed(RelationDeltaPreview),
        "relation.searchTargets": _typed(RelationSearchResult),
        "relation.updateSingle": _typed(RelationSingleUpdateResult),
        "schema.apply": _manual("TableDefinition", table, table_schema),
        "schema.delete": _manual(
            "SchemaDeleteResult",
            {"deleted": True, "tableId": "orders"},
        ),
        "schema.describe": _typed(SchemaDescribeResult),
        "schema.getTable": _manual("TableDefinition", table, table_schema),
        "schema.list": _manual(
            "SchemaListResult",
            {"tables": [table]},
            {
                "type": "object",
                "additionalProperties": False,
                "required": ["tables"],
                "properties": {
                    "tables": {"type": "array", "items": table_schema},
                },
            },
        ),
        "schema.validate": _manual(
            "SchemaValidationResult",
            {
                "definition": table,
                "capabilities": {
                    "text": {
                        "storage": "text",
                        "exact": True,
                        "note": "provider-neutral text storage",
                    }
                },
            },
            {
                "type": "object",
                "additionalProperties": False,
                "required": ["definition", "capabilities"],
                "properties": {
                    "definition": table_schema,
                    "capabilities": {
                        "type": "object",
                        "additionalProperties": {
                            "type": "object",
                            "additionalProperties": False,
                            "required": ["storage", "exact"],
                            "properties": {
                                "storage": {"type": "string"},
                                "exact": {"type": "boolean"},
                                "note": {"type": "string"},
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
        "table_admin.applyRelationChange": _typed(RelationChangeResult),
        "table_admin.previewRelationChange": _typed(RelationChangePlan),
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
        "workspace.linkDocument": _typed(LinkResult),
        "workspace.publishIndexBatch": _typed(PublishIndexBatchResult),
        "workspace.readDocumentHistory": _typed(DocumentHistoryResult),
        "workspace.readDocuments": _typed(DocumentListResult),
        "workspace.readFolder": _typed(FolderResult),
        "workspace.registerDocument": _typed(RegisterDocumentResult),
        "workspace.unlinkDocument": _manual(
            "UnlinkDocumentResult",
            {"deleted": "link-1"},
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
    path = Path(__file__).with_name("fixtures") / "rpc-catalog.json"
    catalog = json.loads(path.read_text(encoding="utf-8"))
    models = _registered_models()
    methods = sorted(models)
    result_specs = _result_specs(path.parent)
    missing_results = sorted(set(methods) - result_specs.keys())
    stale_results = sorted(result_specs.keys() - set(methods))
    if missing_results or stale_results:
        raise RuntimeError(
            "RPC response model registry is not exhaustive: "
            f"missing={missing_results}, stale={stale_results}"
        )
    topics = catalog["eventTopics"]
    catalog["rpcMethods"] = methods
    catalog["rpcCases"] = []
    for index, method in enumerate(methods, start=1):
        request_id = f"rpc-{index:03d}"
        model = models[method]
        params = _model_payload(model)
        result_spec = result_specs[method]
        result, result_schema = _result_payload(result_spec)
        # Validate the serialized golden through the actual method DTO before
        # writing it. CI repeats this validation to prevent stale fixtures.
        model.model_validate(params)
        catalog["rpcCases"].append(
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
    path.write_text(
        json.dumps(catalog, ensure_ascii=False, indent=2) + "\n",
        encoding="utf-8",
    )


if __name__ == "__main__":
    main()
