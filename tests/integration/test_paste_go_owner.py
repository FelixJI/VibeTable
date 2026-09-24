"""Compare the migrated Go paste owner with the frozen Python preview oracle."""

from __future__ import annotations

from pathlib import Path

import pytest

from backend.adapters.pocketbase.client import PocketBaseClient
from backend.adapters.pocketbase.data_io import PocketBaseLocalAuth, PocketBasePasteReadPort
from backend.adapters.pocketbase.mutation import PocketBaseBulkMutationClient
from backend.adapters.pocketbase.transport import PocketBaseConfig, StdlibPocketBaseTransport
from backend.contracts.data_profile import collection_profile_from_definition
from backend.contracts.paste import PreviewPasteParams
from tests.backend.application.paste_oracle import PasteService
from tests.integration.packaged_sidecar_matrix import (
    CLAIM_ID,
    FENCE_EPOCH,
    SESSION_EPOCH,
    WORKSPACE_ID,
    Sidecar,
    _create_field,
    _create_table,
    _create_v2_workspace,
    _recommended_field_draft,
    _workspace_v2_request,
)
from tests.integration.test_data_io_interoperability_roundtrip import (
    source_sidecar_binary as source_sidecar_binary,
)


@pytest.mark.integration
@pytest.mark.asyncio
async def test_go_paste_matches_frozen_python_preview_and_consumes_once(
    tmp_path: Path, source_sidecar_binary: Path
) -> None:
    sidecar = Sidecar(
        source_sidecar_binary,
        _create_v2_workspace(tmp_path / "workspace"),
        workspace_identity={
            "VIBETABLE_WORKSPACE_ID": WORKSPACE_ID,
            "VIBETABLE_WORKSPACE_SESSION_EPOCH": str(SESSION_EPOCH),
            "VIBETABLE_WORKSPACE_FENCE_EPOCH": str(FENCE_EPOCH),
            "VIBETABLE_WORKSPACE_CLAIM_ID": CLAIM_ID,
        },
    )
    try:
        sidecar.start()
        table = _create_table(sidecar, "paste_owner", "paste-owner-create")
        table_id = table["tableId"]
        fields: list[str] = []
        for logical_type in ("text", "number", "json"):
            draft = _recommended_field_draft(sidecar, table_id, logical_type, logical_type)
            if logical_type == "json":
                settings = sidecar.request(
                    "GET", f"/api/vibetable/v2/field-settings/{table_id}"
                ).json()
                capability = next(
                    item for item in settings["capabilities"] if item["logicalType"] == "json"
                )
                draft["json"] = capability["recommended"]["json"]
            field = _create_field(
                sidecar,
                table,
                draft,
                f"paste-owner-{logical_type}",
            )["definition"]
            fields.append(field["identity"]["physicalName"])
        config = PocketBaseConfig(
            base_url=f"http://{sidecar.address}", session_secret=sidecar.secret
        )
        client = PocketBaseClient(
            transport=StdlibPocketBaseTransport(config), session_secret=sidecar.secret
        )
        definition = await client.describe_table(table_id)
        profile = collection_profile_from_definition(definition)
        profiles = {table_id: profile}
        auth = PocketBaseLocalAuth()
        oracle = PasteService(
            client=PocketBasePasteReadPort(
                client=client, profiles=profiles, definitions={table_id: definition}
            ),
            auth=auth,
            bulk=PocketBaseBulkMutationClient(client=client, auth=auth),
            profiles=profiles,
            project="local",
        )
        # These inputs and the old oracle precede the Go implementation. Compare
        # the entire public plan apart from its intentionally random token/clock.
        sequence = 1
        insert_token = ""
        valid_values = ("雪😀", "0", '{"enabled":false,"items":[0,null]}')
        cases = [(valid_values, {"rowKeys": []}), (("", "", "{bad"), {"rowKeys": []})]
        cases.extend((valid_values, selection) for selection in ([], None, False, 0, ""))
        for values, selection in cases:
            params = PreviewPasteParams.model_validate(
                {
                    "collection": table_id,
                    "schemaRevision": profile.capability_hash,
                    "selection": selection,
                    "startCell": {"rowKey": None, "column": fields[0]},
                    "cells": [
                        [
                            {
                                "rowIndex": 0,
                                "columnIndex": index,
                                "column": name,
                                "rawValue": value,
                                "parsedValue": "untrusted",
                            }
                            for index, (name, value) in enumerate(zip(fields, values, strict=True))
                        ]
                    ],
                }
            )
            expected = (await oracle.preview(params)).model_dump(mode="json", by_alias=True)
            response = sidecar.request(
                "POST",
                "/api/vibetable/v2/product/rpc",
                json_body=_workspace_v2_request(
                    sequence, "table.previewPaste", params.model_dump(mode="json", by_alias=True)
                ),
            ).json()
            sequence += 1
            assert "error" not in response, response
            actual = response["result"]
            assert actual["token"]["consumed"] is False
            assert {key: value for key, value in actual.items() if key != "token"} == {
                key: value for key, value in expected.items() if key != "token"
            }
            if not insert_token:
                insert_token = actual["token"]["token"]
        apply_params = {
            "collection": table_id,
            "token": insert_token,
            "idempotencyKey": "paste-owner-apply",
        }
        committed = sidecar.request(
            "POST",
            "/api/vibetable/v2/product/rpc",
            json_body=_workspace_v2_request(sequence, "table.applyPaste", apply_params),
        ).json()
        assert "error" not in committed, committed
        assert committed["result"]["outcome"] == "committed"
        created = committed["result"]["createdRowKeys"]
        assert len(created) == 1
        rows = await client.read_rows(table_id=table_id, row_ids=created)
        assert rows[0][fields[0]] == "雪😀"
        assert rows[0][fields[1]] == 0
        assert rows[0][fields[2]] == {"enabled": False, "items": [0, None]}
        repeated = sidecar.request(
            "POST",
            "/api/vibetable/v2/product/rpc",
            json_body=_workspace_v2_request(sequence + 1, "table.applyPaste", apply_params),
        ).json()
        assert repeated["error"]["code"] == -32040
        assert repeated["error"]["data"]["code"] == "paste_token_consumed"
        # Compare update/no-op/invalid plans against the same persisted row,
        # including its authoritative revision guard and empty-number semantics.
        for offset, values in enumerate(
            (("雪😀", "0", '{"enabled":false,"items":[0,null]}'), ("changed", "", "{bad")),
            start=2,
        ):
            body = params.model_dump(mode="json", by_alias=True)
            body["selection"] = {"rowKeys": created}
            body["startCell"]["rowKey"] = created[0]
            for cell, value in zip(body["cells"][0], values, strict=True):
                cell["rawValue"] = value
            update = PreviewPasteParams.model_validate(body)
            expected = (await oracle.preview(update)).model_dump(mode="json", by_alias=True)
            response = sidecar.request(
                "POST",
                "/api/vibetable/v2/product/rpc",
                json_body=_workspace_v2_request(sequence + offset, "table.previewPaste", body),
            ).json()
            assert "error" not in response, response
            assert {key: value for key, value in response["result"].items() if key != "token"} == {
                key: value for key, value in expected.items() if key != "token"
            }
    finally:
        sidecar.stop()
