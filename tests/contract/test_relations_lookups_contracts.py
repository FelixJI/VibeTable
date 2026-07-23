from __future__ import annotations

import json
from pathlib import Path

from backend.contracts.lookup import LookupDefinition, LookupQueryParams, LookupQueryResult
from backend.contracts.relation_admin import RelationSingleUpdateResult, SchemaSnapshot

FIXTURE = Path(__file__).parent / "fixtures" / "table-relations-lookups-contracts.json"


def _load() -> dict:
    return json.loads(FIXTURE.read_text(encoding="utf-8"))


def test_relations_lookups_fixture_round_trip() -> None:
    fixture = _load()

    assert fixture["contract"] == "table.relations-lookups.fixtures.v1"
    snapshot = SchemaSnapshot.model_validate(fixture["schemaSnapshot"])
    definition = LookupDefinition.model_validate(fixture["lookupDefinition"])
    single_update = RelationSingleUpdateResult.model_validate(fixture["singleUpdateResult"])
    params = LookupQueryParams.model_validate(fixture["query"]["params"])
    result = LookupQueryResult.model_validate(fixture["query"]["result"])

    assert snapshot.normalized_relations[0].relation_id == "orders.contract"
    assert definition.output_type == "decimal"
    assert single_update.current is not None
    assert single_update.current.item_id == "contract-7"
    assert params.request_generation == result.request_generation == 7
    assert result.rows[0]["orders.contract_price"] == "1250.50"
