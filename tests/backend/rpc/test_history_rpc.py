from __future__ import annotations

import pytest

from backend.application.history_service import HistoryError
from backend.contracts.history import HistoryPage, ReadChangeSetsParams
from backend.rpc.dispatcher import CODE_HISTORY, RpcDispatcher, register_history_errors


@pytest.mark.asyncio
async def test_history_table_scope_round_trips_through_dispatcher() -> None:
    async def read(params: ReadChangeSetsParams) -> HistoryPage:
        return HistoryPage(
            collection=params.collection,
            scope=params.scope,
            item_id=params.item_id,
            field=params.field,
            total=0,
            capability_hash="cap",
            schema_revision="schema",
        )

    dispatcher = RpcDispatcher()
    dispatcher.register("history.readChangeSets", read, ReadChangeSetsParams)
    response = await dispatcher.dispatch(
        {
            "jsonrpc": "2.0",
            "id": "history-1",
            "method": "history.readChangeSets",
            "params": {"collection": "orders", "scope": "table", "search": "Ada"},
        }
    )
    assert response is not None
    assert response["result"]["scope"] == "table"
    assert response["result"]["itemId"] is None
    assert response["result"]["hasMore"] is False


@pytest.mark.asyncio
async def test_history_error_preserves_domain_code() -> None:
    async def fail(_params: ReadChangeSetsParams) -> None:
        raise HistoryError("preview expired", code="restore_token_expired")

    register_history_errors()
    dispatcher = RpcDispatcher()
    dispatcher.register("history.readChangeSets", fail, ReadChangeSetsParams)
    response = await dispatcher.dispatch(
        {
            "jsonrpc": "2.0",
            "id": "history-2",
            "method": "history.readChangeSets",
            "params": {"collection": "orders", "scope": "table"},
        }
    )
    assert response is not None
    assert response["error"]["code"] == CODE_HISTORY
    assert response["error"]["data"] == {
        "kind": "history_error",
        "message": "preview expired",
        "code": "restore_token_expired",
    }
