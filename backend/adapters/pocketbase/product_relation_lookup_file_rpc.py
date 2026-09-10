"""Managed-file product RPC module; Relation writes are owned by Go."""

from __future__ import annotations

import re
import uuid

from backend.adapters.pocketbase.product_rpc_support import (
    PocketBaseProductContext,
    ProductRpcHandler,
    _array,
    _result_object,
    _text,
)
from backend.contracts.product_rpc import JsonObject, ProductParams

_ROW_DIGEST = re.compile(r"^sha256:[0-9a-f]{64}$")


class ProductRelationLookupFileRpc:
    """Owns managed-file operations that still require the Python backend."""

    def __init__(self, context: PocketBaseProductContext) -> None:
        self._context = context
        self._handlers: dict[str, ProductRpcHandler] = {
            "file.token": self._create_file_token,
            "file.applyHostChange": self._apply_host_attachment_change,
            "file.saveHostFile": self._save_attachment_to_host,
        }
        self.methods = frozenset(self._handlers)

    async def invoke(self, method: str, params: ProductParams) -> JsonObject:
        try:
            handler = self._handlers[method]
        except KeyError as exc:
            raise ValueError(f"unknown relation/Lookup/file RPC method: {method}") from exc
        return await handler(params)

    async def _create_file_token(self, params: ProductParams) -> JsonObject:
        query = {
            name: _text(params.root, name)
            for name in ("tableId", "recordId", "fieldId", "storedName")
        }
        variant = params.root.get("variant")
        if variant is not None:
            if not isinstance(variant, str):
                raise ValueError("variant must be a string")
            query["variant"] = variant
        return _result_object(
            await self._context.transport.request(
                "GET",
                "/api/vibetable/v1/files/token",
                query=query,
                headers=dict(self._context.headers),
                expected_status=(200,),
            )
        )

    async def _apply_host_attachment_change(self, params: ProductParams) -> JsonObject:
        raw = params.root
        host_paths = _array(raw, "hostPaths")
        remove_names = _array(raw, "removeStoredNames")
        if (
            len(host_paths) > 32
            or len(remove_names) > 32
            or not all(isinstance(item, str) and item for item in host_paths)
            or not all(isinstance(item, str) and item for item in remove_names)
            or (not host_paths and not remove_names)
        ):
            raise ValueError("managed attachment change is invalid")
        expected_digest = _text(raw, "expectedDigest")
        if not _ROW_DIGEST.fullmatch(expected_digest):
            raise ValueError("expectedDigest is invalid")
        request_id = str(uuid.uuid4())
        upload_handles = [f"upload_{index}" for index in range(len(host_paths))]
        request = _result_object(
            {
                "contractVersion": "2.0",
                "requestId": request_id,
                "idempotencyKey": f"attachment:{request_id}",
                "tableId": _text(raw, "tableId"),
                "schemaRevision": _text(raw, "schemaRevision"),
                "operations": [
                    {
                        "kind": "setAttachments",
                        "recordId": _text(raw, "recordId"),
                        "fieldId": _text(raw, "fieldId"),
                        "uploadHandles": upload_handles,
                        "removeStoredNames": list(remove_names),
                    }
                ],
                "actor": {"type": "user", "id": "local-user", "displayName": None},
                "expectedRevision": None,
                "expectedDigest": expected_digest,
            }
        )
        if host_paths:
            result = await self._context.transport.request_multipart(
                "/api/vibetable/v1/mutations/apply",
                json_body=request,
                uploads=list(
                    zip(
                        upload_handles,
                        [item for item in host_paths if isinstance(item, str)],
                        strict=True,
                    )
                ),
                headers=dict(self._context.headers),
                expected_status=(200,),
            )
        else:
            result = await self._context.client.apply_mutation(request)
        return _result_object(result)

    async def _save_attachment_to_host(self, params: ProductParams) -> JsonObject:
        raw = params.root
        query = {
            name: _text(raw, name) for name in ("tableId", "recordId", "fieldId", "storedName")
        }
        variant = raw.get("variant")
        if variant is not None:
            if not isinstance(variant, str) or not variant:
                raise ValueError("variant must be a non-empty string")
            query["variant"] = variant
        token = _result_object(
            await self._context.transport.request(
                "GET",
                "/api/vibetable/v1/files/token",
                query=query,
                headers=dict(self._context.headers),
                expected_status=(200,),
            )
        )
        saved_bytes = await self._context.transport.download_to_file(
            "/api/vibetable/v1/attachments/download",
            query={"capability": _text(token, "downloadCapability")},
            target_path=_text(raw, "outputPath"),
            headers=dict(self._context.headers),
            expected_status=(200,),
        )
        return {"contractVersion": "2.0", "saved": True, "bytes": saved_bytes}
