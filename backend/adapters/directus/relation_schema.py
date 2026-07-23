"""Normalize permission-filtered Directus field and relation metadata.

The Directus relations endpoint describes physical foreign keys from the
many-side.  VibeTable exposes relations from the field a user sees, so a
single Directus relation can yield both an M2O descriptor and an O2M alias
descriptor.  This module is deliberately pure: it never fetches or repairs
schema and reports incomplete metadata through the relation contracts.
"""

from __future__ import annotations

import hashlib
import json
from collections.abc import Mapping, Sequence
from typing import Any, Literal

from backend.adapters.directus.profile import (
    JunctionProfile,
    RelationDeletePolicy,
    RelationState,
)
from backend.contracts.relation_admin import (
    NormalizedRelationDescriptor,
    RelationDiagnostic,
    RelationDiscoveryResult,
)


def normalize_directus_relations(
    *,
    fields: Sequence[Mapping[str, Any]],
    relations: Sequence[Mapping[str, Any]],
) -> RelationDiscoveryResult:
    """Return stable, user-facing descriptors for Directus schema metadata."""

    field_index = {
        (collection, field): raw
        for raw in fields
        if (collection := _text(raw.get("collection"))) is not None
        and (field := _text(raw.get("field"))) is not None
    }
    relation_index: dict[tuple[str, str], list[Mapping[str, Any]]] = {}
    referenced_junction_relations: set[tuple[str, str]] = set()
    for raw in relations:
        meta = _mapping(raw.get("meta"))
        many_collection = _text(meta.get("many_collection")) or _text(raw.get("collection"))
        many_field = _text(meta.get("many_field")) or _text(raw.get("field"))
        if many_collection is not None and many_field is not None:
            relation_index.setdefault((many_collection, many_field), []).append(raw)
            junction_field = _text(meta.get("junction_field"))
            if junction_field is not None and _text(meta.get("one_field")) is not None:
                referenced_junction_relations.add((many_collection, junction_field))
    normalized: list[NormalizedRelationDescriptor] = []
    discovery_diagnostics: list[RelationDiagnostic] = []

    for raw in relations:
        meta_value = raw.get("meta")
        meta = _mapping(meta_value)
        metadata_diagnostics = (
            []
            if isinstance(meta_value, Mapping)
            else [
                RelationDiagnostic(
                    code="relation_metadata_missing",
                    message="Directus relation meta is unavailable; using top-level identity only",
                    severity="warning",
                )
            ]
        )
        many_collection = _text(meta.get("many_collection")) or _text(raw.get("collection"))
        many_field = _text(meta.get("many_field")) or _text(raw.get("field"))
        one_collection = _text(meta.get("one_collection")) or _text(raw.get("related_collection"))
        if many_collection is None or many_field is None:
            discovery_diagnostics.extend(metadata_diagnostics)
            missing = "source collection" if many_collection is None else "many-side field"
            discovery_diagnostics.append(
                RelationDiagnostic(
                    code=(
                        "relation_source_collection_missing"
                        if many_collection is None
                        else "relation_many_field_missing"
                    ),
                    message=f"Directus relation metadata is missing its {missing}",
                )
            )
            continue

        relation_schema = _mapping(raw.get("schema"))
        delete_policy, delete_diagnostics = _normalize_delete_policy(
            relation_schema.get("on_delete")
        )
        if one_collection is None:
            many_metadata = field_index.get((many_collection, many_field))
            diagnostics = [
                RelationDiagnostic(
                    code="related_collection_missing",
                    message=(f"Relation {many_collection}.{many_field} has no related collection"),
                )
            ]
            diagnostics.extend(metadata_diagnostics)
            diagnostics.extend(delete_diagnostics)
            if many_metadata is None:
                diagnostics.append(
                    RelationDiagnostic(
                        code="relation_field_metadata_missing",
                        message=(
                            f"Field metadata for {many_collection}.{many_field} is unavailable; "
                            "the relation cannot be managed safely"
                        ),
                        severity="warning",
                    )
                )
                many_metadata = {}
            many_schema = _mapping(many_metadata.get("schema"))
            many_meta = _mapping(many_metadata.get("meta"))
            normalized.append(
                NormalizedRelationDescriptor(
                    relation_id=_stable_relation_id(
                        raw, collection=many_collection, field=many_field, perspective="m2o"
                    ),
                    field_ref=_relation_id(many_collection, many_field),
                    source_collection=many_collection,
                    kind="m2o",
                    many_field=many_field,
                    unique=many_schema.get("is_unique") is True,
                    nullable=_nullable(many_schema, many_meta),
                    on_delete=delete_policy,
                    state="invalid",
                    display_template=_explicit_display_template(many_metadata),
                    diagnostics=diagnostics,
                )
            )
            continue

        junction_field = _text(meta.get("junction_field"))
        one_field = _text(meta.get("one_field"))
        alias_metadata = (
            field_index.get((one_collection, one_field), {}) if one_field is not None else {}
        )
        alias_diagnostics = _alias_diagnostics(
            alias_metadata,
            collection=one_collection,
            field=one_field,
        )
        declared_kind = _declared_relation_kind(alias_metadata)
        if one_field is not None and junction_field is None and declared_kind in {"m2m", "m2a"}:
            incomplete_diagnostics = [
                *alias_diagnostics,
                *metadata_diagnostics,
                *delete_diagnostics,
                RelationDiagnostic(
                    code="junction_field_missing",
                    message=(
                        f"Declared {declared_kind.upper()} relation "
                        f"{one_collection}.{one_field} has no junction field"
                    ),
                ),
            ]
            normalized.append(
                NormalizedRelationDescriptor(
                    relation_id=_stable_relation_id(
                        raw,
                        collection=one_collection,
                        field=one_field,
                        perspective=declared_kind,
                    ),
                    field_ref=_relation_id(one_collection, one_field),
                    source_collection=one_collection,
                    kind=declared_kind,
                    one_field=one_field,
                    on_delete=delete_policy,
                    state="invalid",
                    display_template=_explicit_display_template(alias_metadata),
                    diagnostics=incomplete_diagnostics,
                )
            )
            continue
        if one_field is not None and _is_translations_field(alias_metadata):
            translation_diagnostics = [
                *alias_diagnostics,
                *metadata_diagnostics,
                *delete_diagnostics,
            ]
            normalized.append(
                NormalizedRelationDescriptor(
                    relation_id=_stable_relation_id(
                        raw, collection=one_collection, field=one_field, perspective="o2m"
                    ),
                    field_ref=_relation_id(one_collection, one_field),
                    source_collection=one_collection,
                    kind="o2m",
                    related_collection=many_collection,
                    many_field=many_field,
                    one_field=one_field,
                    on_delete=delete_policy,
                    preset="translations",
                    self_relation=many_collection == one_collection,
                    state=_diagnostic_state(translation_diagnostics),
                    display_template=_explicit_display_template(alias_metadata),
                    diagnostics=translation_diagnostics,
                )
            )
            continue
        if junction_field is not None:
            if one_field is None:
                if (many_collection, many_field) not in referenced_junction_relations:
                    discovery_diagnostics.append(
                        RelationDiagnostic(
                            code="relation_alias_missing",
                            message=(
                                f"Junction relation {many_collection}.{many_field} has no "
                                "one-side alias and is not referenced by another relation"
                            ),
                        )
                    )
                continue
            collection_field = _text(meta.get("one_collection_field"))
            if collection_field is not None or _has_special(alias_metadata, "m2a"):
                allowed = meta.get("one_allowed_collections")
                allowed_collections = (
                    sorted({item for item in allowed if isinstance(item, str) and item})
                    if isinstance(allowed, list)
                    else []
                )
                m2a_diagnostics = [
                    *alias_diagnostics,
                    *metadata_diagnostics,
                    *delete_diagnostics,
                ]
                if collection_field is None:
                    m2a_diagnostics.append(
                        RelationDiagnostic(
                            code="m2a_collection_field_missing",
                            message=(
                                f"M2A relation {one_collection}.{one_field} has no "
                                "collection discriminator field"
                            ),
                        )
                    )
                if not allowed_collections:
                    m2a_diagnostics.append(
                        RelationDiagnostic(
                            code="m2a_allowed_collections_missing",
                            message=(
                                f"M2A relation {one_collection}.{one_field} has no "
                                "allowed target collections"
                            ),
                        )
                    )
                sort_field = _text(meta.get("sort_field"))
                m2a_diagnostics.extend(
                    _junction_field_diagnostics(
                        field_index,
                        collection=many_collection,
                        source_field=many_field,
                        target_field=junction_field,
                        collection_field=collection_field,
                        sort_field=sort_field,
                    )
                )
                normalized.append(
                    NormalizedRelationDescriptor(
                        relation_id=_stable_relation_id(
                            raw, collection=one_collection, field=one_field, perspective="m2a"
                        ),
                        field_ref=_relation_id(one_collection, one_field),
                        source_collection=one_collection,
                        kind="m2a",
                        allowed_collections=allowed_collections,
                        one_field=one_field,
                        junction=JunctionProfile(
                            collection=many_collection,
                            source_field=many_field,
                            target_field=junction_field,
                            collection_field=collection_field,
                            sort_field=sort_field,
                            context_fields=_context_fields(
                                fields,
                                collection=many_collection,
                                excluded={
                                    many_field,
                                    junction_field,
                                    collection_field,
                                    sort_field,
                                },
                            ),
                        ),
                        on_delete=delete_policy,
                        state=_diagnostic_state(m2a_diagnostics),
                        display_template=_explicit_display_template(alias_metadata),
                        diagnostics=m2a_diagnostics,
                    )
                )
                continue
            complements = relation_index.get((many_collection, junction_field), [])
            complement = complements[0] if len(complements) == 1 else None
            complement_meta = _mapping(complement.get("meta")) if complement is not None else {}
            target_collection = _text(complement_meta.get("one_collection")) or (
                _text(complement.get("related_collection")) if complement is not None else None
            )
            junction_diagnostics = [
                *alias_diagnostics,
                *metadata_diagnostics,
                *delete_diagnostics,
            ]
            if len(complements) == 0:
                junction_diagnostics.append(
                    RelationDiagnostic(
                        code="junction_relation_missing",
                        message=(
                            f"Junction field {many_collection}.{junction_field} has no "
                            "matching Directus relation"
                        ),
                    )
                )
            elif len(complements) > 1:
                junction_diagnostics.append(
                    RelationDiagnostic(
                        code="junction_relation_ambiguous",
                        message=(
                            f"Junction field {many_collection}.{junction_field} matches "
                            "multiple Directus relations"
                        ),
                    )
                )
            elif target_collection is None:
                junction_diagnostics.append(
                    RelationDiagnostic(
                        code="junction_target_missing",
                        message=(
                            f"Junction relation {many_collection}.{junction_field} has no "
                            "related collection"
                        ),
                    )
                )
            sort_field = _text(meta.get("sort_field"))
            junction_diagnostics.extend(
                _junction_field_diagnostics(
                    field_index,
                    collection=many_collection,
                    source_field=many_field,
                    target_field=junction_field,
                    collection_field=None,
                    sort_field=sort_field,
                )
            )
            junction_state = _diagnostic_state(junction_diagnostics)
            normalized.append(
                NormalizedRelationDescriptor(
                    relation_id=_stable_relation_id(
                        raw, collection=one_collection, field=one_field, perspective="m2m"
                    ),
                    field_ref=_relation_id(one_collection, one_field),
                    source_collection=one_collection,
                    kind="m2m",
                    related_collection=target_collection,
                    one_field=one_field,
                    junction=JunctionProfile(
                        collection=many_collection,
                        source_field=many_field,
                        target_field=junction_field,
                        sort_field=sort_field,
                        context_fields=_context_fields(
                            fields,
                            collection=many_collection,
                            excluded={many_field, junction_field, sort_field},
                        ),
                    ),
                    on_delete=delete_policy,
                    preset="files" if target_collection == "directus_files" else "standard",
                    self_relation=one_collection == target_collection,
                    state=junction_state,
                    display_template=_explicit_display_template(alias_metadata),
                    diagnostics=junction_diagnostics,
                )
            )
            continue

        many_metadata = field_index.get((many_collection, many_field))
        relation_diagnostics = [*metadata_diagnostics, *delete_diagnostics]
        if many_metadata is None:
            relation_diagnostics.append(
                RelationDiagnostic(
                    code="relation_field_metadata_missing",
                    message=(
                        f"Field metadata for {many_collection}.{many_field} is unavailable; "
                        "the relation cannot be managed safely"
                    ),
                    severity="warning",
                )
            )
            many_metadata = {}
        many_schema = _mapping(many_metadata.get("schema"))
        many_meta = _mapping(many_metadata.get("meta"))
        relation_state = _diagnostic_state(relation_diagnostics)
        normalized.append(
            NormalizedRelationDescriptor(
                relation_id=_stable_relation_id(
                    raw, collection=many_collection, field=many_field, perspective="m2o"
                ),
                field_ref=_relation_id(many_collection, many_field),
                source_collection=many_collection,
                kind="m2o",
                related_collection=one_collection,
                many_field=many_field,
                unique=many_schema.get("is_unique") is True,
                nullable=_nullable(many_schema, many_meta),
                on_delete=delete_policy,
                preset="file" if one_collection == "directus_files" else "standard",
                self_relation=many_collection == one_collection,
                state=relation_state,
                display_template=_explicit_display_template(many_metadata),
                diagnostics=relation_diagnostics,
            )
        )

        if one_field is not None:
            reverse_diagnostics = [
                *alias_diagnostics,
                *metadata_diagnostics,
                *delete_diagnostics,
            ]
            normalized.append(
                NormalizedRelationDescriptor(
                    relation_id=_stable_relation_id(
                        raw, collection=one_collection, field=one_field, perspective="o2m"
                    ),
                    field_ref=_relation_id(one_collection, one_field),
                    source_collection=one_collection,
                    kind="o2m",
                    related_collection=many_collection,
                    many_field=many_field,
                    one_field=one_field,
                    on_delete=delete_policy,
                    self_relation=many_collection == one_collection,
                    state=_diagnostic_state(reverse_diagnostics),
                    display_template=_explicit_display_template(alias_metadata),
                    diagnostics=reverse_diagnostics,
                )
            )

    normalized = [
        relation.model_copy(
            update={
                "managed": _is_vibetable_managed(
                    field_index.get(
                        (relation.source_collection, relation.field_ref.rsplit(".", 1)[-1]),
                        {},
                    )
                )
            }
        )
        for relation in normalized
    ]
    normalized.sort(key=lambda relation: relation.relation_id)
    result_diagnostics = discovery_diagnostics + [
        diagnostic for relation in normalized for diagnostic in relation.diagnostics
    ]
    revision_payload = {
        "relations": [relation.model_dump(mode="json", by_alias=True) for relation in normalized],
        "diagnostics": [
            diagnostic.model_dump(mode="json", by_alias=True) for diagnostic in result_diagnostics
        ],
    }
    schema_revision = hashlib.sha256(
        json.dumps(
            revision_payload,
            ensure_ascii=False,
            sort_keys=True,
            separators=(",", ":"),
        ).encode("utf-8")
    ).hexdigest()
    return RelationDiscoveryResult(
        relations=normalized,
        schema_revision=schema_revision,
        diagnostics=result_diagnostics,
    )


def _relation_id(collection: str, field: str) -> str:
    return f"{collection}.{field}"


def _stable_relation_id(
    raw: Mapping[str, Any],
    *,
    collection: str,
    field: str,
    perspective: str,
) -> str:
    meta_id = _mapping(raw.get("meta")).get("id")
    if isinstance(meta_id, (int, str)) and not isinstance(meta_id, bool):
        value = str(meta_id).strip()
        if value:
            return f"directus:{value}:{perspective}"
    return _relation_id(collection, field)


def _nullable(schema: Mapping[str, Any], meta: Mapping[str, Any]) -> bool:
    if meta.get("required") is True:
        return False
    value = schema.get("is_nullable")
    return value if isinstance(value, bool) else True


def _normalize_delete_policy(
    value: Any,
) -> tuple[RelationDeletePolicy, list[RelationDiagnostic]]:
    normalized = str(value or "").strip().upper().replace("_", " ")
    if normalized == "CASCADE":
        return "cascade", []
    if normalized == "SET NULL":
        return "nullify", []
    if normalized in {"RESTRICT", "NO ACTION"}:
        return "restrict", []
    code = "delete_policy_missing" if not normalized else "delete_policy_unsupported"
    detail = "missing" if not normalized else repr(normalized)
    return "restrict", [
        RelationDiagnostic(
            code=code,
            message=f"Directus relation delete policy is {detail}; using safe restrict fallback",
            severity="warning",
        )
    ]


def _diagnostic_state(diagnostics: Sequence[RelationDiagnostic]) -> RelationState:
    if any(diagnostic.severity == "error" for diagnostic in diagnostics):
        return "invalid"
    if diagnostics:
        return "readonly"
    return "valid"


def _context_fields(
    fields: Sequence[Mapping[str, Any]],
    *,
    collection: str,
    excluded: set[str | None],
) -> list[str]:
    names: set[str] = set()
    for raw in fields:
        if _text(raw.get("collection")) != collection:
            continue
        name = _text(raw.get("field"))
        schema = _mapping(raw.get("schema"))
        if name is None or name in excluded or schema.get("is_primary_key") is True:
            continue
        names.add(name)
    return sorted(names)


def _is_translations_field(raw: Mapping[str, Any]) -> bool:
    meta = _mapping(raw.get("meta"))
    special = meta.get("special")
    specials = set(special) if isinstance(special, list) else set()
    return "translations" in specials or meta.get("interface") == "translations"


def _has_special(raw: Mapping[str, Any], expected: str) -> bool:
    meta = _mapping(raw.get("meta"))
    special = meta.get("special")
    return isinstance(special, list) and expected in special


def _declared_relation_kind(raw: Mapping[str, Any]) -> Literal["m2m", "m2a"] | None:
    if _has_special(raw, "m2a"):
        return "m2a"
    if _has_special(raw, "m2m"):
        return "m2m"
    return None


def _explicit_display_template(raw: Mapping[str, Any]) -> str | None:
    meta = _mapping(raw.get("meta"))
    candidates = [
        meta.get("display_template"),
        meta.get("template"),
        _mapping(meta.get("options")).get("template"),
        _mapping(meta.get("display_options")).get("template"),
    ]
    for value in candidates:
        if isinstance(value, str) and value.strip():
            return value.strip()
    return None


def _is_vibetable_managed(raw: Mapping[str, Any]) -> bool:
    note = _mapping(raw.get("meta")).get("note")
    return isinstance(note, str) and "[vibetable-managed-relation]" in note


def _alias_diagnostics(
    raw: Mapping[str, Any],
    *,
    collection: str,
    field: str | None,
) -> list[RelationDiagnostic]:
    if field is None or raw:
        return []
    return [
        RelationDiagnostic(
            code="relation_alias_metadata_missing",
            message=(
                f"Alias field metadata for {collection}.{field} is unavailable; "
                "the relation cannot be used safely"
            ),
        )
    ]


def _junction_field_diagnostics(
    field_index: Mapping[tuple[str, str], Mapping[str, Any]],
    *,
    collection: str,
    source_field: str,
    target_field: str,
    collection_field: str | None,
    sort_field: str | None,
) -> list[RelationDiagnostic]:
    checks = [
        (source_field, "junction_source_field_metadata_missing"),
        (target_field, "junction_target_field_metadata_missing"),
        (collection_field, "junction_collection_field_metadata_missing"),
        (sort_field, "junction_sort_field_metadata_missing"),
    ]
    diagnostics: list[RelationDiagnostic] = []
    for field, code in checks:
        if field is None or (collection, field) in field_index:
            continue
        diagnostics.append(
            RelationDiagnostic(
                code=code,
                message=f"Field metadata for {collection}.{field} is unavailable",
            )
        )
    return diagnostics


def _mapping(value: Any) -> Mapping[str, Any]:
    return value if isinstance(value, Mapping) else {}


def _text(value: Any) -> str | None:
    return value if isinstance(value, str) and value else None


__all__ = ["normalize_directus_relations"]
