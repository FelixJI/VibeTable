"""Persistence and orchestration for VibeTable-owned virtual Lookup fields."""

from __future__ import annotations

import builtins
import hashlib
import json
import time
import uuid
from collections import OrderedDict
from collections.abc import Awaitable, Callable
from typing import Any, Protocol

from backend.adapters.directus.transport import DirectusTransport
from backend.application.lookup_compiler import LookupCompileError, compile_lookup_plan
from backend.contracts.lookup import (
    LookupCollectionParams,
    LookupCreateParams,
    LookupDefinition,
    LookupDeleteParams,
    LookupListResult,
    LookupMutationResult,
    LookupPreviewParams,
    LookupQuery,
    LookupQueryParams,
    LookupQueryResult,
    LookupUpdateParams,
    LookupValidateParams,
    LookupValidationResult,
    validate_lookup_dependency_graph,
)
from backend.contracts.relation_admin import SchemaSnapshot

_COLLECTION = "vibetable_lookup_definitions"


class _TokenProvider(Protocol):
    async def access_token(self) -> str: ...


class _SchemaClient(Protocol):
    async def schema_fields(self) -> list[dict[str, Any]]: ...
    async def schema_relations(self) -> list[dict[str, Any]]: ...
    async def relation_lookup_capabilities(self) -> dict[str, Any]: ...


class LookupServiceError(Exception):
    def __init__(self, message: str, *, code: str = "lookup_error") -> None:
        super().__init__(message)
        self.code = code

    def rpc_error_data(self) -> dict[str, str]:
        return {"code": self.code, "message": str(self)}


class LookupService:
    """Own Lookup definitions and delegate authoritative reads to Directus.

    Definitions live in a hidden app collection.  The current user's access
    token is used for metadata and execution calls; the query extension is the
    only component allowed to traverse business relations or aggregate rows.
    """

    def __init__(
        self,
        *,
        transport: DirectusTransport,
        auth: _TokenProvider,
        project: str,
        client: _SchemaClient | None = None,
        schema_provider: Callable[[str], Awaitable[SchemaSnapshot]] | None = None,
        query_cache_max_entries: int = 256,
        query_cache_ttl_seconds: float = 2.0,
    ) -> None:
        if query_cache_max_entries < 1:
            raise ValueError("query_cache_max_entries must be positive")
        if query_cache_ttl_seconds <= 0:
            raise ValueError("query_cache_ttl_seconds must be positive")
        self._transport = transport
        self._auth = auth
        self._project = project
        self._client = client
        self._schema_provider = schema_provider
        self._query_cache: OrderedDict[str, tuple[float, set[str], dict[str, Any]]] = OrderedDict()
        self._cache_max_entries = query_cache_max_entries
        self._cache_ttl_seconds = query_cache_ttl_seconds

    def set_schema_provider(self, provider: Callable[[str], Awaitable[SchemaSnapshot]]) -> None:
        self._schema_provider = provider

    def invalidate_collection(self, collection: str | None = None) -> None:
        """Invalidate cached results affected by a realtime collection change."""
        if collection is None or collection == _COLLECTION:
            self._query_cache.clear()
            return
        self._query_cache = OrderedDict(
            (key, entry) for key, entry in self._query_cache.items() if collection not in entry[1]
        )

    async def list(self, params: LookupCollectionParams) -> LookupListResult:
        definitions = await self._read_definitions(params.collection)
        return LookupListResult(
            collection=params.collection,
            definitions=definitions,
            lookup_revision=_lookup_revision(definitions),
        )

    async def all_definitions(self) -> builtins.list[LookupDefinition]:
        """Return all active definitions for internal dependency validation."""
        return await self._read_all_definitions()

    async def validate(self, params: LookupValidateParams) -> LookupValidationResult:
        known = await self._read_all_definitions()
        definitions_by_id = {definition.lookup_id: definition for definition in known}
        definitions_by_id.update(
            {
                definition.lookup_id: definition
                for definition in params.existing
                if definition.lookup_id != params.definition.lookup_id
            }
        )
        definitions_by_id[params.definition.lookup_id] = params.definition
        definitions = builtins.list(definitions_by_id.values())
        try:
            validate_lookup_dependency_graph(definitions)
        except ValueError as exc:
            raise LookupServiceError(str(exc), code="lookup_dependency_invalid") from exc
        snapshot: SchemaSnapshot | None = None
        if self._schema_provider is not None:
            snapshot = await self._schema_provider(params.definition.collection)
            _validate_lookup_namespace(definitions, snapshot)
        else:
            _validate_lookup_namespace(definitions, None)
        if self._client is not None and snapshot is not None:
            capabilities = await self._client.relation_lookup_capabilities()
            if capabilities.get("lookup_query_v1") is not True:
                raise LookupServiceError(
                    "Directus Lookup extension is unavailable",
                    code="lookup_extension_missing",
                )
            fields, relations = await _gather_lookup_inputs(
                self._client.schema_fields(),
                self._client.schema_relations(),
            )
            try:
                plan = compile_lookup_plan(
                    params=LookupQueryParams(
                        collection=params.definition.collection,
                        field_refs=[params.definition.lookup_id],
                        query=LookupQuery(limit=1),
                        schema_revision=snapshot.schema_revision,
                        permission_revision=snapshot.permission_revision,
                        lookup_revision=snapshot.lookup_revision,
                    ),
                    snapshot=snapshot,
                    definitions=definitions,
                    fields=fields,
                    relations=relations,
                )
            except LookupCompileError as exc:
                raise LookupServiceError(str(exc), code="lookup_plan_invalid") from exc
            _attach_persisted_definition_proof(plan, known)
            token = await self._auth.access_token()
            payload = await self._transport.request(
                "POST",
                "/vibetable-lookup-query/validate",
                access_token=token,
                json_body=plan,
            )
            data = payload.get("data") if isinstance(payload, dict) else None
            if not isinstance(data, dict) or data.get("valid") is not True:
                raise LookupServiceError(
                    "Directus rejected the Lookup execution plan",
                    code="lookup_plan_invalid",
                )
        return LookupValidationResult(
            definition=params.definition,
            valid=params.definition.state == "valid",
            diagnostics=params.definition.diagnostics,
            lookup_revision=_lookup_revision(definitions),
        )

    async def create(self, params: LookupCreateParams) -> LookupMutationResult:
        current = await self._read_definitions(params.definition.collection)
        existing = next(
            (item for item in current if item.lookup_id == params.definition.lookup_id), None
        )
        if existing is not None:
            if existing == params.definition:
                await self.validate(
                    LookupValidateParams(definition=params.definition, existing=current)
                )
                return _mutation(existing, current)
            raise LookupServiceError(
                f"Lookup {params.definition.lookup_id!r} already exists",
                code="lookup_conflict",
            )
        await self.validate(LookupValidateParams(definition=params.definition, existing=current))
        token = await self._auth.access_token()
        await self._transport.request(
            "POST",
            f"/items/{_COLLECTION}",
            access_token=token,
            headers={"X-Request-ID": params.request_id},
            json_body=_record_body(
                self._record_id(params.definition), params.definition, status="active"
            ),
            expected_status=(200, 201),
        )
        updated = [*current, params.definition]
        return _mutation(params.definition, updated)

    async def update(self, params: LookupUpdateParams) -> LookupMutationResult:
        definition = params.definition
        current = await self._read_definitions(definition.collection)
        existing = next((item for item in current if item.lookup_id == definition.lookup_id), None)
        if existing is None:
            raise LookupServiceError(
                f"Lookup {definition.lookup_id!r} does not exist", code="lookup_not_found"
            )
        if existing.revision != params.expected_revision:
            raise LookupServiceError("Lookup was changed by another user", code="lookup_conflict")
        if definition.revision <= existing.revision:
            definition = definition.model_copy(update={"revision": existing.revision + 1})
        candidate = [
            definition if item.lookup_id == definition.lookup_id else item for item in current
        ]
        await self.validate(LookupValidateParams(definition=definition, existing=current))
        payload = await self._conditional_patch(
            self._record_id(definition),
            expected_revision=params.expected_revision,
            values={
                "definition": definition.model_dump(mode="json", by_alias=True),
                "revision": definition.revision,
                "status": "active",
            },
            request_id=params.request_id,
        )
        if not _response_list(payload):
            raise LookupServiceError("Lookup was changed by another user", code="lookup_conflict")
        return _mutation(definition, candidate)

    async def delete(self, params: LookupDeleteParams) -> LookupMutationResult:
        all_definitions = await self._read_all_definitions()
        current = [item for item in all_definitions if item.collection == params.collection]
        existing = next((item for item in current if item.lookup_id == params.lookup_id), None)
        if existing is None:
            raise LookupServiceError(
                f"Lookup {params.lookup_id!r} does not exist", code="lookup_not_found"
            )
        dependents = [
            item.lookup_id for item in all_definitions if params.lookup_id in item.dependencies
        ]
        if dependents:
            raise LookupServiceError(
                "Lookup is referenced by: " + ", ".join(sorted(dependents)),
                code="lookup_dependency_exists",
            )
        payload = await self._conditional_patch(
            self._record_id(existing),
            expected_revision=params.expected_revision,
            values={"status": "deleted", "revision": existing.revision + 1},
            request_id=params.request_id,
        )
        if not _response_list(payload):
            raise LookupServiceError("Lookup was changed by another user", code="lookup_conflict")
        remaining = [item for item in current if item.lookup_id != params.lookup_id]
        return LookupMutationResult(
            collection=params.collection,
            lookup_id=params.lookup_id,
            deleted=True,
            lookup_revision=_lookup_revision(remaining),
        )

    async def cascade_delete(self, lookup_ids: builtins.list[str], request_id: str) -> None:
        """Soft-delete an explicitly reviewed, dependency-closed Lookup set."""
        requested: set[str] = set(lookup_ids)
        records = await self._read_all_definition_records()
        existing = {item.lookup_id: item for item, _status in records}
        if requested - set(existing):
            raise LookupServiceError(
                "Lookup cascade set changed before apply", code="lookup_conflict"
            )
        current = [item for item, status in records if status == "active"]
        outside_dependents = [
            item.lookup_id
            for item in current
            if item.lookup_id not in requested and requested.intersection(item.dependencies)
        ]
        if outside_dependents:
            raise LookupServiceError(
                "Lookup cascade is not dependency-closed",
                code="lookup_dependency_exists",
            )
        active_requested = {item.lookup_id: item for item in current if item.lookup_id in requested}
        for lookup_id in _dependent_first(active_requested):
            definition = existing[lookup_id]
            payload = await self._conditional_patch(
                self._record_id(definition),
                expected_revision=definition.revision,
                values={"status": "deleted", "revision": definition.revision + 1},
                request_id=f"{request_id}:{lookup_id}",
            )
            if not _response_list(payload):
                raise LookupServiceError("Lookup changed during cascade", code="lookup_conflict")
        self.invalidate_collection(_COLLECTION)

    async def query(self, params: LookupQueryParams) -> LookupQueryResult:
        return await self._execute_query(params, definitions_override=None)

    async def preview(self, params: LookupPreviewParams) -> LookupQueryResult:
        """Execute unsaved definitions through the same authoritative engine."""
        persisted = await self._read_all_definitions()
        replacements = {item.lookup_id: item for item in params.definitions}
        definitions = [replacements.pop(item.lookup_id, item) for item in persisted] + list(
            replacements.values()
        )
        try:
            validate_lookup_dependency_graph(definitions)
        except ValueError as exc:
            raise LookupServiceError(str(exc), code="lookup_dependency_invalid") from exc
        query_params = LookupQueryParams.model_validate(
            params.model_dump(mode="json", by_alias=True, exclude={"definitions"})
        )
        return await self._execute_query(query_params, definitions_override=definitions)

    async def _execute_query(
        self,
        params: LookupQueryParams,
        *,
        definitions_override: builtins.list[LookupDefinition] | None,
    ) -> LookupQueryResult:
        if self._client is None or self._schema_provider is None:
            raise LookupServiceError(
                "Lookup query orchestration is not configured",
                code="lookup_extension_missing",
            )
        capabilities = await self._client.relation_lookup_capabilities()
        if capabilities.get("lookup_query_v1") is not True:
            raise LookupServiceError(
                "Directus Lookup extension is unavailable",
                code=(
                    "lookup_permission_denied"
                    if capabilities.get("reason") == "permission_denied"
                    else "lookup_extension_missing"
                ),
            )
        snapshot, persisted, fields, relations = await _gather_lookup_inputs(
            self._schema_provider(params.collection),
            self._read_all_definitions(),
            self._client.schema_fields(),
            self._client.schema_relations(),
        )
        definitions = definitions_override if definitions_override is not None else persisted
        _validate_lookup_namespace(definitions, snapshot)
        root_definitions = [
            definition for definition in definitions if definition.collection == params.collection
        ]
        if (
            definitions_override is None
            and _lookup_revision(root_definitions) != params.lookup_revision
        ):
            raise LookupServiceError(
                "Lookup definitions changed before execution", code="lookup_revision_mismatch"
            )
        try:
            plan = compile_lookup_plan(
                params=params,
                snapshot=snapshot,
                definitions=definitions,
                fields=fields,
                relations=relations,
            )
        except LookupCompileError as exc:
            raise LookupServiceError(str(exc), code="lookup_plan_invalid") from exc
        _attach_persisted_definition_proof(plan, persisted)
        token = await self._auth.access_token()
        cache_key = _query_cache_key(plan, token)
        now = time.monotonic()
        _prune_expired_cache(self._query_cache, now)
        cached = self._query_cache.get(cache_key)
        if cached is not None and cached[0] > now:
            self._query_cache.move_to_end(cache_key)
            cached_data = dict(cached[2])
            cached_data["generation"] = params.request_generation
            return _translate_query_result(params, definitions, cached_data)
        payload = await self._transport.request(
            "POST",
            "/vibetable-lookup-query/query",
            access_token=token,
            json_body=plan,
        )
        data = payload.get("data") if isinstance(payload, dict) else None
        if not isinstance(data, dict):
            raise LookupServiceError(
                "Directus Lookup extension returned an invalid response",
                code="lookup_extension_invalid",
            )
        self._query_cache[cache_key] = (
            now + self._cache_ttl_seconds,
            _plan_collections(plan),
            dict(data),
        )
        self._query_cache.move_to_end(cache_key)
        while len(self._query_cache) > self._cache_max_entries:
            self._query_cache.popitem(last=False)
        return _translate_query_result(params, definitions, data)

    async def _read_definitions(self, collection: str) -> builtins.list[LookupDefinition]:
        token = await self._auth.access_token()
        payload = await self._transport.request(
            "GET",
            f"/items/{_COLLECTION}",
            access_token=token,
            query={
                "filter": {
                    "_and": [{"collection": {"_eq": collection}}, {"status": {"_eq": "active"}}]
                },
                "fields": ["definition", "lookup_id", "revision"],
                "limit": -1,
                "sort": ["lookup_id"],
            },
        )
        rows = _response_list(payload)
        definitions: builtins.list[LookupDefinition] = []
        seen: set[str] = set()
        for row in rows:
            raw = row.get("definition")
            try:
                definition = LookupDefinition.model_validate(raw)
            except Exception as exc:
                raise LookupServiceError(
                    "Stored Lookup definition is invalid", code="lookup_schema_invalid"
                ) from exc
            if (
                definition.collection != collection
                or row.get("lookup_id") != definition.lookup_id
                or definition.lookup_id in seen
            ):
                raise LookupServiceError(
                    "Stored Lookup identity is inconsistent", code="lookup_schema_invalid"
                )
            seen.add(definition.lookup_id)
            definitions.append(definition)
        _validate_lookup_namespace(definitions, None)
        return sorted(definitions, key=lambda item: item.lookup_id)

    async def _read_all_definitions(self) -> builtins.list[LookupDefinition]:
        token = await self._auth.access_token()
        payload = await self._transport.request(
            "GET",
            f"/items/{_COLLECTION}",
            access_token=token,
            query={
                "filter": {"status": {"_eq": "active"}},
                "fields": ["definition", "lookup_id", "revision"],
                "limit": -1,
                "sort": ["lookup_id"],
            },
        )
        definitions: builtins.list[LookupDefinition] = []
        seen: set[str] = set()
        for row in _response_list(payload):
            try:
                definition = LookupDefinition.model_validate(row.get("definition"))
            except Exception as exc:
                raise LookupServiceError(
                    "Stored Lookup definition is invalid", code="lookup_schema_invalid"
                ) from exc
            if row.get("lookup_id") != definition.lookup_id or definition.lookup_id in seen:
                raise LookupServiceError(
                    "Stored Lookup identity is inconsistent", code="lookup_schema_invalid"
                )
            seen.add(definition.lookup_id)
            definitions.append(definition)
        validate_lookup_dependency_graph(definitions)
        _validate_lookup_namespace(definitions, None)
        return sorted(definitions, key=lambda item: item.lookup_id)

    async def _read_all_definition_records(
        self,
    ) -> builtins.list[tuple[LookupDefinition, str]]:
        token = await self._auth.access_token()
        payload = await self._transport.request(
            "GET",
            f"/items/{_COLLECTION}",
            access_token=token,
            query={
                "filter": {"status": {"_in": ["active", "deleted"]}},
                "fields": ["definition", "lookup_id", "revision", "status"],
                "limit": -1,
                "sort": ["lookup_id"],
            },
        )
        records: builtins.list[tuple[LookupDefinition, str]] = []
        seen: set[str] = set()
        for row in _response_list(payload):
            status = row.get("status")
            if status not in {"active", "deleted"}:
                raise LookupServiceError(
                    "Stored Lookup status is invalid", code="lookup_schema_invalid"
                )
            try:
                definition = LookupDefinition.model_validate(row.get("definition"))
            except Exception as exc:
                raise LookupServiceError(
                    "Stored Lookup definition is invalid", code="lookup_schema_invalid"
                ) from exc
            if row.get("lookup_id") != definition.lookup_id or definition.lookup_id in seen:
                raise LookupServiceError(
                    "Stored Lookup identity is inconsistent", code="lookup_schema_invalid"
                )
            seen.add(definition.lookup_id)
            records.append((definition, status))
        return records

    async def _conditional_patch(
        self,
        record_id: str,
        *,
        expected_revision: int,
        values: dict[str, Any],
        request_id: str,
    ) -> Any:
        token = await self._auth.access_token()
        return await self._transport.request(
            "PATCH",
            f"/items/{_COLLECTION}",
            access_token=token,
            headers={"X-Request-ID": request_id},
            query={"fields": ["id", "revision"]},
            json_body={
                "query": {
                    "filter": {
                        "_and": [
                            {"id": {"_eq": record_id}},
                            {"revision": {"_eq": expected_revision}},
                            {"status": {"_eq": "active"}},
                        ]
                    },
                    "limit": 1,
                },
                "data": values,
            },
        )

    def _record_id(self, definition: LookupDefinition) -> str:
        identity = f"{self._project}:{definition.collection}:{definition.lookup_id}"
        return str(uuid.uuid5(uuid.NAMESPACE_URL, "vibetable:lookup:" + identity))


def _record_body(record_id: str, definition: LookupDefinition, *, status: str) -> dict[str, Any]:
    return {
        "id": record_id,
        "status": status,
        "collection": definition.collection,
        "lookup_id": definition.lookup_id,
        "field_key": definition.field_key,
        "definition": definition.model_dump(mode="json", by_alias=True),
        "revision": definition.revision,
    }


def _lookup_revision(definitions: list[LookupDefinition]) -> str:
    payload = [
        item.model_dump(mode="json", by_alias=True)
        for item in sorted(definitions, key=lambda definition: definition.lookup_id)
    ]
    encoded = json.dumps(payload, ensure_ascii=False, sort_keys=True, separators=(",", ":"))
    return hashlib.sha256(encoded.encode("utf-8")).hexdigest()


def _attach_persisted_definition_proof(
    plan: dict[str, Any], persisted: list[LookupDefinition]
) -> None:
    compiled_ids = {
        item.get("lookupId")
        for item in plan.get("lookups", [])
        if isinstance(item, dict) and isinstance(item.get("lookupId"), str)
    }
    plan["definitionRevisions"] = {
        definition.lookup_id: definition.revision
        for definition in persisted
        if definition.lookup_id in compiled_ids
    }


def _dependent_first(definitions: dict[str, LookupDefinition]) -> list[str]:
    remaining = set(definitions)
    output: list[str] = []
    while remaining:
        leaves = sorted(
            lookup_id
            for lookup_id in remaining
            if not any(lookup_id in definitions[candidate].dependencies for candidate in remaining)
        )
        if not leaves:
            raise LookupServiceError(
                "Lookup cascade contains a dependency cycle",
                code="lookup_dependency_invalid",
            )
        output.extend(leaves)
        remaining.difference_update(leaves)
    return output


def _mutation(
    definition: LookupDefinition, definitions: list[LookupDefinition]
) -> LookupMutationResult:
    return LookupMutationResult(
        collection=definition.collection,
        lookup_id=definition.lookup_id,
        definition=definition,
        lookup_revision=_lookup_revision(definitions),
    )


def _response_list(payload: Any) -> list[dict[str, Any]]:
    data = payload.get("data") if isinstance(payload, dict) else None
    if not isinstance(data, list) or not all(isinstance(item, dict) for item in data):
        raise LookupServiceError("Directus returned an invalid list response")
    return data


async def _gather_lookup_inputs(*awaitables: Awaitable[Any]) -> tuple[Any, ...]:
    import asyncio

    return tuple(await asyncio.gather(*awaitables))


def _translate_query_result(
    params: LookupQueryParams,
    definitions: list[LookupDefinition],
    data: dict[str, Any],
) -> LookupQueryResult:
    if data.get("contract") != "vibetable-lookup-query.v1":
        raise LookupServiceError(
            "Directus Lookup extension contract is incompatible",
            code="lookup_extension_incompatible",
        )
    revisions = data.get("revisions")
    page = data.get("page")
    raw_rows = data.get("rows")
    raw_groups = data.get("groups")
    if (
        data.get("generation") != params.request_generation
        or not isinstance(revisions, dict)
        or not isinstance(page, dict)
        or not isinstance(raw_rows, list)
        or not isinstance(raw_groups, list)
    ):
        raise LookupServiceError(
            "Directus Lookup extension returned an invalid response",
            code="lookup_extension_invalid",
        )
    expected_revisions = {
        "schema": params.schema_revision,
        "permission": params.permission_revision,
        "lookup": params.lookup_revision,
    }
    if revisions != expected_revisions:
        raise LookupServiceError(
            "Directus Lookup response revisions do not match the request",
            code="lookup_revision_mismatch",
        )
    definitions_by_ref: dict[str, LookupDefinition] = {}
    for definition in definitions:
        for ref in {
            definition.lookup_id,
            definition.field_key,
            f"{definition.collection}.{definition.field_key}",
        }:
            definitions_by_ref[ref] = definition
    columns = []
    for ref in params.field_refs:
        column_definition = definitions_by_ref.get(ref)
        if column_definition is None:
            continue
        columns.append(
            {
                "fieldRef": ref,
                "title": column_definition.display_name,
                "outputType": column_definition.output_type,
                "nullable": True,
                "scale": column_definition.output_scale,
                "state": column_definition.state,
            }
        )
    rows: list[dict[str, Any]] = []
    for row in raw_rows:
        if not isinstance(row, dict) or not isinstance(row.get("cells"), dict):
            raise LookupServiceError("Directus Lookup extension returned an invalid row")
        raw_provenance = row.get("provenance", {})
        if not isinstance(raw_provenance, dict):
            raise LookupServiceError("Directus Lookup extension returned invalid provenance")
        translated_cells: dict[str, Any] = {}
        for ref, value in row["cells"].items():
            if ref not in definitions_by_ref:
                translated_cells[ref] = value
                continue
            sources = raw_provenance.get(ref, [])
            if not isinstance(sources, list) or any(
                not isinstance(source, dict)
                or not isinstance(source.get("collection"), str)
                or not isinstance(source.get("itemId"), (str, int))
                or isinstance(source.get("itemId"), bool)
                for source in sources
            ):
                raise LookupServiceError("Directus Lookup extension returned invalid provenance")
            translated_cells[ref] = {
                "state": "ok",
                "value": value,
                "provenance": [
                    {
                        "collection": source["collection"],
                        "itemId": str(source["itemId"]),
                        "value": source.get("value"),
                    }
                    for source in sources
                ],
            }
        rows.append({"rowKey": row.get("primaryKey"), **translated_cells})
    groups = []
    for group in raw_groups:
        if not isinstance(group, dict):
            raise LookupServiceError("Directus Lookup extension returned an invalid group")
        groups.append(
            {
                "path": group.get("path", []),
                "key": group.get("key"),
                "count": group.get("count", 0),
                "aggregates": group.get("aggregateCells", {}),
                "childCursor": group.get("childPageCursor"),
            }
        )
    return LookupQueryResult.model_validate(
        {
            "collection": params.collection,
            "requestGeneration": params.request_generation,
            "schemaRevision": params.schema_revision,
            "permissionRevision": params.permission_revision,
            "lookupRevision": params.lookup_revision,
            "columns": columns,
            "rows": rows,
            "groups": groups,
            "offset": page.get("offset"),
            "limit": page.get("limit"),
            "filteredRows": data.get("total"),
            "totalRows": data.get("rootTotal", data.get("total")),
        }
    )


def _query_cache_key(plan: dict[str, Any], token: str) -> str:
    normalized = dict(plan)
    normalized["generation"] = 0
    canonical = json.dumps(normalized, sort_keys=True, separators=(",", ":"), ensure_ascii=False)
    identity = hashlib.sha256(token.encode()).hexdigest()
    return hashlib.sha256(f"{identity}:{canonical}".encode()).hexdigest()


def _validate_lookup_namespace(
    definitions: builtins.list[LookupDefinition],
    snapshot: SchemaSnapshot | None,
) -> None:
    """Keep virtual field keys unique and separate from visible physical fields."""

    owners: dict[tuple[str, str], str] = {}
    for definition in definitions:
        key = (definition.collection, definition.field_key)
        existing = owners.get(key)
        if existing is not None and existing != definition.lookup_id:
            raise LookupServiceError(
                f"Lookup fieldKey {definition.field_key!r} is already used in "
                f"collection {definition.collection!r}",
                code="lookup_field_conflict",
            )
        owners[key] = definition.lookup_id

    if snapshot is None:
        return
    physical_fields = {column.name for column in snapshot.columns if column.kind != "lookup"}
    conflicts = sorted(
        definition.field_key
        for definition in definitions
        if definition.collection == snapshot.collection and definition.field_key in physical_fields
    )
    if conflicts:
        raise LookupServiceError(
            "Lookup fieldKey conflicts with visible physical fields: " + ", ".join(conflicts),
            code="lookup_field_conflict",
        )


def _prune_expired_cache(
    cache: OrderedDict[str, tuple[float, set[str], dict[str, Any]]],
    now: float,
) -> None:
    for key in [key for key, entry in cache.items() if entry[0] <= now]:
        del cache[key]


def _plan_collections(plan: dict[str, Any]) -> set[str]:
    collections: set[str] = set()

    def visit(value: Any, parent: str | None = None) -> None:
        if isinstance(value, dict):
            for key, nested in value.items():
                if key in {"collection", "fromCollection", "toCollection"} and isinstance(
                    nested, str
                ):
                    collections.add(nested)
                elif key == "targetCollections" and isinstance(nested, list):
                    collections.update(item for item in nested if isinstance(item, str))
                visit(nested, key)
        elif isinstance(value, list):
            for nested in value:
                visit(nested, parent)

    visit(plan)
    return collections


__all__ = ["LookupService", "LookupServiceError"]
