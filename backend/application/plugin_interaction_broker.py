"""Host-facing interaction broker independent of storage endpoint paths."""

from __future__ import annotations

from typing import Protocol

from backend.contracts.plugin import (
    CancelFlag,
    InteractionDecision,
    InteractionResolveResult,
    InteractionSnapshot,
)


class InteractionAdapter(Protocol):
    async def watch(self, run_id: str) -> InteractionSnapshot: ...

    async def resolve(
        self, run_id: str, interaction_id: str, decision: InteractionDecision
    ) -> InteractionResolveResult: ...

    async def request_cancel(self, run_id: str) -> CancelFlag: ...

    async def get(self, run_id: str) -> InteractionSnapshot: ...


class PluginInteractionBroker:
    def __init__(self, *, adapter: InteractionAdapter) -> None:
        self._adapter = adapter

    async def watch(self, run_id: str) -> InteractionSnapshot:
        return await self._adapter.watch(run_id)

    async def resolve(
        self, run_id: str, interaction_id: str, decision: InteractionDecision
    ) -> InteractionResolveResult:
        return await self._adapter.resolve(run_id, interaction_id, decision)

    async def request_cancel(self, run_id: str) -> CancelFlag:
        return await self._adapter.request_cancel(run_id)

    async def get(self, run_id: str) -> InteractionSnapshot:
        return await self._adapter.get(run_id)


__all__ = ["PluginInteractionBroker"]
