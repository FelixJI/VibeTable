"""Table content profiles and explicit PocketBase-owned record/document links."""

from __future__ import annotations

from typing import Protocol

from pydantic import ValidationError

from backend.application.revisioned_metadata_port import (
    JsonObject,
    MetadataConflictError,
    MetadataDelete,
    MetadataQuery,
    MetadataRecord,
    MetadataWrite,
    RevisionedMetadataPort,
    json_object,
)
from backend.contracts.generated_workbench import (
    ContentProfile,
    ContentProfileCommitRequest,
    ContentProfileDeleteResult,
    ContentProfileSnapshot,
    RecordDocumentLink,
    RecordDocumentLinkCommitRequest,
    RecordDocumentLinkDeleteResult,
    RecordDocumentLinkListResult,
    RecordDocumentLinkRepairRequest,
    RecordDocumentLinkSnapshot,
)

_TEXT_TYPES = frozenset({"text", "editor", "email", "url", "select"})
_BODY_TYPES = frozenset({"editor"})


class ProductDataPort(Protocol):
    async def describe_table(self, table_id: str) -> JsonObject: ...

    async def read_rows(
        self,
        *,
        table_id: str,
        row_ids: list[str],
    ) -> list[JsonObject]: ...


class ContentModelError(Exception):
    def __init__(self, message: str, *, code: str, path: str | None = None) -> None:
        super().__init__(message)
        self.code = code
        self.path = path

    @property
    def rpc_error_data(self) -> dict[str, str]:
        result = {"code": self.code}
        if self.path is not None:
            result["path"] = self.path
        return result


class ContentModelService:
    """Own closed content settings and link aggregates without copying business data."""

    def __init__(
        self,
        *,
        metadata_port: RevisionedMetadataPort,
        product_data: ProductDataPort,
    ) -> None:
        self._metadata = metadata_port
        self._product_data = product_data

    async def load_profile(self, table_id: str) -> ContentProfileSnapshot:
        row = await self._one("content_profiles", table_id)
        if row is None:
            raise _error("content_profile.not_found", "Content profile not found.")
        return _profile_snapshot(row)

    async def commit_profile(
        self,
        request: ContentProfileCommitRequest,
    ) -> ContentProfileSnapshot:
        await self._validate_profile(request.profile)
        current = await self._one("content_profiles", request.profile.table_id)
        if _optional_revision(current) != request.expected_revision:
            raise _conflict("content_profile.edit_conflict")
        item = await self._upsert(
            "content_profiles",
            request.profile.table_id,
            json_object(request.profile.model_dump(by_alias=True, mode="json")),
            request.expected_revision,
            request.idempotency_key,
        )
        return _profile_snapshot(item)

    async def delete_profile(
        self,
        table_id: str,
        expected_revision: str,
        idempotency_key: str,
    ) -> ContentProfileDeleteResult:
        await self._delete("content_profiles", table_id, expected_revision, idempotency_key)
        return ContentProfileDeleteResult(table_id=table_id)

    async def list_links(
        self,
        table_id: str,
        record_id: str,
    ) -> RecordDocumentLinkListResult:
        rows = await self._metadata.read(MetadataQuery("record_document_links"))
        snapshots: list[RecordDocumentLinkSnapshot] = []
        for row in rows:
            snapshot = _link_snapshot(row)
            if snapshot.link.table_id == table_id and snapshot.link.record_id == record_id:
                snapshots.append(snapshot)
        snapshots.sort(key=lambda item: (item.link.order, item.link.link_id))
        return RecordDocumentLinkListResult(items=snapshots)

    async def commit_link(
        self,
        request: RecordDocumentLinkCommitRequest,
    ) -> RecordDocumentLinkSnapshot:
        await self._validate_record(request.link.table_id, request.link.record_id)
        current = await self._one("record_document_links", request.link.link_id)
        if _optional_revision(current) != request.expected_revision:
            raise _conflict("record_document_link.edit_conflict")
        item = await self._upsert(
            "record_document_links",
            request.link.link_id,
            json_object(request.link.model_dump(by_alias=True, mode="json")),
            request.expected_revision,
            request.idempotency_key,
        )
        return _link_snapshot(item)

    async def repair_link(
        self,
        request: RecordDocumentLinkRepairRequest,
    ) -> RecordDocumentLinkSnapshot:
        current = await self._one("record_document_links", request.link_id)
        if current is None:
            raise _error("record_document_link.not_found", "Link not found.")
        if _optional_revision(current) != request.expected_revision:
            raise _conflict("record_document_link.edit_conflict")
        snapshot = _link_snapshot(current)
        repaired = snapshot.link.model_copy(update={"document_id": request.document_id})
        item = await self._upsert(
            "record_document_links",
            request.link_id,
            json_object(repaired.model_dump(by_alias=True, mode="json")),
            request.expected_revision,
            request.idempotency_key,
        )
        return _link_snapshot(item)

    async def delete_link(
        self,
        link_id: str,
        expected_revision: str,
        idempotency_key: str,
    ) -> RecordDocumentLinkDeleteResult:
        await self._delete("record_document_links", link_id, expected_revision, idempotency_key)
        return RecordDocumentLinkDeleteResult(link_id=link_id)

    async def _validate_profile(self, profile: ContentProfile) -> None:
        try:
            definition = await self._product_data.describe_table(profile.table_id)
        except Exception as error:
            raise _error(
                "content_profile.table_missing", "Content profile table not found.", "tableId"
            ) from error
        fields = definition.get("fields")
        if not isinstance(fields, list):
            raise _error("content_profile.schema_invalid", "Schema fields are invalid.")
        types: dict[str, str] = {}
        for field in fields:
            if isinstance(field, dict):
                identity = field.get("identity")
                field_id = identity.get("fieldId") if isinstance(identity, dict) else None
                data_type = field.get("logicalType")
                if isinstance(field_id, str) and isinstance(data_type, str):
                    types[field_id] = data_type

        selected = [profile.title_field_id, profile.body_field_id]
        if profile.summary_field_id is not None:
            selected.append(profile.summary_field_id)
        selected.extend(profile.searchable_field_ids)
        missing = next((field_id for field_id in selected if field_id not in types), None)
        if missing is not None:
            raise _error(
                "content_profile.field_missing",
                "Content profile field is missing from SchemaCore.",
                missing,
            )
        if types[profile.title_field_id] not in _TEXT_TYPES:
            raise _error(
                "content_profile.title_type_invalid",
                "Title field must be text-like.",
                "titleFieldId",
            )
        if types[profile.body_field_id] not in _BODY_TYPES:
            raise _error(
                "content_profile.body_type_invalid",
                "Body field must be richText or longText.",
                "bodyFieldId",
            )
        if (
            profile.summary_field_id is not None
            and types[profile.summary_field_id] not in _TEXT_TYPES
        ):
            raise _error(
                "content_profile.summary_type_invalid",
                "Summary field must be text-like.",
                "summaryFieldId",
            )
        if len(set(profile.searchable_field_ids)) != len(profile.searchable_field_ids):
            raise _error(
                "content_profile.search_field_duplicate",
                "Searchable fields must be unique.",
                "searchableFieldIds",
            )
        if any(types[field_id] not in _TEXT_TYPES for field_id in profile.searchable_field_ids):
            raise _error(
                "content_profile.search_field_invalid",
                "Searchable fields must be text-like.",
                "searchableFieldIds",
            )

    async def _validate_record(self, table_id: str, record_id: str) -> None:
        try:
            rows = await self._product_data.read_rows(
                table_id=table_id,
                row_ids=[record_id],
            )
        except Exception as error:
            raise _error(
                "record_document_link.record_lookup_failed",
                "Record could not be validated.",
            ) from error
        if len(rows) != 1:
            raise _error(
                "record_document_link.record_missing",
                "Record does not exist.",
                "recordId",
            )

    async def _one(self, namespace: str, logical_id: str) -> MetadataRecord | None:
        rows = await self._metadata.read(MetadataQuery(namespace, keys=(logical_id,)))
        if len(rows) > 1:
            raise _error("content_model.storage_invalid", "Duplicate metadata identity.")
        return rows[0] if rows else None

    async def _upsert(
        self,
        namespace: str,
        logical_id: str,
        values: JsonObject,
        expected_revision: str | None,
        idempotency_key: str,
    ) -> MetadataRecord:
        try:
            return await self._metadata.write(
                MetadataWrite(
                    namespace=namespace,
                    logical_id=logical_id,
                    values=values,
                    expected_revision=expected_revision,
                    idempotency_key=idempotency_key,
                )
            )
        except MetadataConflictError as error:
            raise _conflict("content_model.edit_conflict") from error
        except Exception as error:
            raise _error("content_model.persistence_failed", "Metadata write failed.") from error

    async def _delete(
        self,
        namespace: str,
        logical_id: str,
        expected_revision: str,
        idempotency_key: str,
    ) -> None:
        current = await self._one(namespace, logical_id)
        if current is None:
            raise _error("content_model.not_found", "Metadata item not found.")
        if _optional_revision(current) != expected_revision:
            raise _conflict("content_model.edit_conflict")
        try:
            await self._metadata.delete(
                MetadataDelete(
                    namespace=namespace,
                    logical_id=logical_id,
                    expected_revision=expected_revision,
                    idempotency_key=idempotency_key,
                )
            )
        except MetadataConflictError as error:
            raise _conflict("content_model.edit_conflict") from error
        except Exception as error:
            raise _error("content_model.persistence_failed", "Metadata delete failed.") from error


def _profile_snapshot(row: MetadataRecord) -> ContentProfileSnapshot:
    try:
        return ContentProfileSnapshot(
            profile=ContentProfile.model_validate(row.values), revision=row.revision
        )
    except ValidationError as error:
        raise _error("content_model.storage_invalid", "Stored profile is invalid.") from error


def _link_snapshot(row: MetadataRecord) -> RecordDocumentLinkSnapshot:
    try:
        return RecordDocumentLinkSnapshot(
            link=RecordDocumentLink.model_validate(row.values), revision=row.revision
        )
    except ValidationError as error:
        raise _error("content_model.storage_invalid", "Stored link is invalid.") from error


def _optional_revision(row: MetadataRecord | None) -> str | None:
    return None if row is None else row.revision


def _conflict(code: str) -> ContentModelError:
    return ContentModelError("Content metadata changed elsewhere.", code=code)


def _error(code: str, message: str, path: str | None = None) -> ContentModelError:
    return ContentModelError(message, code=code, path=path)


__all__ = ["ContentModelError", "ContentModelService"]
