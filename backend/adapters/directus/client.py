"""Permission-bound Directus REST client for schema, query and mutations."""

from __future__ import annotations

import asyncio
import uuid
from collections.abc import Sequence
from typing import Any, Literal
from urllib.parse import quote

from backend.adapters.directus.auth import DirectusAuthBroker
from backend.adapters.directus.errors import (
    DirectusQueryError,
    DirectusSchemaError,
    DirectusTransportError,
)
from backend.adapters.directus.profile import CollectionProfile
from backend.adapters.directus.query import DirectusQueryPlan, compile_directus_query
from backend.adapters.directus.schema import directus_readonly_fields
from backend.adapters.directus.transport import DirectusTransport
from backend.contracts.query import TableQuery

_HISTORY_RESTORE_CONTRACT = "vibetable-history-restore.v1"


class DirectusClient:
    """Calls Directus with the current user's token and profile allowlists."""

    def __init__(self, transport: DirectusTransport, auth: DirectusAuthBroker) -> None:
        self._transport = transport
        self._auth = auth
        self._readonly_fields: dict[str, set[str]] = {}
        self._mutation_lock = asyncio.Lock()

    async def server_info(self) -> dict[str, Any]:
        return _response_object(await self._transport.request("GET", "/server/info"))

    async def fields(self, profile: CollectionProfile) -> list[dict[str, Any]]:
        payload = await self._authorized("GET", f"/fields/{_segment(profile.collection)}")
        fields = _response_list(payload)
        self._readonly_fields[profile.collection] = directus_readonly_fields(fields)
        return fields

    async def readonly_fields(
        self,
        profile: CollectionProfile,
        *,
        refresh: bool = False,
    ) -> set[str]:
        """Return the live Directus write restrictions for a collection."""

        if refresh or profile.collection not in self._readonly_fields:
            await self.fields(profile)
        return set(self._readonly_fields[profile.collection])

    async def require_write_fields(
        self,
        profile: CollectionProfile,
        requested: set[str],
        *,
        operation: Literal["create", "update"],
        refresh: bool = False,
    ) -> None:
        """Enforce both the profile allowlist and live Directus metadata."""

        profile.require_fields(requested, operation=operation)
        denied = requested & await self.readonly_fields(profile, refresh=refresh)
        if denied:
            names = ", ".join(sorted(denied))
            raise DirectusSchemaError(f"fields are read-only in Directus: {names}")

    async def relations(self, profile: CollectionProfile) -> list[dict[str, Any]]:
        payload = await self._authorized("GET", f"/relations/{_segment(profile.collection)}")
        relations = _response_list(payload)
        allowed = {relation.field for relation in profile.relations}
        return [relation for relation in relations if _relation_field(relation) in allowed]

    async def read_items(
        self,
        profile: CollectionProfile,
        query: TableQuery,
        *,
        include_archived: bool = False,
    ) -> tuple[list[dict[str, Any]], dict[str, Any], DirectusQueryPlan]:
        plan = compile_directus_query(
            query,
            approved_fields=profile.approved_fields,
            primary_key=profile.primary_key,
        )
        params = dict(plan.params)
        params["fields"] = profile.fields
        params["meta"] = "filter_count,total_count"
        if profile.archive_field and not include_archived:
            archive_filter = {profile.archive_field: {"_neq": profile.archive_value}}
            existing = params.get("filter")
            params["filter"] = (
                archive_filter if existing is None else {"_and": [existing, archive_filter]}
            )
        payload = await self._authorized(
            "GET",
            f"/items/{_segment(profile.collection)}",
            query=params,
        )
        return _response_list(payload), _response_meta(payload), plan

    async def read_items_with_fields(
        self,
        profile: CollectionProfile,
        query: TableQuery,
        fields: list[str],
        *,
        include_archived: bool = False,
    ) -> tuple[list[dict[str, Any]], dict[str, Any], DirectusQueryPlan]:
        """Read items with an explicit field list (supports nested/deep fields).

        Used by the C1 relation workspace to fetch ``project.code``-style deep
        fields declared in the collection's :class:`RelationProfile`. The field
        list still passes through the profile allow-list at the top level.
        """
        plan = compile_directus_query(
            query,
            approved_fields=profile.approved_fields,
            primary_key=profile.primary_key,
        )
        params = dict(plan.params)
        top_level = {f.split(".", 1)[0] for f in fields}
        denied = top_level - set(profile.fields)
        if denied:
            raise DirectusQueryError(
                f"fields outside profile allowlist: {', '.join(sorted(denied))}",
            )
        params["fields"] = fields
        params["meta"] = "filter_count,total_count"
        if profile.archive_field and not include_archived:
            archive_filter = {profile.archive_field: {"_neq": profile.archive_value}}
            existing = params.get("filter")
            params["filter"] = (
                archive_filter if existing is None else {"_and": [existing, archive_filter]}
            )
        payload = await self._authorized(
            "GET",
            f"/items/{_segment(profile.collection)}",
            query=params,
        )
        return _response_list(payload), _response_meta(payload), plan

    async def read_item(
        self,
        profile: CollectionProfile,
        item_id: str,
        *,
        fields: Sequence[str] | None = None,
    ) -> dict[str, Any]:
        payload = await self._authorized(
            "GET",
            f"/items/{_segment(profile.collection)}/{_segment(item_id)}",
            query={"fields": list(fields) if fields is not None else profile.fields},
        )
        return _response_object(payload)

    async def create_item(
        self,
        profile: CollectionProfile,
        values: dict[str, Any],
        *,
        request_id: str | None = None,
    ) -> dict[str, Any]:
        # Validate only caller-supplied fields. The client-generated primary
        # key is added afterwards and is intentionally not mistaken for a
        # user write to Studio's readonly ID interface.
        await self.require_write_fields(
            profile,
            set(values),
            operation="create",
            refresh=True,
        )
        body = dict(values)
        item_id = str(body.get(profile.primary_key) or uuid.uuid4())
        body[profile.primary_key] = item_id
        request_id = request_id or str(uuid.uuid4())
        try:
            payload = await self._authorized(
                "POST",
                f"/items/{_segment(profile.collection)}",
                query={"fields": profile.fields},
                json_body=body,
                headers={"X-Request-ID": request_id},
            )
        except DirectusTransportError as exc:
            if exc.code != "SERVICE_UNAVAILABLE":
                raise
            return await self.read_item(profile, item_id)
        return _response_object(payload)

    async def update_item(
        self,
        profile: CollectionProfile,
        item_id: str,
        values: dict[str, Any],
        *,
        expected_date_updated: str | None = None,
        request_id: str | None = None,
    ) -> dict[str, Any]:
        async with self._mutation_lock:
            return await self._update_item_locked(
                profile,
                item_id,
                values,
                expected_date_updated=expected_date_updated,
                request_id=request_id,
            )

    async def update_item_if_unchanged(
        self,
        profile: CollectionProfile,
        item_id: str,
        values: dict[str, Any],
        *,
        expected_values: dict[str, Any],
        read_fields: set[str] | None = None,
        request_id: str | None = None,
        operation: str | None = None,
        authorization_token: str | None = None,
    ) -> dict[str, Any]:
        """Conditionally update one item with a server-side field-level CAS."""

        async with self._mutation_lock:
            await self.require_write_fields(
                profile,
                set(values),
                operation="update",
                refresh=True,
            )
            if operation == "restore":
                if authorization_token is None:
                    raise DirectusSchemaError("restore requires an authorization token")
                payload = await self._authorized(
                    "POST",
                    "/vibetable-bulk-mutation/restore",
                    json_body={
                        "contract": _HISTORY_RESTORE_CONTRACT,
                        "authorizationToken": authorization_token,
                    },
                    headers={"Idempotency-Key": request_id or str(uuid.uuid4())},
                )
                return _response_object(payload)
            conditions: list[dict[str, Any]] = [{profile.primary_key: {"_eq": item_id}}]
            for field, expected in expected_values.items():
                operator = {"_null": True} if expected is None else {"_eq": expected}
                conditions.append({field: operator})
            payload = await self._authorized(
                "PATCH",
                f"/items/{_segment(profile.collection)}",
                query={
                    "fields": sorted(read_fields) if read_fields is not None else profile.fields
                },
                json_body={
                    "query": {"filter": {"_and": conditions}, "limit": 1},
                    "data": values,
                },
                headers=_mutation_headers(request_id),
            )
            updated = _response_list(payload)
            if len(updated) != 1:
                raise DirectusTransportError(
                    "Item was changed by another user",
                    status=409,
                    code="EDIT_CONFLICT",
                )
            return updated[0]

    async def _update_item_locked(
        self,
        profile: CollectionProfile,
        item_id: str,
        values: dict[str, Any],
        *,
        expected_date_updated: str | None = None,
        request_id: str | None = None,
    ) -> dict[str, Any]:
        # Refresh at the mutation boundary: Studio can toggle readonly or a
        # migration can turn a column into a generated field after the grid's
        # schema snapshot was cached.
        await self.require_write_fields(
            profile,
            set(values),
            operation="update",
            refresh=True,
        )
        if expected_date_updated is not None:
            if profile.date_updated_field is None:
                raise DirectusSchemaError("collection has no optimistic concurrency field")
            current = await self.read_item(profile, item_id)
            if current.get(profile.date_updated_field) != expected_date_updated:
                raise DirectusTransportError(
                    "Item was changed by another user",
                    status=409,
                    code="EDIT_CONFLICT",
                )
        payload = await self._authorized(
            "PATCH",
            f"/items/{_segment(profile.collection)}/{_segment(item_id)}",
            query={"fields": profile.fields},
            json_body=values,
            headers=_mutation_headers(request_id),
        )
        return _response_object(payload)

    async def archive_item(self, profile: CollectionProfile, item_id: str) -> dict[str, Any]:
        if profile.archive_field is None:
            raise DirectusSchemaError("collection does not support archive")
        return await self.update_item(
            profile,
            item_id,
            {profile.archive_field: profile.archive_value},
        )

    async def restore_item(self, profile: CollectionProfile, item_id: str) -> dict[str, Any]:
        if profile.archive_field is None:
            raise DirectusSchemaError("collection does not support restore")
        return await self.update_item(
            profile,
            item_id,
            {profile.archive_field: profile.restore_value},
        )

    async def delete_item(self, profile: CollectionProfile, item_id: str) -> None:
        if not profile.allow_permanent_delete:
            await self.archive_item(profile, item_id)
            return
        await self._authorized(
            "DELETE",
            f"/items/{_segment(profile.collection)}/{_segment(item_id)}",
            expected_status=(204,),
        )

    async def _authorized(self, method: str, path: str, **kwargs: Any) -> Any:
        token = await self._auth.access_token()
        return await self._transport.request(method, path, access_token=token, **kwargs)


def _segment(value: str) -> str:
    return quote(value, safe="")


def _mutation_headers(request_id: str | None) -> dict[str, str]:
    return {"X-Request-ID": request_id or str(uuid.uuid4())}


def _response_object(payload: Any) -> dict[str, Any]:
    if not isinstance(payload, dict) or not isinstance(payload.get("data"), dict):
        raise DirectusTransportError("Directus returned an invalid object response")
    return payload["data"]


def _response_list(payload: Any) -> list[dict[str, Any]]:
    if not isinstance(payload, dict) or not isinstance(payload.get("data"), list):
        raise DirectusTransportError("Directus returned an invalid list response")
    if not all(isinstance(item, dict) for item in payload["data"]):
        raise DirectusTransportError("Directus returned invalid list items")
    return payload["data"]


def _response_meta(payload: Any) -> dict[str, Any]:
    if isinstance(payload, dict) and isinstance(payload.get("meta"), dict):
        return payload["meta"]
    return {}


def _relation_field(relation: dict[str, Any]) -> str | None:
    meta = relation.get("meta")
    if not isinstance(meta, dict):
        return None
    field = meta.get("many_field") or meta.get("one_field")
    return field if isinstance(field, str) else None
