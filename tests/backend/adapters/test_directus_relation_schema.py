from __future__ import annotations

from typing import Any

from backend.adapters.directus.relation_schema import normalize_directus_relations


def _field(
    collection: str,
    field: str,
    *,
    special: list[str] | None = None,
    schema: dict[str, Any] | None = None,
    meta: dict[str, Any] | None = None,
) -> dict[str, Any]:
    field_meta = dict(meta or {})
    if special is not None:
        field_meta["special"] = special
    return {
        "collection": collection,
        "field": field,
        "meta": field_meta,
        "schema": schema,
    }


def _relation(
    many_collection: str,
    many_field: str,
    one_collection: str | None,
    *,
    one_field: str | None = None,
    junction_field: str | None = None,
    collection_field: str | None = None,
    allowed_collections: list[str] | None = None,
    sort_field: str | None = None,
    on_delete: str = "SET NULL",
    meta_id: int | str | None = None,
) -> dict[str, Any]:
    return {
        "collection": many_collection,
        "field": many_field,
        "related_collection": one_collection,
        "schema": {"on_delete": on_delete},
        "meta": {
            "id": meta_id,
            "many_collection": many_collection,
            "many_field": many_field,
            "one_collection": one_collection,
            "one_field": one_field,
            "junction_field": junction_field,
            "one_collection_field": collection_field,
            "one_allowed_collections": allowed_collections,
            "sort_field": sort_field,
            "one_deselect_action": "nullify",
        },
    }


def test_normalizes_m2o_and_o2m_alias_with_stable_field_ids() -> None:
    result = normalize_directus_relations(
        fields=[
            _field("orders", "id", schema={"is_primary_key": True}),
            _field(
                "orders",
                "contract",
                special=["m2o"],
                schema={"is_nullable": False, "is_unique": True},
            ),
            _field("contracts", "id", schema={"is_primary_key": True}),
            _field("contracts", "orders", special=["o2m"], schema=None),
            _field("contracts", "name", schema={"is_nullable": False}),
        ],
        relations=[
            _relation(
                "orders",
                "contract",
                "contracts",
                one_field="orders",
                on_delete="RESTRICT",
            )
        ],
    )

    assert [relation.relation_id for relation in result.relations] == [
        "contracts.orders",
        "orders.contract",
    ]
    reverse, forward = result.relations
    assert (reverse.field_ref, reverse.source_collection, reverse.kind) == (
        "contracts.orders",
        "contracts",
        "o2m",
    )
    assert reverse.related_collection == "orders"
    assert reverse.many_field == "contract"
    assert reverse.one_field == "orders"
    assert reverse.unique is False
    assert reverse.display_template is None
    assert reverse.state == "valid"
    assert forward.field_ref == "orders.contract"
    assert forward.related_collection == "contracts"
    assert forward.many_field == "contract"
    assert forward.one_field is None
    assert forward.kind == "m2o"
    assert forward.unique is True
    assert forward.nullable is False
    assert forward.on_delete == "restrict"
    assert forward.display_template is None
    assert forward.state == "valid"
    assert result.diagnostics == []


def test_marks_file_preset_and_self_relation_without_guessing_display_fields() -> None:
    result = normalize_directus_relations(
        fields=[
            _field("documents", "cover", special=["file"], schema={"is_nullable": True}),
            _field("directus_files", "filename_download", schema={"is_nullable": False}),
            _field("categories", "parent", special=["m2o"], schema={"is_nullable": True}),
            _field("categories", "children", special=["o2m"], schema=None),
        ],
        relations=[
            _relation("documents", "cover", "directus_files"),
            _relation("categories", "parent", "categories", one_field="children"),
        ],
    )

    by_id = {relation.relation_id: relation for relation in result.relations}
    assert by_id["documents.cover"].preset == "file"
    assert by_id["documents.cover"].display_template is None
    assert by_id["categories.parent"].self_relation is True
    assert by_id["categories.children"].self_relation is True


def test_normalizes_m2m_junction_and_contextual_fields() -> None:
    result = normalize_directus_relations(
        fields=[
            _field("articles", "tags", special=["m2m"], schema=None),
            _field("article_tags", "id", schema={"is_primary_key": True}),
            _field("article_tags", "article", special=["m2o"], schema={}),
            _field("article_tags", "tag", special=["m2o"], schema={}),
            _field("article_tags", "sort", schema={}),
            _field("article_tags", "quantity", schema={}),
            _field("article_tags", "note", schema={}),
            _field("tags", "id", schema={"is_primary_key": True}),
        ],
        relations=[
            _relation(
                "article_tags",
                "article",
                "articles",
                one_field="tags",
                junction_field="tag",
                sort_field="sort",
                on_delete="CASCADE",
            ),
            _relation(
                "article_tags",
                "tag",
                "tags",
                junction_field="article",
                on_delete="CASCADE",
            ),
        ],
    )

    assert [relation.relation_id for relation in result.relations] == ["articles.tags"]
    relation = result.relations[0]
    assert relation.kind == "m2m"
    assert relation.related_collection == "tags"
    assert relation.one_field == "tags"
    assert relation.on_delete == "cascade"
    assert relation.preset == "standard"
    assert relation.state == "valid"
    assert relation.junction is not None
    assert relation.junction.model_dump() == {
        "collection": "article_tags",
        "source_field": "article",
        "target_field": "tag",
        "collection_field": None,
        "sort_field": "sort",
        "context_fields": ["note", "quantity"],
    }


def test_normalizes_m2a_polymorphic_junction() -> None:
    result = normalize_directus_relations(
        fields=[
            _field("pages", "sections", special=["m2a"], schema=None),
            _field("page_sections", "id", schema={"is_primary_key": True}),
            _field("page_sections", "page", special=["m2o"], schema={}),
            _field("page_sections", "collection", schema={}),
            _field("page_sections", "item", schema={}),
            _field("page_sections", "sort", schema={}),
            _field("page_sections", "caption", schema={}),
        ],
        relations=[
            _relation(
                "page_sections",
                "page",
                "pages",
                one_field="sections",
                junction_field="item",
                collection_field="collection",
                allowed_collections=["videos", "headings"],
                sort_field="sort",
                on_delete="CASCADE",
            )
        ],
    )

    assert len(result.relations) == 1
    relation = result.relations[0]
    assert relation.relation_id == "pages.sections"
    assert relation.kind == "m2a"
    assert relation.related_collection is None
    assert relation.allowed_collections == ["headings", "videos"]
    assert relation.one_field == "sections"
    assert relation.state == "valid"
    assert relation.junction is not None
    assert relation.junction.model_dump() == {
        "collection": "page_sections",
        "source_field": "page",
        "target_field": "item",
        "collection_field": "collection",
        "sort_field": "sort",
        "context_fields": ["caption"],
    }


def test_normalizes_translations_interface_as_o2m_preset() -> None:
    result = normalize_directus_relations(
        fields=[
            _field(
                "articles",
                "translations",
                special=["o2m"],
                schema=None,
                meta={"interface": "translations"},
            ),
            _field("article_translations", "id", schema={"is_primary_key": True}),
            _field("article_translations", "article", special=["m2o"], schema={}),
            _field("article_translations", "language", special=["m2o"], schema={}),
            _field("article_translations", "title", schema={}),
        ],
        relations=[
            _relation(
                "article_translations",
                "article",
                "articles",
                one_field="translations",
                junction_field="language",
                on_delete="CASCADE",
            ),
            _relation(
                "article_translations",
                "language",
                "directus_languages",
                junction_field="article",
                on_delete="CASCADE",
            ),
        ],
    )

    assert len(result.relations) == 1
    relation = result.relations[0]
    assert relation.relation_id == "articles.translations"
    assert relation.kind == "o2m"
    assert relation.related_collection == "article_translations"
    assert relation.preset == "translations"
    assert relation.junction is None
    assert relation.state == "valid"


def test_missing_many_side_field_is_preserved_as_readonly_with_diagnostic() -> None:
    result = normalize_directus_relations(
        fields=[_field("contracts", "id", schema={"is_primary_key": True})],
        relations=[_relation("orders", "contract", "contracts")],
    )

    assert len(result.relations) == 1
    relation = result.relations[0]
    assert relation.relation_id == "orders.contract"
    assert relation.state == "readonly"
    assert [(item.code, item.severity) for item in relation.diagnostics] == [
        ("relation_field_metadata_missing", "warning")
    ]


def test_missing_m2m_junction_pair_is_invalid_instead_of_silently_dropped() -> None:
    result = normalize_directus_relations(
        fields=[
            _field("articles", "tags", special=["m2m"], schema=None),
            _field("article_tags", "article", special=["m2o"], schema={}),
            _field("article_tags", "tag", special=["m2o"], schema={}),
        ],
        relations=[
            _relation(
                "article_tags",
                "article",
                "articles",
                one_field="tags",
                junction_field="tag",
                on_delete="CASCADE",
            )
        ],
    )

    assert len(result.relations) == 1
    relation = result.relations[0]
    assert relation.relation_id == "articles.tags"
    assert relation.kind == "m2m"
    assert relation.related_collection is None
    assert relation.state == "invalid"
    assert [item.code for item in relation.diagnostics] == ["junction_relation_missing"]
    assert relation.junction is not None
    assert relation.junction.source_field == "article"
    assert relation.junction.target_field == "tag"


def test_incomplete_m2a_remains_m2a_and_reports_missing_polymorphic_metadata() -> None:
    result = normalize_directus_relations(
        fields=[
            _field("pages", "sections", special=["m2a"], schema=None),
            _field("page_sections", "page", special=["m2o"], schema={}),
            _field("page_sections", "item", schema={}),
        ],
        relations=[
            _relation(
                "page_sections",
                "page",
                "pages",
                one_field="sections",
                junction_field="item",
                collection_field=None,
                allowed_collections=None,
            )
        ],
    )

    assert len(result.relations) == 1
    relation = result.relations[0]
    assert relation.kind == "m2a"
    assert relation.state == "invalid"
    assert [item.code for item in relation.diagnostics] == [
        "m2a_collection_field_missing",
        "m2a_allowed_collections_missing",
    ]
    assert relation.junction is not None
    assert relation.junction.collection_field is None
    assert [item.code for item in result.diagnostics] == [
        "m2a_collection_field_missing",
        "m2a_allowed_collections_missing",
    ]


def test_relation_id_prefers_directus_meta_id_and_survives_field_rename() -> None:
    original = normalize_directus_relations(
        fields=[_field("orders", "contract", special=["m2o"], schema={})],
        relations=[_relation("orders", "contract", "contracts", meta_id=42)],
    )
    renamed = normalize_directus_relations(
        fields=[_field("orders", "agreement", special=["m2o"], schema={})],
        relations=[_relation("orders", "agreement", "contracts", meta_id=42)],
    )
    without_meta_id = normalize_directus_relations(
        fields=[_field("orders", "contract", special=["m2o"], schema={})],
        relations=[_relation("orders", "contract", "contracts")],
    )

    assert original.relations[0].relation_id == "directus:42:m2o"
    assert renamed.relations[0].relation_id == "directus:42:m2o"
    assert original.relations[0].field_ref == "orders.contract"
    assert renamed.relations[0].field_ref == "orders.agreement"
    assert original.schema_revision != renamed.schema_revision
    assert without_meta_id.relations[0].relation_id == "orders.contract"


def test_preserves_only_explicit_directus_display_templates() -> None:
    result = normalize_directus_relations(
        fields=[
            _field(
                "orders",
                "contract",
                special=["m2o"],
                schema={},
                meta={"display_template": "{{number}}"},
            ),
            _field(
                "contracts",
                "orders",
                special=["o2m"],
                schema=None,
                meta={"template": "{{id}}"},
            ),
            _field("contracts", "name", schema={}),
            _field("contracts", "title", schema={}),
        ],
        relations=[_relation("orders", "contract", "contracts", one_field="orders")],
    )

    by_field = {relation.field_ref: relation for relation in result.relations}
    assert by_field["orders.contract"].display_template == "{{number}}"
    assert by_field["contracts.orders"].display_template == "{{id}}"


def test_missing_one_side_alias_metadata_keeps_invalid_reverse_descriptor() -> None:
    result = normalize_directus_relations(
        fields=[_field("orders", "contract", special=["m2o"], schema={})],
        relations=[_relation("orders", "contract", "contracts", one_field="orders")],
    )

    by_field = {relation.field_ref: relation for relation in result.relations}
    assert by_field["orders.contract"].state == "valid"
    reverse = by_field["contracts.orders"]
    assert reverse.state == "invalid"
    assert [item.code for item in reverse.diagnostics] == ["relation_alias_metadata_missing"]
    assert [item.code for item in result.diagnostics] == ["relation_alias_metadata_missing"]


def test_missing_junction_field_metadata_marks_m2m_invalid() -> None:
    result = normalize_directus_relations(
        fields=[
            _field("articles", "tags", special=["m2m"], schema=None),
            _field("article_tags", "article", special=["m2o"], schema={}),
        ],
        relations=[
            _relation(
                "article_tags",
                "article",
                "articles",
                one_field="tags",
                junction_field="tag",
            ),
            _relation("article_tags", "tag", "tags", junction_field="article"),
        ],
    )

    assert len(result.relations) == 1
    relation = result.relations[0]
    assert relation.state == "invalid"
    assert [item.code for item in relation.diagnostics] == [
        "junction_target_field_metadata_missing"
    ]


def test_missing_related_collection_keeps_locatable_m2o_invalid() -> None:
    result = normalize_directus_relations(
        fields=[_field("orders", "contract", special=["m2o"], schema={})],
        relations=[_relation("orders", "contract", None)],
    )

    assert len(result.relations) == 1
    relation = result.relations[0]
    assert relation.field_ref == "orders.contract"
    assert relation.kind == "m2o"
    assert relation.related_collection is None
    assert relation.state == "invalid"
    assert [item.code for item in relation.diagnostics] == ["related_collection_missing"]
    assert [item.code for item in result.diagnostics] == ["related_collection_missing"]


def test_declared_m2m_without_junction_field_stays_invalid_m2m() -> None:
    result = normalize_directus_relations(
        fields=[
            _field("articles", "tags", special=["m2m"], schema=None),
            _field("article_tags", "article", special=["m2o"], schema={}),
        ],
        relations=[
            _relation(
                "article_tags",
                "article",
                "articles",
                one_field="tags",
                junction_field=None,
            )
        ],
    )

    assert len(result.relations) == 1
    relation = result.relations[0]
    assert relation.field_ref == "articles.tags"
    assert relation.kind == "m2m"
    assert relation.junction is None
    assert relation.state == "invalid"
    assert [item.code for item in relation.diagnostics] == ["junction_field_missing"]


def test_multiple_directus_files_relation_uses_files_preset() -> None:
    result = normalize_directus_relations(
        fields=[
            _field("documents", "attachments", special=["m2m"], schema=None),
            _field("document_files", "document", special=["m2o"], schema={}),
            _field("document_files", "file", special=["m2o"], schema={}),
        ],
        relations=[
            _relation(
                "document_files",
                "document",
                "documents",
                one_field="attachments",
                junction_field="file",
            ),
            _relation(
                "document_files",
                "file",
                "directus_files",
                junction_field="document",
            ),
        ],
    )

    assert len(result.relations) == 1
    assert result.relations[0].kind == "m2m"
    assert result.relations[0].related_collection == "directus_files"
    assert result.relations[0].preset == "files"


def test_ambiguous_junction_pair_is_retained_as_invalid() -> None:
    result = normalize_directus_relations(
        fields=[
            _field("articles", "tags", special=["m2m"], schema=None),
            _field("article_tags", "article", special=["m2o"], schema={}),
            _field("article_tags", "tag", special=["m2o"], schema={}),
        ],
        relations=[
            _relation(
                "article_tags",
                "article",
                "articles",
                one_field="tags",
                junction_field="tag",
            ),
            _relation("article_tags", "tag", "tags", junction_field="article"),
            _relation("article_tags", "tag", "labels", junction_field="article"),
        ],
    )

    assert len(result.relations) == 1
    relation = result.relations[0]
    assert relation.state == "invalid"
    assert relation.related_collection is None
    assert [item.code for item in relation.diagnostics] == ["junction_relation_ambiguous"]
    assert [item.code for item in result.diagnostics] == ["junction_relation_ambiguous"]


def test_unlocatable_relation_is_reported_at_discovery_level() -> None:
    raw = _relation("orders", "contract", "contracts")
    raw["field"] = None
    raw["meta"]["many_field"] = None

    result = normalize_directus_relations(fields=[], relations=[raw])

    assert result.relations == []
    assert [item.code for item in result.diagnostics] == ["relation_many_field_missing"]
    empty = normalize_directus_relations(fields=[], relations=[])
    assert result.schema_revision != empty.schema_revision


def test_unreferenced_junction_relation_without_alias_is_diagnostic() -> None:
    result = normalize_directus_relations(
        fields=[
            _field("article_tags", "article", special=["m2o"], schema={}),
            _field("article_tags", "tag", special=["m2o"], schema={}),
        ],
        relations=[
            _relation(
                "article_tags",
                "article",
                "articles",
                one_field=None,
                junction_field="tag",
            )
        ],
    )

    assert result.relations == []
    assert [item.code for item in result.diagnostics] == ["relation_alias_missing"]


def test_unknown_delete_action_uses_explicit_readonly_restrict_fallback() -> None:
    result = normalize_directus_relations(
        fields=[_field("orders", "contract", special=["m2o"], schema={})],
        relations=[
            _relation(
                "orders",
                "contract",
                "contracts",
                on_delete="SET DEFAULT",
            )
        ],
    )

    relation = result.relations[0]
    assert relation.on_delete == "restrict"
    assert relation.state == "readonly"
    assert [item.code for item in relation.diagnostics] == ["delete_policy_unsupported"]


def test_missing_relation_meta_uses_top_level_identity_but_is_readonly() -> None:
    raw = _relation("orders", "contract", "contracts")
    raw["meta"] = None

    result = normalize_directus_relations(
        fields=[_field("orders", "contract", special=["m2o"], schema={})],
        relations=[raw],
    )

    relation = result.relations[0]
    assert relation.field_ref == "orders.contract"
    assert relation.related_collection == "contracts"
    assert relation.state == "readonly"
    assert [item.code for item in relation.diagnostics] == ["relation_metadata_missing"]
