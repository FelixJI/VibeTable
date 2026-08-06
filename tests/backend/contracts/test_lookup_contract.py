from __future__ import annotations

import pytest
from pydantic import ValidationError

from backend.contracts.lookup import (
    LookupDefinition,
    LookupValidateParams,
    validate_lookup_dependency_graph,
)
from backend.contracts.relation_admin import NormalizedRelationDescriptor, SchemaSnapshot
from backend.contracts.table import ColumnSchema


def _lookup(
    lookup_id: str,
    *,
    source: dict | None = None,
    dependencies: list[str] | None = None,
) -> LookupDefinition:
    return LookupDefinition.model_validate(
        {
            "lookupId": lookup_id,
            "collection": "orders",
            "fieldKey": f"lookup_{lookup_id}",
            "displayName": lookup_id,
            "path": [{"relationId": "contract"}],
            "source": source or {"kind": "target_field", "fieldRef": "price"},
            "aggregation": "single",
            "outputType": "decimal",
            "outputScale": 2,
            "dependencies": dependencies or [],
        }
    )


def test_lookup_definition_uses_camel_case_wire_shape() -> None:
    dumped = _lookup("contract_price").model_dump(by_alias=True, mode="json")

    assert dumped["lookupId"] == "contract_price"
    assert dumped["path"][0] == {"relationId": "contract", "m2aCollection": None}
    assert dumped["outputScale"] == 2


def test_lookup_aggregation_rejects_the_removed_rollup_surface() -> None:
    with pytest.raises(ValidationError, match="Input should be 'single' or 'values'"):
        LookupDefinition.model_validate(
            {
                "lookupId": "contract_count",
                "collection": "orders",
                "fieldKey": "contract_count",
                "displayName": "Contract count",
                "path": [{"relationId": "contracts"}],
                "source": {"kind": "target_field", "fieldRef": "id"},
                "aggregation": "related_count",
                "outputType": "decimal",
            }
        )


def test_lookup_validate_draft_rejects_server_derived_shape_and_type() -> None:
    with pytest.raises(ValidationError, match="Extra inputs are not permitted"):
        LookupValidateParams.model_validate(
            {
                "definition": {
                    "lookupId": "contract_price",
                    "collection": "orders",
                    "fieldKey": "contract_price",
                    "displayName": "Contract price",
                    "path": [{"relationId": "contract"}],
                    "source": {"kind": "target_field", "fieldRef": "price"},
                    "aggregation": "single",
                    "outputType": "decimal",
                },
                "existing": [],
            }
        )


def test_lookup_dependency_graph_allows_dag_and_rejects_cycle() -> None:
    base = _lookup("base")
    derived = _lookup(
        "derived",
        source={"kind": "lookup", "lookupId": "base"},
        dependencies=["base"],
    )
    validate_lookup_dependency_graph([base, derived])

    cyclic_a = _lookup(
        "a",
        source={"kind": "lookup", "lookupId": "b"},
        dependencies=["b"],
    )
    cyclic_b = _lookup(
        "b",
        source={"kind": "lookup", "lookupId": "a"},
        dependencies=["a"],
    )
    with pytest.raises(ValueError, match="cycle"):
        validate_lookup_dependency_graph([cyclic_a, cyclic_b])


def test_schema_snapshot_keeps_relation_and_numeric_metadata() -> None:
    snapshot = SchemaSnapshot(
        collection="orders",
        primary_key="id",
        columns=[
            ColumnSchema(
                name="amount",
                title="Amount",
                field_id="orders.amount",
                data_type="decimal",
                scale=2,
                precision=10,
            )
        ],
        normalized_relations=[
            NormalizedRelationDescriptor(
                relation_id="orders.contract",
                field_ref="contract",
                source_collection="orders",
                kind="m2o",
                related_collection="contracts",
            )
        ],
        schema_revision="schema-1",
        permission_revision="permission-1",
        capability_hash="capability-1",
        lookup_revision="lookup-1",
    )

    assert snapshot.columns[0].scale == 2
    assert snapshot.normalized_relations[0].related_collection == "contracts"
