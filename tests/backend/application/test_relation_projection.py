"""C1 relation workspace tests.

Covers the declared-relation-only projection: deep-field query compilation,
relation column flattening, and rejection of undeclared relations (no generic
join).
"""

from __future__ import annotations

from typing import Any

import pytest

from backend.adapters.directus.auth import CurrentUser, DirectusAuthBroker
from backend.adapters.directus.client import DirectusClient
from backend.adapters.directus.errors import DirectusSchemaError
from backend.adapters.directus.profile import CapabilityManifest
from backend.application.directus_service import DirectusService
from backend.contracts.relation import RelationProjectionParams


class FakeAuth(DirectusAuthBroker):
    def __init__(self) -> None:
        self._user = CurrentUser(id="u1", display_name="T", role_id="r1")

    async def access_token(self) -> str:
        return "tok"


class FakeTransport:
    def __init__(self, response: Any) -> None:
        self._response = response
        self.requests: list[dict[str, Any]] = []

    async def request(self, method: str, path: str, **kwargs: Any) -> Any:
        self.requests.append({"method": method, "path": path, **kwargs})
        return self._response


def _manifest() -> CapabilityManifest:
    return CapabilityManifest.model_validate(
        {
            "contract": "directus.project.v1",
            "schema_version": "vibetable-1.0",
            "directus_compatibility": ">=12 <13",
            "collections": [
                {
                    "collection": "vibetable_demo",
                    "primary_key": "id",
                    "fields": ["id", "status", "number", "project", "date_updated"],
                    "create_fields": ["number", "project"],
                    "update_fields": ["number", "project"],
                    "archive_field": "status",
                    "archive_value": "archived",
                    "restore_value": "active",
                    "date_updated_field": "date_updated",
                    "relations": [
                        {
                            "field": "project",
                            "kind": "m2o",
                            "related_collection": "vibetable_related",
                            "display_fields": ["code", "name"],
                        }
                    ],
                }
            ],
        }
    )


def _service(transport: FakeTransport, manifest: CapabilityManifest) -> DirectusService:
    return DirectusService(manifest, FakeAuth(), DirectusClient(transport, FakeAuth()), None)  # type: ignore[arg-type]


@pytest.mark.asyncio
async def test_relation_projection_expands_declared_relations() -> None:
    manifest = _manifest()
    transport = FakeTransport(
        {
            "data": [
                {"id": "1", "number": "A-1", "project": {"code": "P1", "name": "Project One"}},
            ],
            "meta": {"filter_count": 1, "total_count": 1},
        }
    )
    service = _service(transport, manifest)
    result = await service.relation_projection(
        RelationProjectionParams(
            collection="vibetable_demo",
            relations=["project"],
        )
    )
    assert len(result.rows) == 1
    assert result.rows[0]["project"]["code"] == "P1"
    # The relation columns describe the flattened display fields.
    paths = [c.display_path for c in result.relation_columns]
    assert "project.code" in paths
    assert "project.name" in paths
    # The request asked for deep fields.
    fields = transport.requests[0]["query"]["fields"]
    assert "project.code" in fields


@pytest.mark.asyncio
async def test_relation_projection_rejects_undeclared_relation() -> None:
    manifest = _manifest()
    transport = FakeTransport({"data": [], "meta": {}})
    service = _service(transport, manifest)
    with pytest.raises(DirectusSchemaError):
        await service.relation_projection(
            RelationProjectionParams(
                collection="vibetable_demo",
                relations=["nonexistent"],
            )
        )


@pytest.mark.asyncio
async def test_relation_projection_respects_max_depth() -> None:
    manifest = _manifest()
    transport = FakeTransport({"data": [], "meta": {}})
    service = _service(transport, manifest)
    result = await service.relation_projection(
        RelationProjectionParams(
            collection="vibetable_demo",
            relations=["project"],
            max_depth=1,
        )
    )
    # display_fields capped at max_depth * 4 = 4 entries; only 2 declared.
    assert len(result.relation_columns) == 2
