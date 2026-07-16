"""Greenfield-only Directus collection and relation bootstrap support."""

from __future__ import annotations

import json
from pathlib import Path
from typing import Any

from pydantic import BaseModel, ConfigDict

from backend.adapters.directus.errors import DirectusSchemaError
from backend.adapters.directus.transport import DirectusTransport


class BootstrapAction(BaseModel):
    model_config = ConfigDict(extra="forbid", frozen=True)

    kind: str
    target: str
    state: str


class DirectusProjectBootstrapper:
    """Create the B4 model in an empty project without destructive updates."""

    def __init__(self, transport: DirectusTransport, admin_token: str) -> None:
        if not admin_token:
            raise ValueError("admin token is required for deployment bootstrap")
        self._transport = transport
        self._admin_token = admin_token

    async def plan(self, blueprint: dict[str, Any]) -> list[BootstrapAction]:
        current = await self._request("GET", "/collections")
        existing = {
            item.get("collection")
            for item in _response_list(current)
            if isinstance(item.get("collection"), str)
        }
        actions: list[BootstrapAction] = []
        for raw_name in blueprint["collections"]:
            if not isinstance(raw_name, str) or not raw_name:
                raise DirectusSchemaError("blueprint collection names must be non-empty strings")
            name = raw_name
            state = "review-existing" if name in existing else "create"
            actions.append(BootstrapAction(kind="collection", target=name, state=state))
        return actions

    async def apply_empty(self, blueprint: dict[str, Any]) -> list[BootstrapAction]:
        actions = await self.plan(blueprint)
        conflicts = [action.target for action in actions if action.state == "review-existing"]
        if conflicts:
            raise DirectusSchemaError(
                "bootstrap is greenfield-only; existing collections require schema diff: "
                + ", ".join(conflicts)
            )
        for name, definition in blueprint["collections"].items():
            await self._request(
                "POST",
                "/collections",
                json_body=build_collection_payload(name, definition),
            )
        for name, definition in blueprint["collections"].items():
            for field_name, field in definition["fields"].items():
                related = field.get("relation")
                if related:
                    await self._request(
                        "POST",
                        "/relations",
                        json_body=build_relation_payload(name, field_name, related),
                    )
        created = [action.model_copy(update={"state": "created"}) for action in actions]
        for policy_name, grants in blueprint.get("policies", {}).items():
            display_name = f"VibeTable {policy_name.title()}"
            policy_response = await self._request(
                "POST",
                "/policies",
                json_body={
                    "name": display_name,
                    "description": f"Managed by {blueprint['contract']} {blueprint['schema_version']}",
                    "admin_access": False,
                    "app_access": False,
                },
            )
            policy_id = _response_id(policy_response, "policy")
            await self._request(
                "POST",
                "/roles",
                json_body={
                    "name": display_name,
                    "description": "Assign users to this role; permissions come from its policy.",
                    "policies": [policy_id],
                },
            )
            permissions = build_permission_payloads(policy_id, grants, blueprint["collections"])
            if permissions:
                await self._request("POST", "/permissions", json_body=permissions)
            created.extend(
                [
                    BootstrapAction(kind="policy", target=policy_name, state="created"),
                    BootstrapAction(kind="role", target=policy_name, state="created"),
                    BootstrapAction(
                        kind="permissions",
                        target=policy_name,
                        state="created",
                    ),
                ]
            )
        return created

    async def snapshot(self) -> dict[str, Any]:
        payload = await self._request("GET", "/schema/snapshot")
        if not isinstance(payload, dict) or not isinstance(payload.get("data"), dict):
            raise DirectusSchemaError("Directus returned an invalid schema snapshot")
        return payload["data"]

    async def _request(self, method: str, path: str, **kwargs: Any) -> Any:
        return await self._transport.request(
            method,
            path,
            access_token=self._admin_token,
            **kwargs,
        )


def load_blueprint(path: Path) -> dict[str, Any]:
    payload = json.loads(path.read_text(encoding="utf-8"))
    if payload.get("contract") != "vibetable.directus-blueprint.v1":
        raise DirectusSchemaError("unsupported Directus blueprint contract")
    collections = payload.get("collections")
    if not isinstance(collections, dict) or not collections:
        raise DirectusSchemaError("Directus blueprint has no collections")
    return payload


def build_collection_payload(name: str, definition: dict[str, Any]) -> dict[str, Any]:
    fields = [_field_payload("id", definition["fields"]["id"])]
    fields.extend(_system_fields(definition))
    fields.extend(
        _field_payload(field_name, field)
        for field_name, field in definition["fields"].items()
        if field_name not in {"id", "status"}
    )
    return {
        "collection": name,
        "schema": {"name": name},
        "meta": {
            "collection": name,
            "accountability": definition.get("accountability", "all"),
            "archive_field": definition.get("archive_field"),
            "archive_value": definition.get("archive_value"),
            "unarchive_value": definition.get("unarchive_value"),
            "archive_app_filter": True,
            "sort_field": "sort",
            "versioning": bool(definition.get("versioning")),
        },
        "fields": fields,
    }


def build_relation_payload(collection: str, field: str, related: str) -> dict[str, Any]:
    return {
        "collection": collection,
        "field": field,
        "related_collection": related,
        "meta": {
            "many_collection": collection,
            "many_field": field,
            "one_collection": related,
            "one_deselect_action": "nullify",
        },
        "schema": {"on_delete": "SET NULL", "on_update": "NO ACTION"},
    }


def build_permission_payloads(
    policy_id: str,
    grants: dict[str, Any],
    collections: dict[str, Any],
) -> list[dict[str, Any]]:
    """Compile the explicit allow-list; omitted actions remain denied by Directus."""
    payloads: list[dict[str, Any]] = []
    for action in ("create", "read", "update", "delete"):
        allowed = grants.get(action, [])
        if not isinstance(allowed, list):
            raise DirectusSchemaError(f"policy action {action!r} must be a collection list")
        for collection in allowed:
            definition = collections.get(collection)
            if not isinstance(definition, dict):
                raise DirectusSchemaError(f"policy grants unknown collection {collection!r}")
            field_names = [
                field["field"]
                for field in build_collection_payload(collection, definition)["fields"]
            ]
            payloads.append(
                {
                    "policy": policy_id,
                    "collection": collection,
                    "action": action,
                    "permissions": {},
                    "validation": {},
                    "presets": {},
                    "fields": field_names,
                }
            )
    return payloads


def _system_fields(definition: dict[str, Any]) -> list[dict[str, Any]]:
    status = dict(definition["fields"].get("status", {"type": "string", "default": "active"}))
    return [
        _field_payload("status", status),
        _field_payload("sort", {"type": "integer"}),
        _field_payload("date_created", {"type": "timestamp", "special": ["date-created"]}),
        _field_payload(
            "user_created",
            {"type": "uuid", "special": ["user-created"], "relation": "directus_users"},
        ),
        _field_payload("date_updated", {"type": "timestamp", "special": ["date-updated"]}),
        _field_payload(
            "user_updated",
            {"type": "uuid", "special": ["user-updated"], "relation": "directus_users"},
        ),
    ]


def _field_payload(name: str, definition: dict[str, Any]) -> dict[str, Any]:
    data_type = definition["type"]
    special = definition.get("special")
    meta: dict[str, Any] = {
        "field": name,
        "required": bool(definition.get("required")),
        "readonly": bool(special),
    }
    if special:
        meta["special"] = special
    if "choices" in definition:
        meta["options"] = {
            "choices": [{"text": value.title(), "value": value} for value in definition["choices"]]
        }
    schema: dict[str, Any] = {
        "name": name,
        "data_type": data_type,
        "is_nullable": not bool(definition.get("required")),
        "is_primary_key": bool(definition.get("primary_key")),
        "is_unique": bool(definition.get("unique")),
        "default_value": definition.get("default"),
    }
    if "max_length" in definition:
        schema["max_length"] = definition["max_length"]
    if "precision" in definition:
        schema["numeric_precision"] = definition["precision"]
    if "scale" in definition:
        schema["numeric_scale"] = definition["scale"]
    return {"field": name, "type": data_type, "meta": meta, "schema": schema}


def _response_list(payload: Any) -> list[dict[str, Any]]:
    if not isinstance(payload, dict) or not isinstance(payload.get("data"), list):
        raise DirectusSchemaError("Directus returned an invalid collection list")
    return payload["data"]


def _response_id(payload: Any, kind: str) -> str:
    if not isinstance(payload, dict) or not isinstance(payload.get("data"), dict):
        raise DirectusSchemaError(f"Directus returned an invalid {kind} response")
    value = payload["data"].get("id")
    if not isinstance(value, str) or not value:
        raise DirectusSchemaError(f"Directus {kind} response omitted id")
    return value
