"""Product adapters for relation-aware imports and Lookup exports."""

from __future__ import annotations

from collections.abc import Mapping
from typing import Any, Literal, Protocol

from backend.adapters.pocketbase.client import QueryPageResult
from backend.application.export_service import (
    AuthoritativeLookupColumn,
    AuthoritativeLookupExportPage,
)
from backend.application.import_service import (
    RelationImportBatchResult,
    RelationImportTarget,
)
from backend.application.paste_service import PasteMutationPort
from backend.contracts.data_io import ImportPlanRow
from backend.contracts.data_profile import CollectionProfile
from backend.contracts.paste import PastePlanRow


class RelationIoError(Exception):
    def __init__(self, message: str, *, code: str) -> None:
        super().__init__(message)
        self.code = code


class _RelationClient(Protocol):
    async def describe_relations(self, table_id: str) -> dict[str, Any]: ...

    async def describe_table(self, table_id: str) -> dict[str, Any]: ...

    async def query_page(
        self,
        *,
        table_id: str,
        query: dict[str, Any],
    ) -> QueryPageResult: ...


class PocketBaseRelationImportProvider:
    """Resolve exact relation matches through normalized product APIs."""

    def __init__(
        self,
        *,
        client: _RelationClient,
        bulk: PasteMutationPort,
    ) -> None:
        self._client = client
        self._bulk = bulk

    async def inspect_mapping(
        self,
        *,
        collection: str,
        target_field: str,
        relation_id: str,
        match_field: str,
    ) -> RelationImportTarget:
        catalog = await self._client.describe_relations(collection)
        raw_relations = catalog.get("relations")
        if not isinstance(raw_relations, list):
            raise RelationIoError(
                "relation catalog is invalid",
                code="relation_target_schema_invalid",
            )
        relation = next(
            (
                raw
                for raw in raw_relations
                if isinstance(raw, dict) and raw.get("relationId") == relation_id
            ),
            None,
        )
        if (
            relation is None
            or relation.get("sourceTableId") != collection
            or relation.get("physicalName") != target_field
        ):
            raise RelationIoError(
                "relation does not identify the mapped target field",
                code="relation_id_mismatch",
            )
        if relation.get("cardinality") not in {"one", "m2o", "o2o"}:
            raise RelationIoError(
                "relation import requires a single-valued relation",
                code="relation_import_kind_unsupported",
            )
        target_table = relation.get("targetTableId")
        if not isinstance(target_table, str) or not target_table:
            raise RelationIoError(
                "relation target table is invalid",
                code="relation_target_schema_invalid",
            )
        schema = await self._client.describe_table(target_table)
        raw_fields = schema.get("fields")
        if not isinstance(raw_fields, list):
            raise RelationIoError(
                "relation target schema is invalid",
                code="relation_target_schema_invalid",
            )
        field = next(
            (
                raw
                for raw in raw_fields
                if isinstance(raw, dict)
                and match_field in {raw.get("fieldId"), raw.get("physicalName")}
            ),
            None,
        )
        constraints = field.get("constraints") if isinstance(field, dict) else None
        unique = isinstance(constraints, list) and any(
            isinstance(item, dict) and item.get("kind") == "unique" and item.get("value") is True
            for item in constraints
        )
        physical_match = field.get("physicalName") if isinstance(field, dict) else None
        if not unique or not isinstance(physical_match, str):
            raise RelationIoError(
                "matchField must be a visible unique field",
                code="relation_match_field_not_unique",
            )
        return RelationImportTarget(
            relation_id=relation_id,
            target_field=target_field,
            target_collection=target_table,
            target_primary_key="id",
            match_field=physical_match,
        )

    async def find_exact(self, target: RelationImportTarget, value: Any) -> list[Any]:
        page = await self._client.query_page(
            table_id=target.target_collection,
            query={
                "filters": [
                    {
                        "field": target.match_field,
                        "operator": "eq",
                        "value": value,
                        "logic": "AND",
                    }
                ],
                "sorts": [],
                "offset": 0,
                "limit": 2,
            },
        )
        return [
            row[target.target_primary_key]
            for row in page.rows
            if row.get(target.target_primary_key) is not None
        ]

    async def apply_chunk(
        self,
        *,
        collection: str,
        profile: CollectionProfile,
        rows: list[ImportPlanRow],
        mode: str,
        upsert_key: str | None,
        idempotency_key: str,
    ) -> RelationImportBatchResult:
        planned: list[PastePlanRow] = []
        row_revisions: dict[str | int, str] = {}
        for row in rows:
            values = dict(row.values)
            for resolution in row.relation_resolutions:
                values[resolution.target_field] = resolution.matched_primary_key
            kind: Literal["update", "insert", "skip"] = "insert"
            target_row_key: str | int | None = None
            if mode == "upsert":
                if not upsert_key or values.get(upsert_key) is None:
                    raise RelationIoError(
                        "upsert key is missing from an import row",
                        code="import_upsert_key_missing",
                    )
                page = await self._client.query_page(
                    table_id=collection,
                    query={
                        "filters": [
                            {
                                "field": upsert_key,
                                "operator": "eq",
                                "value": values[upsert_key],
                                "logic": "AND",
                            }
                        ],
                        "sorts": [],
                        "offset": 0,
                        "limit": 2,
                    },
                )
                if len(page.rows) > 1:
                    raise RelationIoError(
                        "upsert key matched more than one row",
                        code="import_upsert_key_not_unique",
                    )
                if page.rows:
                    target_row_key = str(page.rows[0]["id"])
                    kind = "update"
                    guard = page.rows[0].get("__vibetableRevision")
                    if isinstance(guard, str) and guard:
                        row_revisions[target_row_key] = guard
            planned.append(
                PastePlanRow(
                    kind=kind,
                    target_row_key=target_row_key,
                    changes={
                        field: {"before": None, "after": value} for field, value in values.items()
                    },
                )
            )
        result = await self._bulk.apply(
            collection=collection,
            profile=profile,
            rows=planned,
            row_revisions=row_revisions,
            idempotency_key=idempotency_key,
            schema_revision=profile.schema_revision,
        )
        if result.outcome != "committed":
            raise RelationIoError(
                "relation-aware import did not commit",
                code=("import_pending" if result.outcome == "pending" else "import_conflict"),
            )
        return RelationImportBatchResult(
            created_row_keys=[str(key) for key in result.created_row_keys],
            updated_row_keys=[str(key) for key in result.updated_row_keys],
            request_id=result.request_id,
        )


class _LookupClient(Protocol):
    async def describe_lookups(self, table_id: str) -> dict[str, Any]: ...

    async def query_lookups(
        self,
        *,
        table_id: str,
        schema_revision: str,
        query: Mapping[str, Any],
    ) -> QueryPageResult: ...


class PocketBaseLookupExportProvider:
    """Authoritative Lookup export over product routes."""

    def __init__(self, *, client: _LookupClient) -> None:
        self._client = client

    async def query_page(
        self,
        *,
        collection: str,
        fields: list[str],
        lookup_ids: list[str],
        lookup_revision: str,
        query: dict[str, Any],
        offset: int,
        limit: int,
    ) -> AuthoritativeLookupExportPage:
        del fields
        catalog = await self._client.describe_lookups(collection)
        schema_revision = catalog.get("schemaRevision")
        raw_lookups = catalog.get("lookups")
        if schema_revision != lookup_revision:
            raise RelationIoError(
                "Lookup definitions changed before export",
                code="lookup_revision_mismatch",
            )
        if not isinstance(raw_lookups, list):
            raise RelationIoError(
                "Lookup catalog is invalid",
                code="lookup_export_columns_invalid",
            )
        by_id = {
            raw["lookupId"]: raw["physicalName"]
            for raw in raw_lookups
            if isinstance(raw, dict)
            and isinstance(raw.get("lookupId"), str)
            and isinstance(raw.get("physicalName"), str)
        }
        if any(lookup_id not in by_id for lookup_id in lookup_ids):
            raise RelationIoError(
                "Lookup catalog does not contain every requested Lookup",
                code="lookup_export_columns_mismatch",
            )
        columns = [
            AuthoritativeLookupColumn(lookup_id, by_id[lookup_id]) for lookup_id in lookup_ids
        ]
        if len({column.field_key for column in columns}) != len(columns):
            raise RelationIoError(
                "Lookup catalog contains duplicate export fields",
                code="lookup_export_columns_invalid",
            )
        page = await self._client.query_lookups(
            table_id=collection,
            schema_revision=str(schema_revision),
            query={**query, "offset": offset, "limit": limit},
        )
        rows: list[dict[str, Any]] = []
        for source_row in page.rows:
            row = dict(source_row)
            for column in columns:
                cell = row.get(column.field_key)
                if (
                    not isinstance(cell, dict)
                    or cell.get("state") != "ok"
                    or not isinstance(cell.get("provenance"), list)
                ):
                    raise RelationIoError(
                        "Lookup query returned an invalid or unavailable cell",
                        code="lookup_export_cell_invalid",
                    )
                row[column.field_key] = cell.get("value")
            rows.append(row)
        return AuthoritativeLookupExportPage(
            rows=rows,
            columns=columns,
            filtered_rows=page.filtered_rows,
            lookup_revision=str(schema_revision),
        )


__all__ = [
    "PocketBaseLookupExportProvider",
    "PocketBaseRelationImportProvider",
    "RelationIoError",
]
