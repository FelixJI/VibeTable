"""Query, schema, mutation, formula, and reconciliation product RPC module."""

from __future__ import annotations

from backend.adapters.pocketbase.client import (
    QueryCursorOpenCommand,
    QueryCursorWindowResult,
    SelectionProjectionResult,
)
from backend.adapters.pocketbase.product_rpc_support import (
    PocketBaseProductContext,
    ProductRpcHandler,
    _array,
    _integer,
    _lookup_revision,
    _object,
    _optional_string_list,
    _path_segment,
    _renderer_data_type,
    _result_object,
    _stable_hash,
    _text,
)
from backend.contracts.product_rpc import JsonObject, ProductParams
from backend.contracts.query import QuerySelectionProjectionResult
from backend.contracts.schema_v2 import (
    FormulaPreviewRequestV2,
    FormulaValidateRequestV2,
    SchemaSnapshotV2,
)


class ProductQuerySchemaRpc:
    """Owns query/schema request interpretation and sidecar projections."""

    def __init__(self, context: PocketBaseProductContext) -> None:
        self._context = context
        self._handlers: dict[str, ProductRpcHandler] = {
            "field.settings.describe": self._describe_field_settings,
            "field.change.plan": self._plan_field_change,
            "field.change.apply": self._apply_field_change,
            "field.change.status": self._field_change_status,
            "field.change.cancel": self._cancel_field_change,
            "field.recycleBin.list": self._list_recycled_fields,
            "schema.table.create": self._create_schema_table,
            "schema.delete": self._delete_schema,
            "schema.list": self._list_tables,
            "schema.getTable": self._get_table_schema,
            "schema.describe": self._describe_schema,
            "query.page": self._query_page,
            "query.cursorOpen": self._open_query_cursor,
            "query.selectionOpen": self._open_selection_projection,
            "query.cursorFetch": self._fetch_query_cursor,
            "query.view": self._query_view,
            "query.readRows": self._read_rows,
            "query.validateSnapshot": self._validate_snapshot,
            "mutation.preview": self._preview_mutation,
            "mutation.apply": self._apply_mutation,
            "formula.validate": self._validate_formula,
            "formula.draft.validate": self._validate_formula_draft,
            "formula.preview": self._preview_formula,
            "events.reconcile": self._reconcile,
        }
        self.methods = frozenset(self._handlers)

    async def invoke(self, method: str, params: ProductParams) -> JsonObject:
        try:
            handler = self._handlers[method]
        except KeyError as exc:
            raise ValueError(f"unknown query/schema RPC method: {method}") from exc
        return await handler(params)

    async def _create_schema_table(self, params: ProductParams) -> JsonObject:
        return await self._context.post("/api/vibetable/v2/schema/tables", params.root)

    async def _delete_schema(self, params: ProductParams) -> JsonObject:
        return await self._context.post("/api/vibetable/v1/schema/delete", params.root)

    async def _describe_field_settings(self, params: ProductParams) -> JsonObject:
        table_id = _path_segment(_text(params.root, "tableId"))
        query: dict[str, str] = {}
        if "fieldId" in params.root:
            query["fieldId"] = _text(params.root, "fieldId")
        return _result_object(
            await self._context.transport.request(
                "GET",
                f"/api/vibetable/v2/field-settings/{table_id}",
                query=query,
                headers=dict(self._context.headers),
                expected_status=(200,),
            )
        )

    async def _plan_field_change(self, params: ProductParams) -> JsonObject:
        return await self._context.post("/api/vibetable/v2/field-change/plan", params.root)

    async def _apply_field_change(self, params: ProductParams) -> JsonObject:
        return await self._context.post("/api/vibetable/v2/field-change/apply", params.root)

    async def _field_change_status(self, params: ProductParams) -> JsonObject:
        job_id = _path_segment(_text(params.root, "jobId"))
        return _result_object(
            await self._context.transport.request(
                "GET",
                f"/api/vibetable/v2/field-change/status/{job_id}",
                headers=dict(self._context.headers),
                expected_status=(200,),
            )
        )

    async def _cancel_field_change(self, params: ProductParams) -> JsonObject:
        job_id = _path_segment(_text(params.root, "jobId"))
        return await self._context.post(
            f"/api/vibetable/v2/field-change/cancel/{job_id}",
            {},
        )

    async def _list_recycled_fields(self, params: ProductParams) -> JsonObject:
        table_id = _path_segment(_text(params.root, "tableId"))
        return _result_object(
            await self._context.transport.request(
                "GET",
                f"/api/vibetable/v2/field-recycle-bin/{table_id}",
                headers=dict(self._context.headers),
                expected_status=(200,),
            )
        )

    async def _list_tables(self, params: ProductParams) -> JsonObject:
        if params.root:
            raise ValueError("schema.list does not accept parameters")
        return _result_object(
            await self._context.transport.request(
                "GET",
                "/api/vibetable/v2/schema/tables",
                headers=dict(self._context.headers),
                expected_status=(200,),
            )
        )

    async def _query_page(self, params: ProductParams) -> JsonObject:
        page = await self._context.client.query_page(
            table_id=_text(params.root, "tableId"),
            query=_object(params.root, "query"),
        )
        return _result_object(
            {
                "rows": page.rows,
                "offset": page.offset,
                "limit": page.limit,
                "filteredRows": page.filtered_rows,
                "totalRows": page.total_rows,
                "snapshot": page.snapshot,
            }
        )

    async def _open_query_cursor(self, params: ProductParams) -> JsonObject:
        window = await self._context.client.open_query_cursor(
            QueryCursorOpenCommand(
                table_id=_text(params.root, "tableId"),
                query=_object(params.root, "query"),
            )
        )
        return _cursor_window_result(window)

    async def _open_selection_projection(self, params: ProductParams) -> JsonObject:
        projection = await self._context.client.open_selection_projection(
            QueryCursorOpenCommand(
                table_id=_text(params.root, "tableId"),
                query=_object(params.root, "query"),
            )
        )
        return _selection_projection_result(projection)

    async def _fetch_query_cursor(self, params: ProductParams) -> JsonObject:
        window = await self._context.client.fetch_query_cursor(
            cursor=_text(params.root, "cursor"),
        )
        return _cursor_window_result(window)

    async def _query_view(self, params: ProductParams) -> JsonObject:
        result = await self._context.client.execute_view(
            table_id=_text(params.root, "tableId"),
            view=_object(params.root, "view"),
        )
        page = result.page
        return _result_object(
            {
                "page": {
                    "rows": page.rows,
                    "offset": page.offset,
                    "limit": page.limit,
                    "filteredRows": page.filtered_rows,
                    "totalRows": page.total_rows,
                    "snapshot": page.snapshot,
                },
                "groupRows": result.group_rows,
                "groupOffset": result.group_offset,
                "groupLimit": result.group_limit,
                "hasMoreGroups": result.has_more_groups,
            }
        )

    async def _read_rows(self, params: ProductParams) -> JsonObject:
        raw = params.root
        row_ids = _array(raw, "rowIds")
        if not all(isinstance(item, str) and item for item in row_ids):
            raise ValueError("rowIds must contain non-empty strings")
        return _result_object(
            {
                "rows": await self._context.client.read_rows(
                    table_id=_text(raw, "tableId"),
                    row_ids=[item for item in row_ids if isinstance(item, str)],
                )
            }
        )

    async def _validate_snapshot(self, params: ProductParams) -> JsonObject:
        raw = params.root
        body: JsonObject = {"snapshot": _object(raw, "snapshot")}
        if "currentQuery" in raw:
            body["currentQuery"] = _object(raw, "currentQuery")
        return await self._context.post("/api/vibetable/v1/query/validate-snapshot", body)

    async def _preview_mutation(self, params: ProductParams) -> JsonObject:
        return await self._context.client.preview_mutation(params.root)

    async def _apply_mutation(self, params: ProductParams) -> JsonObject:
        return await self._context.client.apply_mutation(params.root)

    async def _validate_formula(self, params: ProductParams) -> JsonObject:
        FormulaValidateRequestV2.model_validate(params.root)
        return await self._context.post("/api/vibetable/v1/formulas/validate", params.root)

    async def _validate_formula_draft(self, params: ProductParams) -> JsonObject:
        return await self._context.post("/api/vibetable/v1/formulas/draft/validate", params.root)

    async def _preview_formula(self, params: ProductParams) -> JsonObject:
        FormulaPreviewRequestV2.model_validate(params.root)
        return await self._context.post("/api/vibetable/v1/formulas/preview", params.root)

    async def _reconcile(self, params: ProductParams) -> JsonObject:
        return await self._context.client.reconcile_realtime(
            table_id=_text(params.root, "tableId"),
            schema_revision=_text(params.root, "schemaRevision"),
            data_revision=_text(params.root, "dataRevision"),
        )

    async def _get_table_schema(self, params: ProductParams) -> JsonObject:
        table_id = _text(params.root, "tableId")
        snapshot = SchemaSnapshotV2.model_validate(
            _result_object(
                await self._context.transport.request(
                    "GET",
                    f"/api/vibetable/v2/schema/tables/{_path_segment(table_id)}",
                    headers=dict(self._context.headers),
                    expected_status=(200,),
                )
            )
        )
        return snapshot.model_dump(mode="json", by_alias=True)

    async def _describe_schema(self, params: ProductParams) -> JsonObject:
        raw = params.root
        table_id = _path_segment(_text(raw, "collection"))
        request_generation = _integer(raw, "requestGeneration")
        accepts = _array(raw, "accepts")
        if accepts != [
            "vibetable.relation-capabilities.v1",
            "vibetable.lookup-query.v1",
        ]:
            raise ValueError("schema.describe accepts is invalid")
        definition = await self._context.client.describe_table(table_id)
        catalog = await self._context.client.describe_relations(table_id)
        relations = catalog.get("relations")
        lookups = catalog.get("lookups")
        if not isinstance(relations, list) or not isinstance(lookups, list):
            raise ValueError("PocketBase returned an invalid relation catalog")
        lookup_max_depth = catalog.get("lookupMaxDepth")
        if not isinstance(lookup_max_depth, int) or not 1 <= lookup_max_depth <= 32:
            raise ValueError("PocketBase returned an invalid lookup path capability")
        schema_revision = _text(definition, "schemaRevision")
        lookup_revision = _lookup_revision(schema_revision, lookups)
        return _result_object(
            {
                "contract": "vibetable.schema-describe.v1",
                "collection": table_id,
                "requestGeneration": request_generation,
                "schema": {
                    "collection": table_id,
                    "primaryKey": "id",
                    "primaryDisplayFieldId": _primary_display_field_id(definition),
                    "columns": _renderer_columns(definition),
                    "normalizedRelations": [
                        _renderer_relation(item, definition)
                        for item in relations
                        if isinstance(item, dict)
                    ],
                    "schemaRevision": schema_revision,
                    "permissionRevision": schema_revision,
                    "capabilityHash": _stable_hash(definition),
                    "lookupRevision": lookup_revision,
                },
                "capabilities": {
                    "contract": "vibetable.relation-capabilities.v1",
                    "relationReadV1": True,
                    "relationEditV1": True,
                    "lookupQueryV1": True,
                    "lookupMaxDepth": lookup_max_depth,
                    "reason": None,
                },
            }
        )


def _cursor_window_result(window: QueryCursorWindowResult) -> JsonObject:
    return _result_object(
        {
            "rows": window.rows,
            "nextCursor": window.next_cursor,
            "hasMore": window.has_more,
            "filteredRows": window.filtered_rows,
            "totalRows": window.total_rows,
            "querySnapshot": window.snapshot,
        }
    )


def _selection_projection_result(projection: SelectionProjectionResult) -> JsonObject:
    validated = QuerySelectionProjectionResult.model_validate(
        {
            "schemaSnapshot": projection.schema_snapshot,
            "cursorWindow": _cursor_window_result(projection.cursor_window),
        }
    )
    return _result_object(validated.model_dump(mode="json", by_alias=True))


def _renderer_relation(value: JsonObject, definition: JsonObject) -> JsonObject:
    cardinality = _text(value, "cardinality")
    kind = "m2o" if cardinality == "one" else "m2m"
    source_field_id = _text(value, "sourceFieldId")
    fields = definition.get("fields")
    if not isinstance(fields, list):
        raise ValueError("PocketBase returned an invalid field catalog")
    source_field = next(
        (
            field
            for field in fields
            if isinstance(field, dict)
            and isinstance(identity := field.get("identity"), dict)
            and identity.get("fieldId") == source_field_id
        ),
        {},
    )
    source_physical_name = _schema_v2_identity_text(source_field, "physicalName")
    source_value = source_field.get("value")
    target_table = _text(value, "targetTableId")
    on_delete = {
        "set-null": "nullify",
        "setNull": "nullify",
        "nullify": "nullify",
        "restrict": "restrict",
        "cascade": "cascade",
    }.get(str(value.get("deletePolicy")), "restrict")
    return _result_object(
        {
            "relationId": _text(value, "relationId"),
            "fieldRef": source_physical_name,
            "sourceCollection": _text(value, "sourceTableId"),
            "kind": kind,
            "relatedCollection": target_table,
            "manyField": source_physical_name,
            "oneField": None,
            "unique": cardinality == "one",
            "nullable": not (
                isinstance(source_value, dict) and source_value.get("required") is True
            ),
            "onDelete": on_delete,
            "preset": "standard",
            "selfRelation": target_table == value.get("sourceTableId"),
            "managed": True,
            "pairId": value.get("pairId") if isinstance(value.get("pairId"), str) else "",
            "reciprocalFieldId": (
                value.get("reciprocalFieldId")
                if isinstance(value.get("reciprocalFieldId"), str)
                else ""
            ),
            "quickCreateEligible": value.get("quickCreateEligible") is True,
            "quickCreateReason": (
                value.get("quickCreateReason")
                if isinstance(value.get("quickCreateReason"), str)
                else ""
            ),
            "state": "valid",
            "displayTemplate": None,
            "diagnostics": [],
        }
    )


def _primary_display_field_id(definition: JsonObject) -> str:
    fields = definition.get("fields")
    if not isinstance(fields, list):
        raise ValueError("PocketBase returned an invalid field catalog")
    for field in fields:
        if (
            isinstance(field, dict)
            and not _schema_v2_field_readonly(definition, field)
            and field.get("logicalType") != "relation"
        ):
            return _schema_v2_identity_text(field, "fieldId")
    for field in fields:
        if isinstance(field, dict):
            return _schema_v2_identity_text(field, "fieldId")
    return ""


def _renderer_columns(definition: JsonObject) -> list[JsonObject]:
    table_id = _text(definition, "tableId")
    fields = definition.get("fields")
    if not isinstance(fields, list):
        raise ValueError("PocketBase returned an invalid field catalog")
    capabilities = definition.get("capabilities")
    if not isinstance(capabilities, list):
        raise ValueError("PocketBase returned an invalid capability catalog")
    operators_by_type = {
        item.get("logicalType"): item
        for item in capabilities
        if isinstance(item, dict) and isinstance(item.get("logicalType"), str)
    }
    columns: list[JsonObject] = [
        _result_object(
            {
                "name": "id",
                "title": "ID",
                "fieldId": "id",
                "kind": "system",
                "relationId": None,
                "lookupId": None,
                "dataType": "text",
                "editable": False,
                "nullable": False,
                "scale": None,
                "precision": None,
                "attachmentPolicy": None,
            }
        )
    ]
    for field in fields:
        if not isinstance(field, dict):
            raise ValueError("PocketBase returned an invalid field catalog")
        field_id = _schema_v2_identity_text(field, "fieldId")
        kind = _schema_v2_field_kind(field)
        product_data_type = _text(field, "logicalType")
        if product_data_type == "formula":
            formula = field.get("formula")
            if not isinstance(formula, dict):
                raise ValueError("Formula field omitted its formula definition")
            product_data_type = _text(formula, "resultType")
            if product_data_type == "number":
                storage = field.get("storage")
                options = storage.get("options") if isinstance(storage, dict) else None
                if isinstance(options, dict) and options.get("onlyInt") is True:
                    product_data_type = "integer"
        elif product_data_type == "lookup":
            product_data_type = "json"
        data_type = _renderer_data_type(product_data_type)
        precision, scale = _precision_scale(field)
        capability = operators_by_type.get(field.get("logicalType"))
        value = field.get("value")
        nullable = not (isinstance(value, dict) and value.get("required") is True)
        file_policy = field.get("file")
        columns.append(
            _result_object(
                {
                    "name": _schema_v2_identity_text(field, "physicalName"),
                    "title": _text(field, "displayName"),
                    "fieldId": field_id,
                    "kind": kind,
                    "relationId": f"{table_id}.{field_id}" if kind == "relation" else None,
                    "lookupId": f"{table_id}.{field_id}" if kind == "lookup" else None,
                    "dataType": data_type,
                    "editable": not _schema_v2_field_readonly(definition, field),
                    "nullable": nullable,
                    "scale": scale,
                    "precision": precision,
                    "attachmentPolicy": (
                        {
                            "maxFiles": file_policy.get("maxFiles"),
                            "maxBytesPerFile": file_policy.get("maxBytesPerFile"),
                            "allowedMimeTypes": file_policy.get("allowedMimeTypes"),
                            "thumbnailVariants": file_policy.get("thumbs"),
                            "protected": file_policy.get("protected"),
                        }
                        if kind == "attachment" and isinstance(file_policy, dict)
                        else None
                    ),
                    "filterOperators": _optional_string_list(
                        capability.get("filterOperators") if isinstance(capability, dict) else None
                    ),
                    "groupable": isinstance(capability, dict)
                    and capability.get("groupable") is True,
                    "summaryOperations": _optional_string_list(
                        capability.get("summaryOperations")
                        if isinstance(capability, dict)
                        else None
                    ),
                }
            )
        )
    return columns


def _precision_scale(field: JsonObject) -> tuple[int | None, int | None]:
    display = field.get("display")
    if not isinstance(display, dict):
        raise ValueError("PocketBase returned invalid field display settings")
    scale = display.get("displayScale")
    if isinstance(scale, bool) or not isinstance(scale, int):
        raise ValueError("PocketBase returned invalid display scale")
    return None, scale


def _schema_v2_identity_text(field: JsonObject, name: str) -> str:
    identity = field.get("identity")
    if not isinstance(identity, dict):
        raise ValueError("PocketBase returned invalid field identity")
    return _text(identity, name)


def _schema_v2_field_kind(field: JsonObject) -> str:
    return {
        "autoDate": "system",
        "relation": "relation",
        "file": "attachment",
        "formula": "formula",
        "lookup": "lookup",
    }.get(_text(field, "logicalType"), "scalar")


def _schema_v2_field_readonly(definition: JsonObject, field: JsonObject) -> bool:
    lifecycle = field.get("lifecycle")
    return (
        definition.get("kind") == "view"
        or field.get("logicalType") in {"autoDate", "formula", "lookup"}
        or not isinstance(lifecycle, dict)
        or lifecycle.get("state") != "active"
    )


__all__ = ["ProductQuerySchemaRpc"]
