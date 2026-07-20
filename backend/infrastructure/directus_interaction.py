"""Authenticated adapter for the Directus plugin interaction bridge."""

from __future__ import annotations

from typing import Any, Literal
from urllib.parse import quote

from backend.contracts.plugin import (
    CancelFlag,
    ConfirmationPreview,
    InteractionDecision,
    InteractionResolveResult,
    InteractionSnapshot,
    PendingConfirmation,
    PluginProgress,
)


class DirectusInteractionAdapter:
    """Maps the public bridge wire contract to host-domain snapshots."""

    requires_host_notifications = True

    def __init__(self, *, transport: Any, auth: Any) -> None:
        self._transport = transport
        self._auth = auth

    async def register_run(self, *, run_id: str, plugin_id: str, action_id: str) -> None:
        await self._request(
            "POST",
            self._run_path(run_id),
            json_body={
                "contract": "vibetable.plugin-run.v1",
                "runId": run_id,
                "pluginId": plugin_id,
                "actionId": action_id,
            },
            expected_status=(201,),
        )

    async def watch(self, run_id: str) -> InteractionSnapshot:
        # The first public bridge contract is bounded short-polling.  The WPF
        # caller controls cadence and can always rebuild state with get().
        return await self.get(run_id)

    async def get(self, run_id: str) -> InteractionSnapshot:
        return _snapshot(_response_object(await self._request("GET", self._run_path(run_id))))

    async def resolve(
        self,
        run_id: str,
        interaction_id: str,
        decision: InteractionDecision,
    ) -> InteractionResolveResult:
        payload = _response_object(
            await self._request(
                "POST",
                f"{self._run_path(run_id)}/confirm/{_segment(interaction_id)}",
                json_body={"decision": "approve" if decision == "approved" else "reject"},
            )
        )
        raw_status = payload.get("status")
        raw_decision = payload.get("decision")
        status: Literal["resolved", "already-resolved", "expired"]
        mapped_decision: InteractionDecision | None
        if raw_status == "already-decided" and raw_decision == "expired":
            status = "expired"
            mapped_decision = None
        elif raw_status == "already-decided":
            status = "already-resolved"
            mapped_decision = raw_decision if raw_decision in {"approved", "rejected"} else None
        elif raw_status == "decided":
            status = "resolved"
            mapped_decision = raw_decision if raw_decision in {"approved", "rejected"} else decision
        else:
            raise ValueError("Directus plugin bridge returned an unknown decision status")
        return InteractionResolveResult(status=status, decision=mapped_decision)

    async def request_cancel(self, run_id: str) -> CancelFlag:
        await self._request("POST", f"{self._run_path(run_id)}/cancel", json_body={})
        return CancelFlag(cancel_requested=True)

    async def complete_run(self, run_id: str, terminal_hint: str) -> None:
        await self._request(
            "POST",
            f"{self._run_path(run_id)}/complete",
            json_body={"terminalHint": terminal_hint},
        )

    async def _request(self, method: str, path: str, **kwargs: Any) -> Any:
        token = await self._auth.access_token()
        return await self._transport.request(method, path, access_token=token, **kwargs)

    @staticmethod
    def _run_path(run_id: str) -> str:
        return f"/vibetable-plugin-bridge/runs/{_segment(run_id)}"


def _segment(value: str) -> str:
    return quote(value, safe="")


def _response_object(payload: Any) -> dict[str, Any]:
    if not isinstance(payload, dict) or not isinstance(payload.get("data"), dict):
        raise ValueError("Directus plugin bridge returned an invalid response")
    return payload["data"]


def _snapshot(data: dict[str, Any]) -> InteractionSnapshot:
    raw_caller = data.get("caller")
    caller: dict[str, Any] = raw_caller if isinstance(raw_caller, dict) else {}
    progress_data = data.get("progress")
    pending_data = data.get("pendingConfirmation")
    progress = (
        PluginProgress.model_validate(progress_data) if isinstance(progress_data, dict) else None
    )
    pending = None
    if isinstance(pending_data, dict):
        pending = PendingConfirmation(
            interaction_id=str(pending_data.get("interactionId", "")),
            risk=pending_data.get("risk", "write"),
            title=str(pending_data.get("title", "")),
            preview=ConfirmationPreview.model_validate(pending_data.get("preview", {})),
            expires_at=float(pending_data.get("expiresAt", 0)),
        )
    return InteractionSnapshot(
        run_id=str(data.get("runId", "")),
        project_key=str(caller.get("projectId", "")),
        plugin_id=str(data.get("pluginId", "")),
        action_id=str(data.get("actionId", "")),
        caller=str(caller.get("userId", "")),
        progress=progress,
        pending_confirmation=pending,
        cancel_requested=bool(data.get("cancelRequested", False)),
    )


__all__ = ["DirectusInteractionAdapter"]
