"""Directus Flow boundary and deterministic in-memory adapter."""

from __future__ import annotations

import asyncio
from dataclasses import dataclass, field
from typing import Any, Literal
from urllib.parse import quote

from backend.contracts.plugin import FlowTrigger


class DirectusFlowAdapter:
    """Authenticated Directus 12 Flow/Operation adapter."""

    def __init__(self, *, transport: Any, auth: Any) -> None:
        self._transport = transport
        self._auth = auth

    async def create_inactive_flow(
        self, *, trigger: FlowTrigger, definition: dict[str, Any]
    ) -> str:
        body = {
            key: value
            for key, value in definition.items()
            if key not in {"operations", "status", "trigger", "id"}
        }
        body.update({"trigger": trigger, "status": "inactive"})
        payload = await self._request("POST", "/flows", json_body=body)
        data = _response_object(payload)
        flow_uuid = data.get("id")
        if not isinstance(flow_uuid, str) or not flow_uuid:
            raise ValueError("Directus did not return the created Flow UUID")
        return flow_uuid

    async def list_flows(self) -> list[DirectusFlowDefinition]:
        payload = await self._request("GET", "/flows", query={"fields": ["id"], "limit": -1})
        definitions: list[DirectusFlowDefinition] = []
        for item in _response_list(payload):
            flow_uuid = item.get("id")
            if not isinstance(flow_uuid, str):
                continue
            definition = await self.read_flow(flow_uuid)
            if definition is not None:
                definitions.append(definition)
        return definitions

    async def create_operations(self, flow_uuid: str, operations: list[dict[str, Any]]) -> None:
        operation_ids: dict[str, str] = {}
        for index, operation in enumerate(operations):
            key = operation.get("key")
            operation_type = operation.get("type")
            if not isinstance(key, str) or not key or key in operation_ids:
                raise ValueError("Directus Flow operation keys must be unique")
            if not isinstance(operation_type, str) or not operation_type:
                raise ValueError(f"Directus Flow operation {key!r} has no type")
            reserved = {
                "key",
                "type",
                "name",
                "position_x",
                "position_y",
                "options",
                "resolve",
                "reject",
            }
            options = dict(operation.get("options") or {})
            options.update({k: v for k, v in operation.items() if k not in reserved})
            body = {
                "flow": flow_uuid,
                "key": key,
                "type": operation_type,
                "name": operation.get("name", key),
                "position_x": operation.get("position_x", index * 20),
                "position_y": operation.get("position_y", 0),
                "options": options,
            }
            created = _response_object(await self._request("POST", "/operations", json_body=body))
            operation_uuid = created.get("id")
            if not isinstance(operation_uuid, str) or not operation_uuid:
                raise ValueError("Directus did not return the created Operation UUID")
            operation_ids[key] = operation_uuid

        for index, operation in enumerate(operations):
            key = str(operation["key"])
            default_resolve = (
                str(operations[index + 1]["key"]) if index + 1 < len(operations) else None
            )
            links: dict[str, str] = {}
            for branch, target in (
                ("resolve", operation.get("resolve", default_resolve)),
                ("reject", operation.get("reject")),
            ):
                if target is None:
                    continue
                if not isinstance(target, str) or target not in operation_ids:
                    raise ValueError(
                        f"Directus Flow operation {key!r} has an unknown {branch} target"
                    )
                links[branch] = operation_ids[target]
            if links:
                await self._request(
                    "PATCH",
                    f"/operations/{_segment(operation_ids[key])}",
                    json_body=links,
                )
        if operations:
            root_key = str(operations[0]["key"])
            await self._request(
                "PATCH",
                f"/flows/{_segment(flow_uuid)}",
                json_body={"operation": operation_ids[root_key]},
            )

    async def read_flow(self, flow_uuid: str) -> DirectusFlowDefinition | None:
        segment = _segment(flow_uuid)
        try:
            payload = await self._request("GET", f"/flows/{segment}")
        except Exception as exc:
            if getattr(exc, "status", None) == 404:
                return None
            raise
        data = _response_object(payload)
        operations_payload = await self._request(
            "GET",
            "/operations",
            query={"filter": {"flow": {"_eq": flow_uuid}}, "limit": -1},
        )
        operations = _response_list(operations_payload)
        operations = sorted(
            operations,
            key=lambda item: (
                item.get("position_y", 0),
                item.get("position_x", 0),
                str(item.get("key", "")),
            ),
        )
        id_to_key = {
            str(item["id"]): str(item["key"])
            for item in operations
            if item.get("id") is not None and item.get("key")
        }
        normalized_operations: list[dict[str, Any]] = []
        for item in operations:
            if not item.get("key") or not item.get("type"):
                continue
            normalized: dict[str, Any] = {
                "key": str(item["key"]),
                "type": str(item["type"]),
                "options": item.get("options") if isinstance(item.get("options"), dict) else {},
            }
            if isinstance(item.get("name"), str):
                normalized["name"] = item["name"]
            for branch in ("resolve", "reject"):
                target = item.get(branch)
                target_id = str(target.get("id")) if isinstance(target, dict) else str(target)
                if target is not None and target_id in id_to_key:
                    normalized[branch] = id_to_key[target_id]
            normalized_operations.append(normalized)
        trigger = str(data.get("trigger", "manual"))
        if trigger not in {"manual", "webhook", "schedule", "event"}:
            trigger = "event"
        status: Literal["active", "inactive"] = (
            "active" if data.get("status") == "active" else "inactive"
        )
        definition = {
            key: value
            for key, value in data.items()
            if key not in {"id", "trigger", "status", "operation", "operations"}
        }
        root = data.get("operation")
        root_id = str(root.get("id")) if isinstance(root, dict) else str(root)
        definition["operation"] = id_to_key.get(
            root_id,
            normalized_operations[0]["key"] if normalized_operations else None,
        )
        definition["operations"] = normalized_operations
        return DirectusFlowDefinition(
            flow_uuid=flow_uuid,
            trigger=trigger,  # type: ignore[arg-type]
            status=status,
            operation_keys=tuple(item["key"] for item in normalized_operations),
            definition=definition,
        )

    async def activate_flow(self, flow_uuid: str) -> None:
        await self._request(
            "PATCH", f"/flows/{_segment(flow_uuid)}", json_body={"status": "active"}
        )

    async def deactivate_flow(self, flow_uuid: str) -> None:
        await self._request(
            "PATCH", f"/flows/{_segment(flow_uuid)}", json_body={"status": "inactive"}
        )

    async def delete_flow(self, flow_uuid: str) -> None:
        await self._request("DELETE", f"/flows/{_segment(flow_uuid)}", expected_status=(204,))

    async def trigger_manual(self, flow_uuid: str, body: dict[str, Any]) -> dict[str, Any]:
        payload = await self._request(
            "POST", f"/flows/trigger/{_segment(flow_uuid)}", json_body=body
        )
        # Flow trigger responses are the operation return value itself, unlike
        # Directus CRUD endpoints which wrap records in ``data``.
        if not isinstance(payload, dict):
            raise ValueError("Directus Flow returned an invalid result")
        return payload

    async def _request(self, method: str, path: str, **kwargs: Any) -> Any:
        token = await self._auth.access_token()
        return await self._transport.request(method, path, access_token=token, **kwargs)


@dataclass(frozen=True)
class DirectusFlowDefinition:
    flow_uuid: str
    trigger: FlowTrigger
    status: Literal["active", "inactive"]
    operation_keys: tuple[str, ...]
    definition: dict[str, Any]


@dataclass
class InMemoryDirectusFlowAdapter:
    """Boundary fake that makes accidental writes to external Flows observable."""

    flows: list[DirectusFlowDefinition] = field(default_factory=list)
    mutation_log: list[tuple[str, str]] = field(default_factory=list)
    fail_on: set[str] = field(default_factory=set)
    flow_results: dict[str, dict[str, Any]] = field(default_factory=dict)
    invocation_log: list[dict[str, Any]] = field(default_factory=list)
    trace: list[str] | None = None
    trigger_gate: asyncio.Event | None = None
    _counter: int = 0

    async def list_flows(self) -> list[DirectusFlowDefinition]:
        return list(self.flows)

    async def read_flow(self, flow_uuid: str) -> DirectusFlowDefinition | None:
        return next((flow for flow in self.flows if flow.flow_uuid == flow_uuid), None)

    async def trigger_manual(self, flow_uuid: str, body: dict[str, Any]) -> dict[str, Any]:
        flow = await self.read_flow(flow_uuid)
        if flow is None:
            raise KeyError(flow_uuid)
        if flow.trigger != "manual" or flow.status != "active":
            raise RuntimeError("Flow is not an active manual Flow")
        if self.trace is not None:
            self.trace.append("flow.trigger")
        self.invocation_log.append({"flowUuid": flow_uuid, "body": body})
        if self.trigger_gate is not None:
            await self.trigger_gate.wait()
        return self.flow_results.get(
            flow_uuid,
            {
                "contract": "vibetable.plugin-result.v1",
                "status": "success",
                "summary": "Flow completed",
            },
        )

    async def create_inactive_flow(
        self, *, trigger: FlowTrigger, definition: dict[str, Any]
    ) -> str:
        self._fail_if_requested("create")
        self._counter += 1
        flow_uuid = f"managed-flow-{self._counter}"
        base_definition = {key: value for key, value in definition.items() if key != "operations"}
        self.flows.append(
            DirectusFlowDefinition(
                flow_uuid=flow_uuid,
                trigger=trigger,
                status="inactive",
                operation_keys=(),
                definition=base_definition,
            )
        )
        self.mutation_log.append(("create-inactive", flow_uuid))
        return flow_uuid

    async def create_operations(self, flow_uuid: str, operations: list[dict[str, Any]]) -> None:
        self._fail_if_requested("operations")
        flow = await self.read_flow(flow_uuid)
        if flow is None:
            raise KeyError(flow_uuid)
        keys = tuple(str(operation.get("key", "")) for operation in operations)
        definition = {
            **flow.definition,
            "operation": keys[0] if keys else None,
            "operations": [dict(operation) for operation in operations],
        }
        self._replace(flow, operation_keys=keys, definition=definition)
        self.mutation_log.append(("create-operations", flow_uuid))

    async def activate_flow(self, flow_uuid: str) -> None:
        self._fail_if_requested("activate")
        flow = await self.read_flow(flow_uuid)
        if flow is None:
            raise KeyError(flow_uuid)
        self._replace(flow, status="active")
        self.mutation_log.append(("activate", flow_uuid))

    async def deactivate_flow(self, flow_uuid: str) -> None:
        self._fail_if_requested("deactivate")
        flow = await self.read_flow(flow_uuid)
        if flow is None:
            raise KeyError(flow_uuid)
        self._replace(flow, status="inactive")
        self.mutation_log.append(("deactivate", flow_uuid))

    async def delete_flow(self, flow_uuid: str) -> None:
        self._fail_if_requested("delete")
        flow = await self.read_flow(flow_uuid)
        if flow is not None:
            self.flows.remove(flow)
        self.mutation_log.append(("delete", flow_uuid))

    def edit_definition(self, flow_uuid: str, definition: dict[str, Any]) -> None:
        flow = next((item for item in self.flows if item.flow_uuid == flow_uuid), None)
        if flow is None:
            raise KeyError(flow_uuid)
        self._replace(flow, definition=definition)
        self.mutation_log.append(("user-edit", flow_uuid))

    def _fail_if_requested(self, operation: str) -> None:
        if operation in self.fail_on:
            raise RuntimeError(f"injected Directus {operation} failure")

    def _replace(
        self,
        flow: DirectusFlowDefinition,
        *,
        status: Literal["active", "inactive"] | None = None,
        operation_keys: tuple[str, ...] | None = None,
        definition: dict[str, Any] | None = None,
    ) -> None:
        replacement = DirectusFlowDefinition(
            flow_uuid=flow.flow_uuid,
            trigger=flow.trigger,
            status=status or flow.status,
            operation_keys=operation_keys if operation_keys is not None else flow.operation_keys,
            definition=definition if definition is not None else flow.definition,
        )
        self.flows[self.flows.index(flow)] = replacement


def _segment(value: str) -> str:
    return quote(value, safe="")


def _response_object(payload: Any) -> dict[str, Any]:
    if not isinstance(payload, dict) or not isinstance(payload.get("data"), dict):
        raise ValueError("Directus returned an invalid object response")
    return payload["data"]


def _response_list(payload: Any) -> list[dict[str, Any]]:
    if not isinstance(payload, dict) or not isinstance(payload.get("data"), list):
        raise ValueError("Directus returned an invalid list response")
    return [item for item in payload["data"] if isinstance(item, dict)]


__all__ = [
    "DirectusFlowAdapter",
    "DirectusFlowDefinition",
    "InMemoryDirectusFlowAdapter",
]
