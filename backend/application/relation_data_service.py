"""Permission-bound relation target search and in-grid mutation orchestration."""

from __future__ import annotations

import hashlib
import json
import re
from collections.abc import Awaitable, Callable
from typing import Any, Protocol
from urllib.parse import quote

from backend.adapters.directus.client import DirectusClient
from backend.adapters.directus.profile import CollectionProfile
from backend.adapters.directus.transport import DirectusTransport
from backend.contracts.relation_admin import (
    NormalizedRelationDescriptor,
    RelationDelta,
    RelationDeltaPreview,
    RelationDeltaResult,
    RelationDiagnostic,
    RelationSearchParams,
    RelationSearchResult,
    RelationSingleUpdateParams,
    RelationSingleUpdateResult,
    RelationTargetRef,
    SchemaSnapshot,
)

_TEMPLATE_FIELD = re.compile(r"{{\s*([A-Za-z_][A-Za-z0-9_]*)\s*}}")


class _Auth(Protocol):
    async def access_token(self) -> str: ...


RelationResolver = Callable[[str], Awaitable[tuple[SchemaSnapshot, NormalizedRelationDescriptor]]]


class RelationDataError(Exception):
    def __init__(self, message: str, *, code: str = "relation_error") -> None:
        super().__init__(message)
        self.code = code

    def rpc_error_data(self) -> dict[str, str]:
        return {"code": self.code, "message": str(self)}


class RelationDataService:
    def __init__(
        self,
        *,
        client: DirectusClient,
        auth: _Auth,
        transport: DirectusTransport,
        profiles: dict[str, CollectionProfile],
        resolve_relation: RelationResolver,
    ) -> None:
        self._client = client
        self._auth = auth
        self._transport = transport
        self._profiles = profiles
        self._resolve_relation = resolve_relation

    async def search_targets(self, params: RelationSearchParams) -> RelationSearchResult:
        _snapshot, relation = await self._usable(params.relation_id)
        collections = (
            relation.allowed_collections
            if relation.kind == "m2a"
            else [relation.related_collection]
            if relation.related_collection
            else []
        )
        if params.collection is not None:
            if params.collection not in collections:
                raise RelationDataError(
                    "target collection is outside the relation allow-list",
                    code="relation_target_invalid",
                )
            collections = [params.collection]
        if len(collections) != 1:
            raise RelationDataError(
                "M2A target search requires an explicit collection",
                code="relation_target_collection_required",
            )
        collection = collections[0]
        primary_key = await self._primary_key(collection)
        template_fields = sorted(set(_TEMPLATE_FIELD.findall(relation.display_template or "")))
        fields = [primary_key, *template_fields]
        query: dict[str, Any] = {
            "fields": fields,
            "offset": params.offset,
            "limit": params.limit,
            "meta": "filter_count",
            "sort": [primary_key],
        }
        if params.query:
            if template_fields:
                query["filter"] = {
                    "_or": [{field: {"_icontains": params.query}} for field in template_fields]
                }
            else:
                # No display field guessing: with no explicit template only an
                # exact primary-key lookup is permitted.
                query["filter"] = {primary_key: {"_eq": params.query}}
        token = await self._auth.access_token()
        payload = await self._transport.request(
            "GET",
            f"/items/{quote(collection, safe='')}",
            access_token=token,
            query=query,
        )
        rows, meta = _list_payload(payload)
        return RelationSearchResult(
            items=[
                RelationTargetRef(
                    collection=collection,
                    item_id=str(row[primary_key]),
                    label=_render_label(
                        relation.display_template, row, fallback=str(row[primary_key])
                    ),
                )
                for row in rows
                if row.get(primary_key) is not None
            ],
            total=_nonnegative_int(meta.get("filter_count"), default=len(rows)),
        )

    async def update_single(self, params: RelationSingleUpdateParams) -> RelationSingleUpdateResult:
        snapshot, relation = await self._usable(params.relation_id)
        if relation.kind != "m2o" or relation.many_field is None:
            raise RelationDataError("relation is not single-valued", code="relation_kind_invalid")
        if snapshot.schema_revision != params.expected_schema_revision:
            raise RelationDataError("schema changed", code="schema_mismatch")
        if params.target is None:
            if not relation.nullable:
                raise RelationDataError(
                    "required relation cannot be cleared", code="relation_required"
                )
            value: Any = None
        else:
            if params.target.collection != relation.related_collection:
                raise RelationDataError(
                    "target collection is outside the relation allow-list",
                    code="relation_target_invalid",
                )
            value = params.target.item_id
        profile = self._profile(relation.source_collection)
        await self._client.update_item(
            profile,
            params.source_item_id,
            {relation.many_field: value},
            expected_date_updated=params.expected_date_updated,
            request_id=params.idempotency_key,
        )
        return RelationSingleUpdateResult(
            outcome="committed",
            current=params.target,
            schema_revision=snapshot.schema_revision,
            request_id=params.idempotency_key,
        )

    async def preview_delta(self, params: RelationDelta) -> RelationDeltaPreview:
        snapshot, relation = await self._usable(params.relation_id)
        diagnostics = self._validate_delta(params, relation, snapshot)
        current = await self._read_current(relation, params.source_item_id)
        return RelationDeltaPreview(
            delta=params,
            relation_id=params.relation_id,
            source_item_id=params.source_item_id,
            adds=len(params.adds),
            updates=len(params.updates),
            removes=len(params.removes),
            current=current,
            can_apply=not diagnostics,
            schema_revision=snapshot.schema_revision,
            diagnostics=diagnostics,
        )

    async def apply_delta(self, params: RelationDelta) -> RelationDeltaResult:
        snapshot, relation = await self._usable(params.relation_id)
        diagnostics = self._validate_delta(params, relation, snapshot)
        if diagnostics:
            raise RelationDataError(diagnostics[0].message, code=diagnostics[0].code)
        body = await self._compile_delta(params, relation)
        token = await self._auth.access_token()
        payload = await self._transport.request(
            "POST",
            "/vibetable-bulk-mutation/relation-delta",
            access_token=token,
            headers={"Idempotency-Key": params.idempotency_key},
            json_body=body,
        )
        data = payload.get("data") if isinstance(payload, dict) else None
        if not isinstance(data, dict) or data.get("outcome") != "committed":
            raise RelationDataError(
                "relation mutation extension returned an invalid response",
                code="relation_extension_invalid",
            )
        current = await self._read_current(relation, params.source_item_id)
        return RelationDeltaResult(
            outcome="committed",
            current=current,
            schema_revision=snapshot.schema_revision,
            request_id=params.idempotency_key,
        )

    async def _usable(
        self, relation_id: str
    ) -> tuple[SchemaSnapshot, NormalizedRelationDescriptor]:
        snapshot, relation = await self._resolve_relation(relation_id)
        if relation.state != "valid":
            raise RelationDataError(
                "relation schema is not safely editable", code="relation_schema_invalid"
            )
        return snapshot, relation

    def _validate_delta(
        self,
        params: RelationDelta,
        relation: NormalizedRelationDescriptor,
        snapshot: SchemaSnapshot,
    ) -> list[RelationDiagnostic]:
        diagnostics: list[RelationDiagnostic] = []
        if relation.kind not in {"o2m", "m2m", "m2a"}:
            diagnostics.append(
                RelationDiagnostic(
                    code="relation_kind_invalid",
                    message="single-valued relations must use relation.updateSingle",
                )
            )
        if params.expected_schema_revision != snapshot.schema_revision:
            diagnostics.append(RelationDiagnostic(code="schema_mismatch", message="schema changed"))
        allowed = set(
            relation.allowed_collections
            if relation.kind == "m2a"
            else [relation.related_collection]
        )
        for item in [
            *(entry.target for entry in params.adds),
            *(entry.target for entry in params.removes),
        ]:
            if item.collection not in allowed:
                diagnostics.append(
                    RelationDiagnostic(
                        code="relation_target_invalid",
                        message="target collection is outside the relation allow-list",
                    )
                )
        context = set(relation.junction.context_fields if relation.junction else [])
        for add in params.adds:
            if set(add.target.junction_values) - context:
                diagnostics.append(
                    RelationDiagnostic(
                        code="junction_field_invalid",
                        message="delta contains an undeclared junction context field",
                    )
                )
        for update in params.updates:
            if set(update.values) - context:
                diagnostics.append(
                    RelationDiagnostic(
                        code="junction_field_invalid",
                        message="delta contains an undeclared junction context field",
                    )
                )
            if relation.kind in {"m2m", "m2a"} and update.expected_revision is None:
                diagnostics.append(
                    RelationDiagnostic(
                        code="junction_revision_required",
                        message="junction updates require the previewed revision",
                    )
                )
        if relation.kind in {"m2m", "m2a"}:
            for remove in params.removes:
                if remove.expected_revision is None:
                    diagnostics.append(
                        RelationDiagnostic(
                            code="junction_revision_required",
                            message="junction removals require the previewed revision",
                        )
                    )
        return diagnostics

    async def _compile_delta(
        self, params: RelationDelta, relation: NormalizedRelationDescriptor
    ) -> dict[str, Any]:
        source_profile = self._profile(relation.source_collection)
        target_primary_keys: dict[str, str] = {}
        targets = (
            relation.allowed_collections
            if relation.kind == "m2a"
            else [relation.related_collection]
            if relation.related_collection
            else []
        )
        for collection in targets:
            target_primary_keys[collection] = await self._primary_key(collection)
        related_primary_key = (
            target_primary_keys.get(relation.related_collection)
            if relation.related_collection
            else None
        )
        relation_plan = {
            "relationId": relation.relation_id,
            "kind": relation.kind,
            "sourceCollection": relation.source_collection,
            "sourcePrimaryKey": source_profile.primary_key,
            "sourceItemId": params.source_item_id,
            "sourceDateUpdatedField": source_profile.date_updated_field,
            "expectedDateUpdated": params.expected_date_updated,
            "relatedCollection": relation.related_collection,
            "relatedPrimaryKey": related_primary_key,
            "manyField": relation.many_field,
            "allowedCollections": relation.allowed_collections,
            "targetPrimaryKeys": target_primary_keys,
            "junction": (
                {
                    "collection": relation.junction.collection,
                    "sourceField": relation.junction.source_field,
                    "targetField": relation.junction.target_field,
                    "collectionField": relation.junction.collection_field,
                    "contextFields": relation.junction.context_fields,
                }
                if relation.junction
                else None
            ),
            "adds": [
                {
                    "collection": add.target.collection,
                    "itemId": add.target.item_id,
                    "junctionValues": add.target.junction_values,
                }
                for add in params.adds
            ],
            "updates": [update.model_dump(mode="json", by_alias=True) for update in params.updates],
            "removes": [
                {
                    "collection": remove.target.collection,
                    "itemId": remove.target.item_id,
                    "junctionId": remove.target.junction_id,
                    "expectedRevision": remove.expected_revision,
                }
                for remove in params.removes
            ],
        }
        return {
            "contract": "vibetable-relation-delta.v1",
            "idempotencyKey": params.idempotency_key,
            "expectedSchemaRevision": params.expected_schema_revision,
            "schemaProof": _relation_schema_proof(relation_plan),
            "relation": relation_plan,
        }

    async def _read_current(
        self, relation: NormalizedRelationDescriptor, source_item_id: str
    ) -> list[RelationTargetRef]:
        token = await self._auth.access_token()
        if relation.kind == "o2m":
            if relation.related_collection is None or relation.many_field is None:
                return []
            primary_key = await self._primary_key(relation.related_collection)
            payload = await self._transport.request(
                "GET",
                f"/items/{quote(relation.related_collection, safe='')}",
                access_token=token,
                query={
                    "filter": {relation.many_field: {"_eq": source_item_id}},
                    "fields": [primary_key],
                    "limit": -1,
                    "sort": [primary_key],
                },
            )
            rows, _ = _list_payload(payload)
            return [
                RelationTargetRef(
                    collection=relation.related_collection,
                    item_id=str(row[primary_key]),
                    label=str(row[primary_key]),
                )
                for row in rows
                if row.get(primary_key) is not None
            ]
        junction = relation.junction
        if junction is None:
            return []
        fields = ["id", junction.target_field, *junction.context_fields]
        if junction.collection_field:
            fields.append(junction.collection_field)
        payload = await self._transport.request(
            "GET",
            f"/items/{quote(junction.collection, safe='')}",
            access_token=token,
            query={
                "filter": {junction.source_field: {"_eq": source_item_id}},
                "fields": fields,
                "limit": -1,
                "sort": ["id"],
            },
        )
        rows, _ = _list_payload(payload)
        output: list[RelationTargetRef] = []
        for row in rows:
            item_id = row.get(junction.target_field)
            junction_id = row.get("id")
            collection = (
                row.get(junction.collection_field)
                if junction.collection_field
                else relation.related_collection
            )
            if item_id is None or junction_id is None or not isinstance(collection, str):
                continue
            output.append(
                RelationTargetRef(
                    collection=collection,
                    item_id=str(item_id),
                    label=str(item_id),
                    junction_id=str(junction_id),
                    junction_revision=_junction_revision(
                        junction_id=junction_id,
                        source_item_id=source_item_id,
                        target_item_id=item_id,
                        collection=collection if junction.collection_field else None,
                        values={field: row.get(field) for field in junction.context_fields},
                    ),
                    junction_values={field: row.get(field) for field in junction.context_fields},
                )
            )
        return output

    async def _primary_key(self, collection: str) -> str:
        profile = self._profiles.get(collection)
        if profile is not None:
            return profile.primary_key
        fields = await self._client.schema_fields()
        candidates = [
            field.get("field")
            for field in fields
            if field.get("collection") == collection
            and isinstance(field.get("schema"), dict)
            and field["schema"].get("is_primary_key") is True
        ]
        if len(candidates) != 1 or not isinstance(candidates[0], str):
            raise RelationDataError(
                f"target collection {collection!r} has no visible primary key",
                code="relation_target_schema_invalid",
            )
        return candidates[0]

    def _profile(self, collection: str) -> CollectionProfile:
        profile = self._profiles.get(collection)
        if profile is None:
            raise RelationDataError(
                f"collection {collection!r} is not available",
                code="relation_collection_unknown",
            )
        return profile


def _render_label(template: str | None, row: dict[str, Any], *, fallback: str) -> str:
    if not template:
        return fallback
    return _TEMPLATE_FIELD.sub(lambda match: str(row.get(match.group(1), "")), template)


def _list_payload(payload: Any) -> tuple[list[dict[str, Any]], dict[str, Any]]:
    data = payload.get("data") if isinstance(payload, dict) else None
    if not isinstance(data, list) or not all(isinstance(item, dict) for item in data):
        raise RelationDataError("Directus returned an invalid relation response")
    meta = payload.get("meta")
    return data, meta if isinstance(meta, dict) else {}


def _nonnegative_int(value: Any, *, default: int) -> int:
    return value if isinstance(value, int) and value >= 0 else default


def _junction_revision(
    *,
    junction_id: Any,
    source_item_id: Any,
    target_item_id: Any,
    collection: str | None,
    values: dict[str, Any],
) -> str:
    payload = {
        "id": str(junction_id),
        "source": str(source_item_id),
        "target": str(target_item_id),
        "collection": collection,
        "values": {key: values[key] for key in sorted(values)},
    }
    encoded = json.dumps(
        payload,
        ensure_ascii=False,
        sort_keys=False,
        separators=(",", ":"),
    )
    return hashlib.sha256(encoded.encode("utf-8")).hexdigest()


def _relation_schema_proof(relation: dict[str, Any]) -> str:
    kind = relation["kind"]
    proof: dict[str, Any] = {
        "version": 1,
        "kind": kind,
        "source": {
            "collection": relation["sourceCollection"],
            "primaryKey": relation["sourcePrimaryKey"],
            "dateUpdatedField": relation.get("sourceDateUpdatedField"),
        },
    }
    if kind == "o2m":
        proof["target"] = {
            "collection": relation["relatedCollection"],
            "primaryKey": relation["relatedPrimaryKey"],
            "manyField": relation["manyField"],
        }
    else:
        if kind == "m2m":
            proof["target"] = {
                "collection": relation["relatedCollection"],
                "primaryKey": relation["relatedPrimaryKey"],
            }
        else:
            target_primary_keys = relation.get("targetPrimaryKeys") or {}
            proof["target"] = {
                "collections": sorted(relation.get("allowedCollections") or []),
                "primaryKeys": {
                    key: target_primary_keys[key] for key in sorted(target_primary_keys)
                },
            }
        junction = relation["junction"]
        proof["junction"] = {
            "collection": junction["collection"],
            "sourceField": junction["sourceField"],
            "targetField": junction["targetField"],
            "collectionField": junction.get("collectionField"),
            "contextFields": sorted(junction.get("contextFields") or []),
        }
    encoded = json.dumps(
        proof,
        ensure_ascii=False,
        sort_keys=False,
        separators=(",", ":"),
    )
    return hashlib.sha256(encoded.encode("utf-8")).hexdigest()


__all__ = ["RelationDataError", "RelationDataService"]
