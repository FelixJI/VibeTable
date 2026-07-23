"""Live-schema adapters for relation-aware imports and authoritative exports."""

from __future__ import annotations

from collections.abc import Awaitable, Callable, Mapping, Sequence
from typing import Any, Protocol
from urllib.parse import quote

from backend.adapters.directus.profile import CollectionProfile
from backend.application.export_service import (
    AuthoritativeLookupColumn,
    AuthoritativeLookupExportPage,
)
from backend.application.import_service import (
    RelationImportBatchResult,
    RelationImportTarget,
)
from backend.application.lookup_service import LookupService
from backend.contracts.data_io import ImportPlanRow
from backend.contracts.lookup import (
    LookupCollectionParams,
    LookupQuery,
    LookupQueryParams,
)
from backend.contracts.relation_admin import (
    NormalizedRelationDescriptor,
    SchemaSnapshot,
)


class RelationIoError(Exception):
    def __init__(self, message: str, *, code: str) -> None:
        super().__init__(message)
        self.code = code


class _RelationImportClient(Protocol):
    async def schema_fields(self) -> list[dict[str, Any]]: ...

    async def relation_lookup_capabilities(self) -> dict[str, Any]: ...


class _RelationImportTransport(Protocol):
    async def request(
        self,
        method: str,
        path: str,
        *,
        query: Mapping[str, Any] | None = ...,
        json_body: Any | None = ...,
        access_token: str | None = ...,
        headers: Mapping[str, str] | None = ...,
        expected_status: Sequence[int] = ...,
    ) -> Any: ...


class _TokenProvider(Protocol):
    async def access_token(self) -> str: ...


class DirectusRelationImportProvider:
    """Resolve explicit unique matches and delegate atomic apply to Directus."""

    def __init__(
        self,
        *,
        client: _RelationImportClient,
        transport: _RelationImportTransport,
        auth: _TokenProvider,
        resolve_relation: Callable[
            [str], Awaitable[tuple[SchemaSnapshot, NormalizedRelationDescriptor]]
        ],
    ) -> None:
        self._client = client
        self._transport = transport
        self._auth = auth
        self._resolve_relation = resolve_relation
        self._proofs: dict[tuple[str, str], tuple[RelationImportTarget, str]] = {}

    async def inspect_mapping(
        self,
        *,
        collection: str,
        target_field: str,
        relation_id: str,
        match_field: str,
    ) -> RelationImportTarget:
        snapshot, relation = await self._resolve_relation(relation_id)
        if snapshot.collection != collection or relation.source_collection != collection:
            raise RelationIoError(
                "relation does not belong to the import collection",
                code="relation_id_mismatch",
            )
        if relation.kind != "m2o" or relation.related_collection is None:
            raise RelationIoError(
                "relation import currently requires a single-valued M2O/O2O field",
                code="relation_import_kind_unsupported",
            )
        if relation.many_field != target_field:
            raise RelationIoError(
                "relation does not identify the mapped target field",
                code="relation_id_mismatch",
            )
        fields = await self._client.schema_fields()
        target_pk = _primary_key(fields, relation.related_collection)
        raw_match = next(
            (
                item
                for item in fields
                if item.get("collection") == relation.related_collection
                and item.get("field") == match_field
            ),
            None,
        )
        schema = raw_match.get("schema") if isinstance(raw_match, dict) else None
        if not isinstance(schema, dict) or not (
            schema.get("is_primary_key") is True or schema.get("is_unique") is True
        ):
            raise RelationIoError(
                "matchField must be a visible primary-key or unique field",
                code="relation_match_field_not_unique",
            )
        target = RelationImportTarget(
            relation_id=relation_id,
            target_field=target_field,
            target_collection=relation.related_collection,
            target_primary_key=target_pk,
            match_field=match_field,
        )
        self._proofs[(relation_id, match_field)] = (target, snapshot.schema_revision)
        return target

    async def find_exact(self, target: RelationImportTarget, value: Any) -> list[Any]:
        token = await self._auth.access_token()
        payload = await self._transport.request(
            "GET",
            f"/items/{quote(target.target_collection, safe='')}",
            access_token=token,
            query={
                "filter": {target.match_field: {"_eq": value}},
                "fields": [target.target_primary_key],
                "limit": 2,
            },
        )
        rows = payload.get("data") if isinstance(payload, dict) else None
        if not isinstance(rows, list) or not all(isinstance(row, dict) for row in rows):
            raise RelationIoError(
                "Directus returned an invalid relation match response",
                code="relation_lookup_failed",
            )
        return [
            row[target.target_primary_key]
            for row in rows
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
        capabilities = await self._client.relation_lookup_capabilities()
        if capabilities.get("relation_import_v1") is not True:
            raise RelationIoError(
                "Directus relation import extension is unavailable",
                code="relation_import_extension_missing",
            )
        compiled_rows: list[dict[str, Any]] = []
        proofs: dict[tuple[str, str], RelationImportTarget] = {}
        verified_relations: set[tuple[str, str]] = set()
        for row in rows:
            resolutions: list[dict[str, Any]] = []
            for resolution in row.relation_resolutions:
                stored = self._proofs.get((resolution.relation_id, resolution.match_field))
                if stored is None:
                    raise RelationIoError(
                        "relation import proof expired; preview again",
                        code="relation_import_proof_missing",
                    )
                target, schema_revision = stored
                proof_key = (target.relation_id, target.match_field)
                if proof_key not in verified_relations:
                    live_snapshot, live_relation = await self._resolve_relation(target.relation_id)
                    if (
                        live_snapshot.schema_revision != schema_revision
                        or live_snapshot.collection != collection
                        or live_relation.kind != "m2o"
                        or live_relation.source_collection != collection
                        or live_relation.related_collection != target.target_collection
                        or live_relation.many_field != target.target_field
                    ):
                        raise RelationIoError(
                            "relation import proof expired; preview again",
                            code="relation_import_proof_expired",
                        )
                    verified_relations.add(proof_key)
                proofs[(target.relation_id, target.match_field)] = target
                resolutions.append(
                    {
                        "targetField": target.target_field,
                        "relationId": resolution.relation_id,
                        "targetCollection": target.target_collection,
                        "targetPrimaryKey": target.target_primary_key,
                        "matchField": resolution.match_field,
                        "sourceValue": resolution.source_value,
                        "state": resolution.state,
                        "matchedPrimaryKey": resolution.matched_primary_key,
                    }
                )
            compiled_rows.append(
                {
                    "values": {
                        field: value
                        for field, value in row.values.items()
                        if field not in {item["targetField"] for item in resolutions}
                    },
                    "relations": resolutions,
                }
            )
        live_fields = await self._client.schema_fields()
        source_fields = {
            str(item.get("field"))
            for item in live_fields
            if item.get("collection") == collection and isinstance(item.get("field"), str)
        }
        source_unique = {
            str(item.get("field"))
            for item in live_fields
            if item.get("collection") == collection
            and isinstance(item.get("field"), str)
            and isinstance(item.get("schema"), dict)
            and (
                item["schema"].get("is_primary_key") is True
                or item["schema"].get("is_unique") is True
            )
        }
        if mode == "upsert" and (upsert_key is None or upsert_key not in source_unique):
            raise RelationIoError(
                "upsertKey must be a visible primary-key or unique field",
                code="import_upsert_key_not_unique",
            )
        proof_fields: dict[str, list[str]] = {
            collection: sorted(
                {
                    profile.primary_key,
                    *(field for row in compiled_rows for field in row["values"]),
                    *(target.target_field for target in proofs.values()),
                    *([upsert_key] if upsert_key else []),
                }
                & source_fields
            )
        }
        proof_unique: dict[str, list[str]] = {
            collection: sorted(source_unique & set(proof_fields[collection]))
        }
        for target in proofs.values():
            proof_fields.setdefault(target.target_collection, [])
            proof_fields[target.target_collection] = sorted(
                set(proof_fields[target.target_collection])
                | {target.target_primary_key, target.match_field}
            )
            proof_unique.setdefault(target.target_collection, [])
            proof_unique[target.target_collection] = sorted(
                set(proof_unique[target.target_collection]) | {target.match_field}
            )
        token = await self._auth.access_token()
        payload = await self._transport.request(
            "POST",
            "/vibetable-bulk-mutation/relation-import",
            access_token=token,
            headers={"Idempotency-Key": idempotency_key},
            json_body={
                "contract": "vibetable-relation-import.v1",
                "idempotencyKey": idempotency_key,
                "sourceCollection": collection,
                "sourcePrimaryKey": profile.primary_key,
                "mode": "upsert" if mode == "upsert" else "create",
                **({"upsertKey": upsert_key} if upsert_key else {}),
                "schemaProof": {
                    "collections": sorted(proof_fields),
                    "fields": proof_fields,
                    "uniqueFields": proof_unique,
                    "relationIds": sorted({target.relation_id for target in proofs.values()}),
                },
                "rows": compiled_rows,
            },
        )
        data = payload.get("data") if isinstance(payload, dict) else None
        if not isinstance(data, dict) or data.get("outcome") != "committed":
            raise RelationIoError(
                "relation import extension returned an invalid response",
                code="relation_import_extension_invalid",
            )
        return RelationImportBatchResult(
            created_row_keys=[str(item) for item in data.get("createdSourceRowKeys", [])],
            updated_row_keys=[str(item) for item in data.get("updatedSourceRowKeys", [])],
            request_id=str(data.get("requestId") or idempotency_key),
        )


class LookupExportProvider:
    """Adapt LookupService.query to the streaming export provider protocol."""

    def __init__(
        self,
        *,
        lookup_service: LookupService,
        schema_provider: Callable[[str], Awaitable[SchemaSnapshot]],
    ) -> None:
        self._lookup_service = lookup_service
        self._schema_provider = schema_provider

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
        snapshot = await self._schema_provider(collection)
        if snapshot.lookup_revision != lookup_revision:
            raise RelationIoError(
                "Lookup definitions changed before export",
                code="lookup_revision_mismatch",
            )
        normalized_query = LookupQuery.model_validate({**query, "offset": offset, "limit": limit})
        result = await self._lookup_service.query(
            LookupQueryParams(
                collection=collection,
                field_refs=[*fields, *lookup_ids],
                query=normalized_query,
                request_generation=offset // max(limit, 1),
                schema_revision=snapshot.schema_revision,
                permission_revision=snapshot.permission_revision,
                lookup_revision=lookup_revision,
            )
        )
        listed = await self._lookup_service.list(LookupCollectionParams(collection=collection))
        definitions = {item.lookup_id: item for item in listed.definitions}
        columns = [
            AuthoritativeLookupColumn(
                lookup_id=lookup_id,
                field_key=definitions[lookup_id].field_key,
            )
            for lookup_id in lookup_ids
            if lookup_id in definitions
        ]
        rows: list[dict[str, Any]] = []
        key_map = {column.lookup_id: column.field_key for column in columns}
        for raw in result.rows:
            row = dict(raw)
            for lookup_id, field_key in key_map.items():
                if lookup_id in row:
                    row[field_key] = row.pop(lookup_id)
            rows.append(row)
        return AuthoritativeLookupExportPage(
            rows=rows,
            columns=columns,
            filtered_rows=result.filtered_rows,
            lookup_revision=result.lookup_revision,
        )


def _primary_key(fields: list[dict[str, Any]], collection: str) -> str:
    matches = [
        item.get("field")
        for item in fields
        if item.get("collection") == collection
        and isinstance(item.get("schema"), dict)
        and item["schema"].get("is_primary_key") is True
    ]
    if len(matches) != 1 or not isinstance(matches[0], str):
        raise RelationIoError(
            "target collection has no unambiguous visible primary key",
            code="relation_target_schema_invalid",
        )
    return matches[0]


__all__ = ["DirectusRelationImportProvider", "LookupExportProvider", "RelationIoError"]
