"""PocketBase MutationKernel adapter for validated plugin mutation plans."""

from __future__ import annotations

import uuid
from collections.abc import Mapping
from typing import Any, Protocol

from backend.contracts.data_profile import collection_profile_from_definition
from backend.contracts.plugin import MutationPlan


class _MutationClient(Protocol):
    async def apply_mutation(self, request: Mapping[str, Any]) -> dict[str, Any]: ...
    async def describe_table(self, table_id: str) -> dict[str, Any]: ...


class PocketBasePluginMutationAdapter:
    """Constrain a plugin plan, then submit it through MutationKernel only."""

    def __init__(
        self,
        *,
        client: _MutationClient,
        schema_revisions: Mapping[str, str],
        writable_fields: Mapping[str, set[str] | frozenset[str]],
    ) -> None:
        self._client = client
        self._schema_revisions = dict(schema_revisions)
        self._writable_fields = {
            table_id: frozenset(fields)
            for table_id, fields in writable_fields.items()
        }
        self._dynamic_schema = not schema_revisions and not writable_fields

    async def apply(self, plan: MutationPlan) -> dict[str, Any]:
        schema_revision = self._schema_revisions.get(plan.collection)
        allowed = self._writable_fields.get(plan.collection)
        if self._dynamic_schema or schema_revision is None or allowed is None:
            if not self._dynamic_schema:
                raise ValueError("plugin mutation collection is not granted")
            definition = await self._client.describe_table(plan.collection)
            profile = collection_profile_from_definition(definition)
            schema_revision = profile.schema_revision
            allowed = frozenset(profile.create_fields) | frozenset(
                profile.update_fields
            )
            if schema_revision is None:
                raise ValueError("plugin mutation collection is not granted")
            self._schema_revisions[plan.collection] = schema_revision
            self._writable_fields[plan.collection] = allowed

        operations: list[dict[str, Any]] = []
        for index, operation in enumerate(plan.operations):
            forbidden = set(operation.values) - allowed
            if forbidden:
                name = sorted(forbidden)[0]
                raise ValueError(
                    f"plugin mutation field {name!r} is not granted at operations[{index}]"
                )
            operations.append(
                {
                    "kind": "insert" if operation.kind == "create" else "update",
                    "recordId": operation.primary_key,
                    "values": dict(operation.values),
                }
            )

        request_id = plan.idempotency_key or f"plugin-{uuid.uuid4()}"
        receipt = await self._client.apply_mutation(
            {
                "contractVersion": "1.0",
                "requestId": request_id,
                "idempotencyKey": request_id,
                "tableId": plan.collection,
                "schemaRevision": schema_revision,
                "operations": operations,
                "actor": {
                    "type": "plugin",
                    "id": "local-plugin",
                    "displayName": None,
                },
                "expectedRevision": None,
                "expectedDigest": None,
            }
        )
        if receipt.get("status") not in {"applied", "replayed"}:
            raise ValueError("plugin mutation was not applied")
        affected = receipt.get("affectedRows")
        count = len(affected) if isinstance(affected, list) else len(operations)
        return {
            "contract": "vibetable.plugin-result.v1",
            "status": "success",
            "summary": f"Applied {count} plugin mutation(s)",
            "metrics": [{"label": "affectedRows", "value": count}],
        }


__all__ = ["PocketBasePluginMutationAdapter"]
