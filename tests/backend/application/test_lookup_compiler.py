from __future__ import annotations

import pytest

from backend.application.lookup_compiler import LookupCompileError, compile_lookup_plan
from backend.contracts.lookup import LookupDefinition, LookupQueryParams
from backend.contracts.relation_admin import SchemaSnapshot
from backend.contracts.table import ColumnSchema


def _field(collection: str, field: str, *, primary: bool = False, type_: str = "string"):
    return {
        "collection": collection,
        "field": field,
        "type": type_,
        "meta": {},
        "schema": {"is_primary_key": primary, "is_nullable": not primary},
    }


def _relation(
    many_collection: str,
    many_field: str,
    one_collection: str,
    *,
    one_field: str | None = None,
):
    return {
        "collection": many_collection,
        "field": many_field,
        "related_collection": one_collection,
        "meta": {
            "many_collection": many_collection,
            "many_field": many_field,
            "one_collection": one_collection,
            "one_field": one_field,
        },
        "schema": {"on_delete": "SET NULL"},
    }


def _snapshot() -> SchemaSnapshot:
    return SchemaSnapshot(
        collection="orders",
        primary_key="id",
        columns=[
            ColumnSchema(
                name="amount",
                title="Amount",
                field_id="orders.amount",
                data_type="decimal",
                scale=2,
            )
        ],
        schema_revision="schema-1",
        permission_revision="permission-1",
        capability_hash="capability-1",
        lookup_revision="lookup-1",
    )


def _definition() -> LookupDefinition:
    return LookupDefinition.model_validate(
        {
            "lookupId": "contract-price",
            "collection": "orders",
            "fieldKey": "contract_price",
            "displayName": "合同价格",
            "path": [{"relationId": "orders.contract"}],
            "source": {"kind": "target_field", "fieldRef": "price"},
            "aggregation": "single",
            "outputType": "decimal",
            "outputScale": 2,
        }
    )


def _params() -> LookupQueryParams:
    return LookupQueryParams.model_validate(
        {
            "collection": "orders",
            "fieldRefs": ["orders.amount", "orders.contract_price"],
            "query": {
                "filters": [
                    {
                        "field": "orders.contract_price",
                        "operator": "gt",
                        "value": "100.00",
                    }
                ],
                "sorts": [{"field": "orders.contract_price", "direction": "desc"}],
                "groups": [],
                "offset": 0,
                "limit": 100,
            },
            "requestGeneration": 7,
            "schemaRevision": "schema-1",
            "permissionRevision": "permission-1",
            "lookupRevision": "lookup-1",
        }
    )


def test_compiles_stable_m2o_lookup_to_physical_plan() -> None:
    fields = [
        _field("orders", "id", primary=True, type_="uuid"),
        _field("orders", "amount", type_="decimal"),
        _field("orders", "contract", type_="uuid"),
        _field("contracts", "id", primary=True, type_="uuid"),
        _field("contracts", "price", type_="decimal"),
    ]

    plan = compile_lookup_plan(
        params=_params(),
        snapshot=_snapshot(),
        definitions=[_definition()],
        fields=fields,
        relations=[_relation("orders", "contract", "contracts")],
    )

    assert plan["contract"] == "vibetable-lookup-query.v1"
    assert plan["definitionRevisions"] == {"contract-price": 1}
    assert plan["baseFields"] == [
        {
            "ref": "orders.amount",
            "field": "amount",
            "outputType": {"kind": "decimal", "scale": 2},
        }
    ]
    lookup = plan["lookups"][0]
    assert lookup["ref"] == "orders.contract_price"
    assert lookup["path"] == [
        {
            "relationId": "orders.contract",
            "kind": "m2o",
            "fromCollection": "orders",
            "sourceField": "contract",
            "targetField": "id",
            "destinationPrimaryKey": "id",
            "toCollection": "contracts",
        }
    ]
    assert lookup["source"] == {"kind": "field", "field": "price"}
    assert plan["filter"]["operator"] == "gt"


def test_rejects_stale_permission_revision_before_execution() -> None:
    params = _params().model_copy(update={"permission_revision": "stale"})
    with pytest.raises(LookupCompileError, match="permissions changed"):
        compile_lookup_plan(
            params=params,
            snapshot=_snapshot(),
            definitions=[_definition()],
            fields=[],
            relations=[],
        )


def test_rejects_unportable_regex_instead_of_current_page_fallback() -> None:
    params = _params().model_copy(
        update={
            "query": _params().query.model_copy(
                update={
                    "filters": [_params().query.filters[0].model_copy(update={"operator": "regex"})]
                }
            )
        }
    )
    with pytest.raises(LookupCompileError, match="unsupported"):
        compile_lookup_plan(
            params=params,
            snapshot=_snapshot(),
            definitions=[_definition()],
            fields=[
                _field("orders", "id", primary=True),
                _field("orders", "amount"),
                _field("orders", "contract"),
                _field("contracts", "id", primary=True),
                _field("contracts", "price"),
            ],
            relations=[_relation("orders", "contract", "contracts")],
        )
