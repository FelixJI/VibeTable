"""Two-phase, idempotent lifecycle for VibeTable-managed Directus relations."""

from __future__ import annotations

import hashlib
import json
import re
from collections.abc import Awaitable, Callable
from dataclasses import dataclass
from typing import Any, Literal, Protocol
from urllib.parse import quote

from backend.adapters.directus.relation_schema import normalize_directus_relations
from backend.adapters.directus.transport import DirectusTransport
from backend.contracts.lookup import LookupDefinition
from backend.contracts.relation_admin import (
    ApplyRelationChangeParams,
    CreateM2AConfig,
    CreateM2MConfig,
    CreateM2OConfig,
    CreateO2MConfig,
    NormalizedRelationDescriptor,
    PreviewRelationChangeParams,
    RelationChangePlan,
    RelationChangeResult,
    RelationDiagnostic,
    SchemaChangeStep,
    SchemaSnapshot,
)

_IDENTIFIER = re.compile(r"^[A-Za-z_][A-Za-z0-9_]*$")
_MANAGED_NOTE = "[vibetable-managed-relation]"
_RelationConfig = CreateM2OConfig | CreateO2MConfig | CreateM2MConfig | CreateM2AConfig
_LookupProvider = Callable[[], Awaitable[list[LookupDefinition]]]
_LookupCascade = Callable[[list[str], str], Awaitable[None]]
_SchemaProvider = Callable[[str], Awaitable[SchemaSnapshot]]


class _Auth(Protocol):
    async def access_token(self) -> str: ...


class _SchemaClient(Protocol):
    async def schema_fields(self) -> list[dict[str, Any]]: ...
    async def schema_relations(self) -> list[dict[str, Any]]: ...


class RelationSchemaError(Exception):
    def __init__(self, message: str, *, code: str = "relation_schema_error") -> None:
        super().__init__(message)
        self.code = code

    def rpc_error_data(self) -> dict[str, str]:
        return {"code": self.code, "message": str(self)}


@dataclass(frozen=True)
class _Mutation:
    step: SchemaChangeStep
    method: str
    path: str
    body: dict[str, Any] | None
    identity: tuple[str, str, str]


@dataclass(frozen=True)
class _StoredPlan:
    public: RelationChangePlan
    mutations: tuple[_Mutation, ...]
    expected_relation_id: str | None


class RelationSchemaService:
    """Plans schema changes without writes, then applies reviewed plans.

    Directus schema changes are not assumed to be transactional.  Each apply
    records progress in ``vibetable_schema_operations`` and checks whether a
    step is already satisfied before writing, so retrying an operation never
    creates a second field/relation/junction.
    """

    def __init__(
        self,
        *,
        client: _SchemaClient,
        transport: DirectusTransport,
        auth: _Auth,
        schema_provider: _SchemaProvider,
        lookup_provider: _LookupProvider | None = None,
        lookup_cascade: _LookupCascade | None = None,
    ) -> None:
        self._client = client
        self._transport = transport
        self._auth = auth
        self._schema_provider = schema_provider
        self._lookup_provider = lookup_provider
        self._lookup_cascade = lookup_cascade
        self._plans: dict[str, _StoredPlan] = {}

    async def preview(self, params: PreviewRelationChangeParams) -> RelationChangePlan:
        snapshot, fields, relations = await self._inputs(params.collection)
        if snapshot.schema_revision != params.expected_schema_revision:
            raise RelationSchemaError("schema changed before preview", code="schema_mismatch")
        discovery = normalize_directus_relations(fields=fields, relations=relations)
        current = next(
            (item for item in discovery.relations if item.relation_id == params.relation_id),
            None,
        )
        diagnostics: list[RelationDiagnostic] = []
        affected = await self._affected_lookups(params.relation_id)
        mutations: list[_Mutation] = []
        expected_relation_id: str | None = params.relation_id

        if params.action == "create":
            assert params.config is not None
            if any(
                item.field_ref == f"{params.collection}.{params.config.field_key}"
                for item in discovery.relations
            ):
                diagnostics.append(_error("relation_field_exists", "relation field already exists"))
            else:
                mutations = self._plan_create(params.collection, params.config, fields)
                expected_relation_id = f"{params.collection}.{params.config.field_key}"
        elif current is None:
            diagnostics.append(_error("relation_not_found", "relation no longer exists"))
        elif params.action == "delete":
            if affected:
                diagnostics.append(
                    _error(
                        "lookup_dependency_exists",
                        "relation is referenced by Lookup definitions; explicit cascade is required",
                        severity="warning",
                    )
                )
            if current is not None and not current.managed:
                diagnostics.append(
                    _error(
                        "external_relation_delete_blocked",
                        "adopted Directus relations are never deleted by VibeTable",
                    )
                )
            if current is not None and not any(
                diagnostic.severity == "error" for diagnostic in diagnostics
            ):
                mutations = self._plan_delete(current)
        else:
            assert params.config is not None
            assert current is not None
            if not current.managed:
                diagnostics.append(
                    _error(
                        "external_relation_update_blocked",
                        "adopted Directus relations are never modified by VibeTable",
                    )
                )
            else:
                mutations, update_diagnostics = self._plan_update(current, params.config, fields)
                diagnostics.extend(update_diagnostics)

        public_steps = [mutation.step for mutation in mutations]
        plan_id = _plan_id(params, public_steps)
        public = RelationChangePlan(
            plan_id=plan_id,
            collection=params.collection,
            expected_schema_revision=params.expected_schema_revision,
            action=params.action,
            relation_id=params.relation_id,
            steps=public_steps,
            affected_lookup_ids=affected,
            diagnostics=diagnostics,
            can_apply=(
                not any(diagnostic.severity == "error" for diagnostic in diagnostics)
                and bool(public_steps)
            ),
        )
        self._plans[plan_id] = _StoredPlan(public, tuple(mutations), expected_relation_id)
        return public

    async def apply(self, params: ApplyRelationChangeParams) -> RelationChangeResult:
        token = await self._auth.access_token()
        existing = await self._read_operation(params.operation_id, token)
        stored = self._plans.get(params.plan_id)
        if stored is None and existing is not None:
            stored = _restore_stored_plan(existing.get("plan"))
            if stored is not None:
                self._plans[stored.public.plan_id] = stored
        if stored is None or stored.public.plan_id != params.plan_id:
            raise RelationSchemaError("relation plan expired", code="relation_plan_missing")
        plan = stored.public
        if not plan.can_apply:
            raise RelationSchemaError("relation plan is blocked", code="relation_plan_blocked")
        if params.expected_schema_revision != plan.expected_schema_revision:
            raise RelationSchemaError(
                "schema revision does not match the plan", code="schema_mismatch"
            )
        missing_cascade = (
            set(plan.affected_lookup_ids) - set(params.cascade_lookup_ids)
            if plan.action == "delete"
            else set()
        )
        if missing_cascade:
            raise RelationSchemaError(
                "dependent Lookups were not explicitly cascaded",
                code="lookup_dependency_exists",
            )

        applied_keys = set(existing.get("applied_steps", [])) if existing else set()
        if existing and existing.get("plan_id") != params.plan_id:
            raise RelationSchemaError(
                "operation id belongs to another plan", code="operation_conflict"
            )
        if existing and existing.get("status") == "complete":
            return await self._result_for_completed_plan(stored)
        live_snapshot = await self._schema_provider(plan.collection)
        persisted_plan = existing.get("plan") if existing else None
        persisted_revision = (
            persisted_plan.get("lastSchemaRevision") if isinstance(persisted_plan, dict) else None
        )
        last_revision = (
            persisted_revision
            if isinstance(persisted_revision, str)
            else plan.expected_schema_revision
        )
        if not existing and live_snapshot.schema_revision != plan.expected_schema_revision:
            raise RelationSchemaError("schema changed after preview", code="schema_mismatch")
        if existing and live_snapshot.schema_revision != last_revision:
            next_mutation = next(
                (item for item in stored.mutations if item.step.key not in applied_keys),
                None,
            )
            fields, relations = await self._raw_schema()
            in_flight_step = (
                persisted_plan.get("inFlightStep") if isinstance(persisted_plan, dict) else None
            )
            if (
                next_mutation is None
                or in_flight_step != next_mutation.step.key
                or not _is_satisfied(next_mutation, fields, relations)
            ):
                raise RelationSchemaError("schema changed after preview", code="schema_mismatch")
        if not existing:
            await self._create_operation(params.operation_id, stored, token)
            persisted_plan = _serialize_stored_plan(
                stored, last_schema_revision=plan.expected_schema_revision
            )

        applied: list[SchemaChangeStep] = []
        for mutation in stored.mutations:
            key = mutation.step.key
            if key not in applied_keys:
                fields, relations = await self._raw_schema()
                if not _is_satisfied(mutation, fields, relations):
                    _require_owned_mutation(mutation, fields, relations)
                    persisted_plan = _serialize_stored_plan(
                        stored,
                        last_schema_revision=last_revision,
                        in_flight_step=key,
                    )
                    await self._patch_operation(
                        params.operation_id,
                        {"status": "applying", "plan": persisted_plan},
                        token,
                    )
                    await self._transport.request(
                        mutation.method,
                        mutation.path,
                        access_token=token,
                        json_body=mutation.body,
                        headers={"X-Request-ID": f"{params.operation_id}:{key}"},
                        expected_status=(200, 201, 204),
                    )
                applied_keys.add(key)
                live_snapshot = await self._schema_provider(plan.collection)
                last_revision = live_snapshot.schema_revision
                persisted_plan = _serialize_stored_plan(stored, last_schema_revision=last_revision)
                await self._patch_operation(
                    params.operation_id,
                    {
                        "status": "applying",
                        "applied_steps": sorted(applied_keys),
                        "plan": persisted_plan,
                    },
                    token,
                )
            applied.append(mutation.step)

        if plan.action == "delete" and plan.affected_lookup_ids:
            if self._lookup_cascade is None:
                raise RelationSchemaError(
                    "Lookup cascade is not configured", code="lookup_cascade_unavailable"
                )
            await self._lookup_cascade(plan.affected_lookup_ids, params.operation_id)
        await self._patch_operation(params.operation_id, {"status": "complete"}, token)
        snapshot = await self._schema_provider(plan.collection)
        relation = None
        if plan.action != "delete" and stored.expected_relation_id:
            relation = next(
                (
                    item
                    for item in snapshot.normalized_relations
                    if item.relation_id == stored.expected_relation_id
                    or item.field_ref == stored.expected_relation_id
                ),
                None,
            )
            if relation is None:
                raise RelationSchemaError(
                    "completed relation is no longer discoverable",
                    code="relation_reconcile_failed",
                )
            if relation is None:
                raise RelationSchemaError(
                    "Directus applied the plan but the relation was not discoverable",
                    code="relation_reconcile_failed",
                )
        return RelationChangeResult(
            relation=relation,
            deleted=plan.action == "delete",
            schema_revision=snapshot.schema_revision,
            applied_steps=applied,
        )

    async def _result_for_completed_plan(self, stored: _StoredPlan) -> RelationChangeResult:
        plan = stored.public
        snapshot = await self._schema_provider(plan.collection)
        relation = None
        if plan.action != "delete" and stored.expected_relation_id:
            relation = next(
                (
                    item
                    for item in snapshot.normalized_relations
                    if item.relation_id == stored.expected_relation_id
                    or item.field_ref == stored.expected_relation_id
                ),
                None,
            )
        return RelationChangeResult(
            relation=relation,
            deleted=plan.action == "delete",
            schema_revision=snapshot.schema_revision,
            applied_steps=[item.step for item in stored.mutations],
        )

    async def _inputs(
        self, collection: str
    ) -> tuple[SchemaSnapshot, list[dict[str, Any]], list[dict[str, Any]]]:
        snapshot = await self._schema_provider(collection)
        fields, relations = await self._raw_schema()
        return snapshot, fields, relations

    async def _raw_schema(self) -> tuple[list[dict[str, Any]], list[dict[str, Any]]]:
        return await self._client.schema_fields(), await self._client.schema_relations()

    async def _affected_lookups(self, relation_id: str | None) -> list[str]:
        if relation_id is None or self._lookup_provider is None:
            return []
        definitions: list[LookupDefinition] = await self._lookup_provider()
        affected = {
            definition.lookup_id
            for definition in definitions
            if any(step.relation_id == relation_id for step in definition.path)
        }
        changed = True
        while changed:
            before = len(affected)
            affected.update(
                definition.lookup_id
                for definition in definitions
                if affected.intersection(definition.dependencies)
            )
            changed = len(affected) != before
        return sorted(affected)

    def _plan_create(
        self,
        source: str,
        config: _RelationConfig,
        fields: list[dict[str, Any]],
    ) -> list[_Mutation]:
        collections = {str(item.get("collection")) for item in fields}
        if source not in collections:
            raise RelationSchemaError(
                "source collection is unavailable", code="relation_collection_unknown"
            )
        if isinstance(config, CreateM2OConfig):
            target_pk = _primary_key(fields, config.related_collection)
            return [
                _field_create(source, config.field_key, _m2o_field(config, target_pk)),
                _relation_create(
                    source,
                    config.field_key,
                    config.related_collection,
                    one_field=None,
                    on_delete=config.on_delete,
                ),
            ]
        if isinstance(config, CreateO2MConfig):
            source_pk = _primary_key(fields, source)
            alias = _alias_field(config.field_key, "o2m", config)
            return [
                _field_create(source, config.field_key, alias),
                _field_create(
                    config.related_collection,
                    config.related_many_field,
                    _m2o_field(config, source_pk),
                ),
                _relation_create(
                    config.related_collection,
                    config.related_many_field,
                    source,
                    one_field=config.field_key,
                    on_delete=config.on_delete,
                ),
            ]
        if isinstance(config, (CreateM2MConfig, CreateM2AConfig)):
            return _junction_create_mutations(source, config, fields)
        raise RelationSchemaError("unsupported relation kind", code="relation_kind_invalid")

    def _plan_update(
        self,
        current: NormalizedRelationDescriptor,
        config: _RelationConfig,
        fields: list[dict[str, Any]],
    ) -> tuple[list[_Mutation], list[RelationDiagnostic]]:
        if (
            config.kind != current.kind
            or config.preset != current.preset
            or config.field_key != current.field_ref.rsplit(".", 1)[-1]
        ):
            return [], [
                _error(
                    "relation_shape_change_blocked",
                    "cardinality and physical field keys cannot be changed in place",
                )
            ]
        if isinstance(config, CreateM2OConfig):
            if config.related_collection != current.related_collection:
                return [], [
                    _error(
                        "relation_shape_change_blocked",
                        "target collection cannot be changed in place",
                    )
                ]
            mutations = [
                _field_update(
                    current.source_collection,
                    config.field_key,
                    meta={
                        "note": _MANAGED_NOTE,
                        "required": not config.nullable,
                        "display_template": config.display_template,
                        "translations": _field_translations(config.field_display_name),
                    },
                    schema={"is_nullable": config.nullable, "is_unique": config.unique},
                )
            ]
        elif isinstance(config, CreateO2MConfig):
            if (
                config.related_collection != current.related_collection
                or config.related_many_field != current.many_field
            ):
                return [], [
                    _error(
                        "relation_shape_change_blocked",
                        "O2M physical fields cannot be changed in place",
                    )
                ]
            mutations = [
                _field_update(
                    current.source_collection,
                    config.field_key,
                    meta={
                        "note": _MANAGED_NOTE,
                        "display_template": config.display_template,
                        "translations": _field_translations(config.field_display_name),
                    },
                ),
                _field_update(
                    config.related_collection,
                    config.related_many_field,
                    meta={"note": _MANAGED_NOTE, "required": not config.nullable},
                    schema={"is_nullable": config.nullable},
                ),
            ]
        else:
            assert isinstance(config, (CreateM2MConfig, CreateM2AConfig))
            if current.junction is None or config.junction != current.junction:
                return [], [
                    _error(
                        "relation_shape_change_blocked", "junction shape cannot be changed in place"
                    )
                ]
            if (
                isinstance(config, CreateM2MConfig)
                and config.related_collection != current.related_collection
            ):
                return [], [
                    _error(
                        "relation_shape_change_blocked",
                        "target collection cannot be changed in place",
                    )
                ]
            if isinstance(config, CreateM2AConfig) and sorted(config.allowed_collections) != sorted(
                current.allowed_collections
            ):
                return [], [
                    _error(
                        "relation_shape_change_blocked",
                        "M2A target collections cannot be changed in place",
                    )
                ]
            context_by_name = {item.field: item for item in config.junction_context_fields}
            if set(context_by_name) != set(current.junction.context_fields):
                return [], [
                    _error(
                        "relation_shape_change_blocked",
                        "junction context fields cannot be added or removed in place",
                    )
                ]
            for name, context in context_by_name.items():
                raw = next(
                    (
                        item
                        for item in fields
                        if item.get("collection") == current.junction.collection
                        and item.get("field") == name
                    ),
                    None,
                )
                if raw is None or raw.get("type") != context.type:
                    return [], [
                        _error(
                            "relation_shape_change_blocked",
                            "junction context field types cannot be changed in place",
                        )
                    ]
            mutations = [
                _field_update(
                    current.source_collection,
                    config.field_key,
                    meta={
                        "note": _MANAGED_NOTE,
                        "display_template": config.display_template,
                        "translations": _field_translations(config.field_display_name),
                    },
                )
            ]
            mutations.extend(
                _field_update(
                    current.junction.collection,
                    context.field,
                    meta={"note": _MANAGED_NOTE, "required": not context.nullable},
                    schema={
                        "is_nullable": context.nullable,
                        "default_value": context.default_value,
                    },
                )
                for context in config.junction_context_fields
            )
        relation_collection = (
            current.source_collection
            if current.kind == "m2o"
            else current.junction.collection
            if current.junction
            else current.related_collection
        )
        relation_field = current.many_field or (
            current.junction.source_field if current.junction else None
        )
        if relation_collection and relation_field:
            mutations.append(
                _relation_update(relation_collection, relation_field, config.on_delete)
            )
        if current.kind == "m2m" and current.junction is not None:
            mutations.append(
                _relation_update(
                    current.junction.collection,
                    current.junction.target_field,
                    config.on_delete,
                )
            )
        return mutations, []

    def _plan_delete(self, current: NormalizedRelationDescriptor) -> list[_Mutation]:
        field = current.field_ref.rsplit(".", 1)[-1]
        relation_collection = (
            current.source_collection
            if current.kind == "m2o"
            else (current.junction.collection if current.junction else current.related_collection)
        )
        relation_field = current.many_field or (
            current.junction.source_field if current.junction else field
        )
        output: list[_Mutation] = []
        if relation_collection and relation_field:
            output.append(_relation_delete(relation_collection, relation_field))
        output.append(_field_delete(current.source_collection, field))
        if current.kind == "o2m" and current.related_collection and current.many_field:
            output.append(_field_delete(current.related_collection, current.many_field))
        if current.kind in {"m2m", "m2a"} and current.junction:
            output.append(_collection_delete(current.junction.collection))
        return output

    async def _read_operation(self, operation_id: str, token: str) -> dict[str, Any] | None:
        payload = await self._transport.request(
            "GET",
            "/items/vibetable_schema_operations",
            access_token=token,
            query={"filter": {"operation_id": {"_eq": operation_id}}, "limit": 1},
        )
        data = payload.get("data") if isinstance(payload, dict) else None
        return data[0] if isinstance(data, list) and data and isinstance(data[0], dict) else None

    async def _create_operation(self, operation_id: str, stored: _StoredPlan, token: str) -> None:
        await self._transport.request(
            "POST",
            "/items/vibetable_schema_operations",
            access_token=token,
            headers={"X-Request-ID": operation_id},
            json_body={
                "operation_id": operation_id,
                "plan_id": stored.public.plan_id,
                "status": "applying",
                "plan": _serialize_stored_plan(
                    stored,
                    last_schema_revision=stored.public.expected_schema_revision,
                ),
                "applied_steps": [],
            },
            expected_status=(200, 201),
        )

    async def _patch_operation(self, operation_id: str, values: dict[str, Any], token: str) -> None:
        await self._transport.request(
            "PATCH",
            "/items/vibetable_schema_operations",
            access_token=token,
            query={"filter": {"operation_id": {"_eq": operation_id}}},
            json_body=values,
        )


def _field_create(collection: str, field: str, body: dict[str, Any]) -> _Mutation:
    body = {**body, "field": field}
    return _Mutation(
        SchemaChangeStep(resource="field", action="create", key=f"field:{collection}.{field}"),
        "POST",
        f"/fields/{_q(collection)}",
        body,
        ("field", collection, field),
    )


def _field_delete(collection: str, field: str) -> _Mutation:
    return _Mutation(
        SchemaChangeStep(
            resource="field", action="delete", key=f"field:{collection}.{field}", destructive=True
        ),
        "DELETE",
        f"/fields/{_q(collection)}/{_q(field)}",
        None,
        ("field", collection, field),
    )


def _field_update(
    collection: str,
    field: str,
    *,
    meta: dict[str, Any],
    schema: dict[str, Any] | None = None,
) -> _Mutation:
    body: dict[str, Any] = {"meta": meta}
    if schema is not None:
        body["schema"] = schema
    return _Mutation(
        SchemaChangeStep(resource="field", action="update", key=f"field:{collection}.{field}"),
        "PATCH",
        f"/fields/{_q(collection)}/{_q(field)}",
        body,
        ("field", collection, field),
    )


def _collection_delete(collection: str) -> _Mutation:
    return _Mutation(
        SchemaChangeStep(
            resource="collection", action="delete", key=f"collection:{collection}", destructive=True
        ),
        "DELETE",
        f"/collections/{_q(collection)}",
        None,
        ("collection", collection, ""),
    )


def _relation_create(
    collection: str,
    field: str,
    related: str,
    *,
    one_field: str | None,
    on_delete: str,
    junction_field: str | None = None,
    one_collection_field: str | None = None,
    allowed: list[str] | None = None,
) -> _Mutation:
    meta: dict[str, Any] = {
        "many_collection": collection,
        "many_field": field,
        "one_collection": related,
        "one_field": one_field,
        "junction_field": junction_field,
    }
    if one_collection_field:
        meta["one_collection_field"] = one_collection_field
        meta["one_allowed_collections"] = allowed or []
    body = {
        "collection": collection,
        "field": field,
        "related_collection": related,
        "meta": meta,
        "schema": {"on_delete": _delete_action(on_delete)},
    }
    return _Mutation(
        SchemaChangeStep(
            resource="relation", action="create", key=f"relation:{collection}.{field}"
        ),
        "POST",
        "/relations",
        body,
        ("relation", collection, field),
    )


def _relation_delete(collection: str, field: str) -> _Mutation:
    return _Mutation(
        SchemaChangeStep(
            resource="relation",
            action="delete",
            key=f"relation:{collection}.{field}",
            destructive=True,
        ),
        "DELETE",
        f"/relations/{_q(collection)}/{_q(field)}",
        None,
        ("relation", collection, field),
    )


def _relation_update(collection: str, field: str, on_delete: str) -> _Mutation:
    return _Mutation(
        SchemaChangeStep(
            resource="relation", action="update", key=f"relation:{collection}.{field}"
        ),
        "PATCH",
        f"/relations/{_q(collection)}/{_q(field)}",
        {"schema": {"on_delete": _delete_action(on_delete)}},
        ("relation", collection, field),
    )


def _m2o_field(config: _RelationConfig, target_pk: dict[str, Any]) -> dict[str, Any]:
    field = config.related_many_field if isinstance(config, CreateO2MConfig) else config.field_key
    unique = config.unique if isinstance(config, CreateM2OConfig) else False
    file_preset = isinstance(config, CreateM2OConfig) and config.preset == "file"
    return {
        "field": field,
        "type": target_pk.get("type", "uuid"),
        "meta": {
            "special": ["file"] if file_preset else ["m2o"],
            "interface": "file-image" if file_preset else "select-dropdown-m2o",
            "note": _MANAGED_NOTE,
            "required": not config.nullable,
            "display_template": config.display_template,
            "translations": _field_translations(config.field_display_name),
        },
        "schema": {
            "data_type": _schema(target_pk).get("data_type"),
            "is_nullable": config.nullable,
            "is_unique": unique,
        },
    }


def _field_translations(display_name: str) -> list[dict[str, str]]:
    return [{"language": "zh-CN", "translation": display_name}]


def _alias_field(
    field: str,
    special: str,
    config: CreateO2MConfig | CreateM2MConfig | CreateM2AConfig,
) -> dict[str, Any]:
    effective_special = (
        "translations"
        if isinstance(config, CreateO2MConfig) and config.preset == "translations"
        else "files"
        if isinstance(config, CreateM2MConfig) and config.preset == "files"
        else special
    )
    interface = {
        "o2m": "list-o2m",
        "m2m": "list-m2m",
        "m2a": "list-m2a",
        "files": "files",
        "translations": "translations",
    }[effective_special]
    return {
        "field": field,
        "type": "alias",
        "meta": {
            "special": [effective_special],
            "interface": interface,
            "note": _MANAGED_NOTE,
            "display_template": config.display_template,
            "translations": _field_translations(config.field_display_name),
        },
        "schema": None,
    }


def _junction_create_mutations(
    source: str,
    config: CreateM2MConfig | CreateM2AConfig,
    fields: list[dict[str, Any]],
) -> list[_Mutation]:
    junction = config.junction
    source_pk = _primary_key(fields, source)
    target_pk = (
        None
        if isinstance(config, CreateM2AConfig)
        else _primary_key(fields, config.related_collection)
    )
    output = [
        _Mutation(
            SchemaChangeStep(
                resource="collection", action="create", key=f"collection:{junction.collection}"
            ),
            "POST",
            "/collections",
            {
                "collection": junction.collection,
                "meta": {"hidden": True, "note": _MANAGED_NOTE},
                "schema": {},
                "fields": [
                    {
                        "field": "id",
                        "type": "uuid",
                        "meta": {
                            "hidden": True,
                            "special": ["uuid"],
                            "note": _MANAGED_NOTE,
                        },
                        "schema": {"is_primary_key": True},
                    }
                ],
            },
            ("collection", junction.collection, ""),
        ),
        _field_create(junction.collection, junction.source_field, _m2o_field(config, source_pk)),
        _field_create(
            source, config.field_key, _alias_field(config.field_key, config.kind, config)
        ),
    ]
    if isinstance(config, CreateM2AConfig):
        if not junction.collection_field:
            raise RelationSchemaError(
                "M2A requires collectionField", code="m2a_collection_field_missing"
            )
        output.extend(
            [
                _field_create(
                    junction.collection,
                    junction.collection_field,
                    {
                        "field": junction.collection_field,
                        "type": "string",
                        "meta": {"note": _MANAGED_NOTE},
                        "schema": {"is_nullable": False},
                    },
                ),
                _field_create(
                    junction.collection,
                    junction.target_field,
                    {
                        "field": junction.target_field,
                        "type": "string",
                        "meta": {"note": _MANAGED_NOTE},
                        "schema": {"is_nullable": False},
                    },
                ),
            ]
        )
    else:
        assert target_pk is not None
        output.append(
            _field_create(junction.collection, junction.target_field, _m2o_field(config, target_pk))
        )
    for context in config.junction_context_fields:
        if context.field not in junction.context_fields:
            raise RelationSchemaError(
                "typed junction field is not declared in contextFields",
                code="junction_context_invalid",
            )
        output.append(
            _field_create(
                junction.collection,
                context.field,
                {
                    "field": context.field,
                    "type": context.type,
                    "meta": {"note": _MANAGED_NOTE, "required": not context.nullable},
                    "schema": {
                        "is_nullable": context.nullable,
                        "default_value": context.default_value,
                    },
                },
            )
        )
    output.append(
        _relation_create(
            junction.collection,
            junction.source_field,
            source,
            one_field=config.field_key,
            on_delete=config.on_delete,
            junction_field=junction.target_field,
            one_collection_field=(
                junction.collection_field if isinstance(config, CreateM2AConfig) else None
            ),
            allowed=(config.allowed_collections if isinstance(config, CreateM2AConfig) else None),
        )
    )
    if isinstance(config, CreateM2MConfig):
        output.append(
            _relation_create(
                junction.collection,
                junction.target_field,
                config.related_collection,
                one_field=None,
                on_delete=config.on_delete,
                junction_field=junction.source_field,
            )
        )
    return output


def _primary_key(fields: list[dict[str, Any]], collection: str) -> dict[str, Any]:
    matches = [
        item
        for item in fields
        if item.get("collection") == collection and _schema(item).get("is_primary_key") is True
    ]
    if len(matches) != 1:
        raise RelationSchemaError(
            f"collection {collection!r} has no unambiguous visible primary key",
            code="relation_target_schema_invalid",
        )
    return matches[0]


def _serialize_stored_plan(
    stored: _StoredPlan,
    *,
    last_schema_revision: str,
    in_flight_step: str | None = None,
) -> dict[str, Any]:
    return {
        "contract": "vibetable.relation-change-journal.v1",
        "public": stored.public.model_dump(mode="json", by_alias=True),
        "expectedRelationId": stored.expected_relation_id,
        "lastSchemaRevision": last_schema_revision,
        "inFlightStep": in_flight_step,
        "mutations": [
            {
                "step": item.step.model_dump(mode="json", by_alias=True),
                "method": item.method,
                "path": item.path,
                "body": item.body,
                "identity": list(item.identity),
            }
            for item in stored.mutations
        ],
    }


def _restore_stored_plan(raw: Any) -> _StoredPlan | None:
    if not isinstance(raw, dict) or raw.get("contract") != "vibetable.relation-change-journal.v1":
        return None
    public_raw = raw.get("public")
    mutations_raw = raw.get("mutations")
    if not isinstance(public_raw, dict) or not isinstance(mutations_raw, list):
        return None
    try:
        public = RelationChangePlan.model_validate(public_raw)
        mutations: list[_Mutation] = []
        for item in mutations_raw:
            if not isinstance(item, dict):
                return None
            identity = item.get("identity")
            if (
                not isinstance(identity, list)
                or len(identity) != 3
                or not all(isinstance(value, str) for value in identity)
            ):
                return None
            body = item.get("body")
            if body is not None and not isinstance(body, dict):
                return None
            method = item.get("method")
            path = item.get("path")
            if not isinstance(method, str) or not isinstance(path, str):
                return None
            mutations.append(
                _Mutation(
                    step=SchemaChangeStep.model_validate(item.get("step")),
                    method=method,
                    path=path,
                    body=body,
                    identity=(identity[0], identity[1], identity[2]),
                )
            )
        expected_relation_id = raw.get("expectedRelationId")
        if expected_relation_id is not None and not isinstance(expected_relation_id, str):
            return None
        return _StoredPlan(public, tuple(mutations), expected_relation_id)
    except (TypeError, ValueError):
        return None


def _is_satisfied(
    mutation: _Mutation,
    fields: list[dict[str, Any]],
    relations: list[dict[str, Any]],
) -> bool:
    resource, collection, field = mutation.identity
    current: dict[str, Any] | None = None
    if resource == "field":
        current = next(
            (
                item
                for item in fields
                if item.get("collection") == collection and item.get("field") == field
            ),
            None,
        )
    elif resource == "relation":
        current = next(
            (
                item
                for item in relations
                if (item.get("collection") == collection and item.get("field") == field)
                or (
                    isinstance(item.get("meta"), dict)
                    and item["meta"].get("many_collection") == collection
                    and item["meta"].get("many_field") == field
                )
            ),
            None,
        )
    else:
        current = next(
            (item for item in fields if item.get("collection") == collection),
            None,
        )
    if mutation.step.action == "delete":
        return current is None
    if resource == "collection" and mutation.step.action == "create":
        expected_fields = mutation.body.get("fields") if mutation.body else None
        if not isinstance(expected_fields, list):
            return current is not None
        collection_fields = [item for item in fields if item.get("collection") == collection]
        return all(
            isinstance(expected, dict)
            and any(_mapping_contains(item, expected) for item in collection_fields)
            for expected in expected_fields
        )
    if current is None:
        return False
    if mutation.body is None:
        return True
    return _mapping_contains(current, mutation.body)


def _mapping_contains(current: dict[str, Any], expected: dict[str, Any]) -> bool:
    for key, value in expected.items():
        if key in {"field", "collection"}:
            if current.get(key) != value:
                return False
            continue
        if value is None:
            continue
        current_value = current.get(key)
        if isinstance(value, dict):
            if not isinstance(current_value, dict) or not _mapping_contains(current_value, value):
                return False
        elif current_value != value:
            return False
    return True


def _require_owned_mutation(
    mutation: _Mutation,
    fields: list[dict[str, Any]],
    relations: list[dict[str, Any]],
) -> None:
    resource, collection, field = mutation.identity
    if mutation.step.action == "create":
        exists = (
            any(
                item.get("collection") == collection and item.get("field") == field
                for item in fields
            )
            if resource == "field"
            else any(
                (item.get("collection") == collection and item.get("field") == field)
                or (
                    isinstance(item.get("meta"), dict)
                    and item["meta"].get("many_collection") == collection
                    and item["meta"].get("many_field") == field
                )
                for item in relations
            )
            if resource == "relation"
            else any(item.get("collection") == collection for item in fields)
        )
        if not exists:
            return
        raise RelationSchemaError(
            "schema resource was replaced after preview",
            code="schema_mismatch",
        )
    if resource in {"field", "relation"}:
        candidate = next(
            (
                item
                for item in fields
                if item.get("collection") == collection and item.get("field") == field
            ),
            None,
        )
        if candidate is not None and _managed_field(candidate):
            return
    elif resource == "collection":
        owned_fields = [item for item in fields if item.get("collection") == collection]
        if owned_fields and any(_managed_field(item) for item in owned_fields):
            return
    raise RelationSchemaError(
        "schema resource is no longer owned by VibeTable",
        code="relation_ownership_mismatch",
    )


def _managed_field(field: dict[str, Any]) -> bool:
    meta = field.get("meta")
    return isinstance(meta, dict) and _MANAGED_NOTE in str(meta.get("note") or "")


def _schema(field: dict[str, Any]) -> dict[str, Any]:
    value = field.get("schema")
    return value if isinstance(value, dict) else {}


def _delete_action(value: str) -> str:
    return {"cascade": "CASCADE", "nullify": "SET NULL", "restrict": "RESTRICT"}[value]


def _error(
    code: str,
    message: str,
    *,
    severity: Literal["warning", "error"] = "error",
) -> RelationDiagnostic:
    return RelationDiagnostic(code=code, message=message, severity=severity)


def _plan_id(params: PreviewRelationChangeParams, steps: list[SchemaChangeStep]) -> str:
    payload = {
        "params": params.model_dump(mode="json", by_alias=True),
        "steps": [item.model_dump(mode="json", by_alias=True) for item in steps],
    }
    encoded = json.dumps(payload, sort_keys=True, separators=(",", ":"), ensure_ascii=False)
    return hashlib.sha256(encoded.encode()).hexdigest()


def _q(value: str) -> str:
    if not _IDENTIFIER.fullmatch(value):
        raise RelationSchemaError("invalid Directus identifier", code="relation_identifier_invalid")
    return quote(value, safe="")


__all__ = ["RelationSchemaError", "RelationSchemaService"]
