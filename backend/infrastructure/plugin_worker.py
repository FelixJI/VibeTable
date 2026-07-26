"""Closed local Worker and bulk-mutation infrastructure.

Plugin JavaScript never runs in the backend process.  The production adapter
uses a short-lived Node subprocess, a module-less VM realm and an explicit
line-delimited capability protocol.  Node starts with its permission model on,
no filesystem/network object is injected, and the child environment contains
no application credentials.
"""

from __future__ import annotations

import asyncio
import json
import os
import re
import shutil
from collections.abc import Awaitable, Callable
from dataclasses import dataclass, field
from typing import Any, cast

from backend.contracts.data_profile import collection_profile_from_definition
from backend.contracts.plugin import (
    ConfirmationPreview,
    MutationPlan,
    PluginPrivateSetting,
    PluginProgress,
    PluginResult,
    PluginRisk,
)
from backend.contracts.query import TableQuery
from backend.infrastructure.plugin_package import read_plugin_package_member
from backend.infrastructure.plugin_worker_runner import RUNNER_SOURCE

_STORAGE_KEY = re.compile(r"^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$")


class PluginWorkerError(RuntimeError):
    """Safe, diagnostic failure at the local plugin isolation boundary."""


@dataclass(frozen=True)
class _ResolvedWorker:
    project_key: str
    plugin_id: str
    permissions: dict[str, Any]
    source: str


class NodePluginWorkerAdapter:
    """Execute a pre-built ESM Worker behind closed host capabilities.

    A package is resolved from the installed project snapshot, not from a path
    supplied by JavaScript.  Every invocation gets a fresh process.  This is
    slower than a pool but gives deterministic termination and prevents state
    or authority from leaking between plugins.
    """

    def __init__(
        self,
        *,
        store: Any,
        profiles: dict[str, Any],
        client: Any,
        node_executable: str | None = None,
        timeout_seconds: float = 15.0,
        max_concurrency: int = 2,
        max_message_bytes: int = 1_048_576,
        max_capability_calls: int = 64,
        file_adapter: Any | None = None,
    ) -> None:
        self._store = store
        self._profiles = profiles
        self._dynamic_profiles = not profiles
        self._client = client
        if timeout_seconds <= 0:
            raise ValueError("Worker timeout must be positive")
        if max_concurrency < 1:
            raise ValueError("Worker concurrency must be at least one")
        if max_message_bytes < 1024:
            raise ValueError("Worker message limit is too small")
        if max_capability_calls < 1:
            raise ValueError("Worker capability-call limit must be at least one")
        self._node = node_executable or shutil.which("node")
        self._timeout = timeout_seconds
        self._semaphore = asyncio.Semaphore(max_concurrency)
        self._max_message_bytes = max_message_bytes
        self._max_capability_calls = max_capability_calls
        self._file_adapter = file_adapter
        self._active_processes: dict[str, asyncio.subprocess.Process] = {}

    @property
    def available(self) -> bool:
        """Whether this host can attempt the fail-closed Node boundary."""

        return self._node is not None

    async def prepare(
        self,
        worker_entry: str,
        context: dict[str, Any],
        input_payload: dict[str, Any],
        *,
        execution: dict[str, Any] | None = None,
    ) -> dict[str, Any]:
        return await self._invoke(
            "prepare", worker_entry, context, input_payload, execution=execution
        )

    async def run(
        self,
        worker_entry: str,
        context: dict[str, Any],
        input_payload: dict[str, Any],
        *,
        execution: dict[str, Any] | None = None,
    ) -> dict[str, Any]:
        return await self._invoke("run", worker_entry, context, input_payload, execution=execution)

    async def _invoke(
        self,
        method: str,
        worker_entry: str,
        context: dict[str, Any],
        payload: dict[str, Any],
        *,
        execution: dict[str, Any] | None,
    ) -> dict[str, Any]:
        node = self._node
        if node is None:
            raise PluginWorkerError("local plugin execution is unavailable: Node.js was not found")
        resolved = self._resolve(worker_entry, context, execution)
        invocation = {
            "type": "invoke",
            "method": method,
            "source": resolved.source,
            "payload": payload,
        }
        self._bounded_json(invocation, "Worker invocation")
        async with self._semaphore:
            try:
                async with asyncio.timeout(self._timeout):
                    result = await self._run_process(
                        node, invocation, resolved, context, execution or {}
                    )
            except TimeoutError as exc:
                raise PluginWorkerError("plugin Worker timed out and was terminated") from exc
        if not isinstance(result, dict):
            raise PluginWorkerError("plugin Worker result must be a JSON object")
        if result.get("contract") == "vibetable.mutation-plan.v1":
            await self._validate_mutation_plan(resolved, context, result)
        return result

    async def _run_process(
        self,
        node: str,
        invocation: dict[str, Any],
        resolved: _ResolvedWorker,
        context: dict[str, Any],
        execution: dict[str, Any],
    ) -> Any:
        process = await asyncio.create_subprocess_exec(
            node,
            "--permission",
            "--experimental-vm-modules",
            "--max-old-space-size=64",
            "-e",
            RUNNER_SOURCE,
            stdin=asyncio.subprocess.PIPE,
            stdout=asyncio.subprocess.PIPE,
            stderr=asyncio.subprocess.PIPE,
            env=self._child_environment(),
            limit=self._max_message_bytes + 1,
        )
        assert process.stdin is not None
        assert process.stdout is not None
        run_id = execution.get("runId")
        if isinstance(run_id, str) and run_id:
            self._active_processes[run_id] = process
        try:
            await self._write_message(process, invocation)
            calls = 0
            while True:
                message = await self._read_message(process)
                kind = message.get("type")
                if kind == "result":
                    return message.get("value")
                if kind == "error":
                    raise PluginWorkerError(
                        f"plugin Worker failed: {message.get('error', 'unknown error')}"
                    )
                if kind != "capability" or not isinstance(message.get("id"), int):
                    raise PluginWorkerError("plugin Worker emitted an invalid protocol message")
                calls += 1
                if calls > self._max_capability_calls:
                    raise PluginWorkerError("plugin Worker exceeded its capability-call limit")
                try:
                    value = await self._dispatch_capability(
                        resolved,
                        context,
                        execution,
                        message.get("name"),
                        message.get("args"),
                    )
                    response = {
                        "type": "capabilityResult",
                        "id": message["id"],
                        "ok": True,
                        "value": value,
                    }
                except Exception as exc:
                    response = {
                        "type": "capabilityResult",
                        "id": message["id"],
                        "ok": False,
                        "error": str(exc),
                    }
                await self._write_message(process, response)
        finally:
            if isinstance(run_id, str) and self._active_processes.get(run_id) is process:
                self._active_processes.pop(run_id, None)
            if process.returncode is None:
                process.terminate()
                try:
                    await asyncio.wait_for(process.wait(), timeout=1)
                except TimeoutError:
                    process.kill()
                    await process.wait()

    async def cancel(self, run_id: str) -> bool:
        """Terminate the isolated process for a cancellable local run."""

        process = self._active_processes.get(run_id)
        if process is None or process.returncode is not None:
            return False
        process.terminate()
        try:
            await asyncio.wait_for(process.wait(), timeout=1)
        except TimeoutError:
            process.kill()
            await process.wait()
        return True

    async def _dispatch_capability(
        self,
        resolved: _ResolvedWorker,
        context: dict[str, Any],
        execution: dict[str, Any],
        name: Any,
        args: Any,
    ) -> Any:
        if name == "context.read":
            return context
        if name == "data.read":
            return await self._data_read(resolved, context, args)
        if name == "data.mutate":
            raise PluginWorkerError(
                "data.mutate cannot write directly; return a mutation plan from the action"
            )
        if name in {"file.pickRead", "file.pickWrite", "file.read", "file.write"}:
            return await self._file_capability(resolved, execution, name, args)
        if name in {
            "storage.private.get",
            "storage.private.set",
            "storage.private.delete",
        }:
            return self._storage(resolved, name, args)
        if name == "ui.emitResult":
            PluginResult.model_validate(args)
            return None
        if name == "ui.reportProgress":
            progress = PluginProgress.model_validate(args)
            reporter = execution.get("_hostReporter")
            if reporter is None:
                raise PluginWorkerError("plugin progress reporter is unavailable")
            await reporter.report(
                done=progress.current,
                total=progress.total,
                message=progress.message,
            )
            cancel = execution.get("_hostCancel")
            return {"cancelRequested": bool(getattr(cancel, "cancelled", False))}
        raise PluginWorkerError(f"plugin capability {name!r} is unavailable")

    async def _file_capability(
        self,
        resolved: _ResolvedWorker,
        execution: dict[str, Any],
        name: str,
        args: Any,
    ) -> Any:
        if self._file_adapter is None or not bool(getattr(self._file_adapter, "available", True)):
            raise PluginWorkerError("host file picker is unavailable")
        if not isinstance(args, dict):
            raise PluginWorkerError("plugin file capability arguments must be an object")
        declared = resolved.permissions.get("files", [])
        if not isinstance(declared, list):
            declared = []
        if name == "file.pickRead":
            if "pickRead" not in declared:
                raise PluginWorkerError("plugin did not declare file.pickRead")
            return await self._file_adapter.pick_read(execution, args)
        if name == "file.pickWrite":
            if "pickWrite" not in declared:
                raise PluginWorkerError("plugin did not declare file.pickWrite")
            return await self._file_adapter.pick_write(execution, args)
        grant_id = args.get("grantId")
        if not isinstance(grant_id, str) or not grant_id:
            raise PluginWorkerError("plugin file grantId is required")
        if name == "file.read":
            if "pickRead" not in declared:
                raise PluginWorkerError("plugin did not declare file.pickRead")
            return self._file_adapter.read(execution, grant_id)
        if "pickWrite" not in declared:
            raise PluginWorkerError("plugin did not declare file.pickWrite")
        encoded = args.get("base64")
        if not isinstance(encoded, str):
            raise PluginWorkerError("plugin file content is required")
        self._file_adapter.write(execution, grant_id, encoded)
        return None

    async def _data_read(
        self,
        resolved: _ResolvedWorker,
        context: dict[str, Any],
        request: Any,
    ) -> dict[str, Any]:
        if not isinstance(request, dict):
            raise PluginWorkerError("data.read request must be an object")
        allowed_keys = {"collection", "fields", "filter", "cursor", "pageSize"}
        if set(request) - allowed_keys:
            raise PluginWorkerError("data.read request contains unsupported fields")
        collection = request.get("collection")
        if not isinstance(collection, str) or not collection:
            raise PluginWorkerError("data.read collection is required")
        grant = self._read_grant(resolved.permissions, context, collection)
        if grant is None:
            raise PluginWorkerError(f"collection {collection!r} was not declared for read")
        profile = await self._profile(collection)
        requested_fields = request.get("fields")
        if not isinstance(requested_fields, list) or not all(
            isinstance(field, str) for field in requested_fields
        ):
            raise PluginWorkerError("data.read fields must be a string array")
        allowed_fields = self._allowed_fields(grant, profile)
        fields = list(profile.fields) if requested_fields == ["*"] else requested_fields
        denied = set(fields) - allowed_fields
        if denied:
            raise PluginWorkerError(
                f"data.read fields were not declared: {', '.join(sorted(denied))}"
            )
        if request.get("filter") not in (None, {}):
            raise PluginWorkerError("data.read filter is unavailable in plugin API v1")
        page_size = request.get("pageSize", 100)
        if not isinstance(page_size, int) or isinstance(page_size, bool):
            raise PluginWorkerError("data.read pageSize must be an integer")
        page_size = min(max(page_size, 1), 200)
        cursor = request.get("cursor")
        if cursor in (None, ""):
            offset = 0
        elif isinstance(cursor, (str, int)) and not isinstance(cursor, bool):
            try:
                offset = int(cursor)
            except ValueError as exc:
                raise PluginWorkerError("data.read cursor is invalid") from exc
        else:
            raise PluginWorkerError("data.read cursor is invalid")
        if offset < 0:
            raise PluginWorkerError("data.read cursor is invalid")
        legacy_read = getattr(self._client, "read_items_with_fields", None)
        if callable(legacy_read):
            items, _meta, _plan = await cast(
                Callable[
                    [Any, TableQuery, list[str]],
                    Awaitable[tuple[list[dict[str, Any]], Any, Any]],
                ],
                legacy_read,
            )(
                profile,
                TableQuery(offset=offset, limit=page_size),
                fields,
            )
        else:
            page = await self._client.query_page(
                table_id=profile.collection,
                query=TableQuery(offset=offset, limit=page_size).model_dump(
                    by_alias=True, mode="json"
                ),
            )
            items = [{field: row.get(field) for field in fields} for row in page.rows]
        value = {
            "items": items,
            "nextCursor": str(offset + len(items)) if len(items) == page_size else None,
        }
        self._bounded_json(value, "data.read response")
        return value

    def _storage(self, resolved: _ResolvedWorker, name: str, args: Any) -> Any:
        if resolved.permissions.get("privateStorage") is not True:
            raise PluginWorkerError("plugin did not declare privateStorage")
        if not isinstance(args, dict) or not isinstance(args.get("key"), str):
            raise PluginWorkerError("private storage key is required")
        key = args["key"]
        if not _STORAGE_KEY.fullmatch(key):
            raise PluginWorkerError("private storage key is invalid")
        current = self._store.get_private_setting(resolved.project_key, resolved.plugin_id, key)
        if name == "storage.private.get":
            return None if current is None else current.value
        value = None if name == "storage.private.delete" else args.get("value")
        self._bounded_json(value, "private storage value", limit=65_536)
        revision = 1 if current is None else current.revision + 1
        self._store.save_private_setting(
            PluginPrivateSetting(
                project_key=resolved.project_key,
                plugin_id=resolved.plugin_id,
                setting_key=key,
                value=value,
                revision=revision,
            ),
            expected_revision=None if current is None else current.revision,
        )
        return None

    async def _validate_mutation_plan(
        self,
        resolved: _ResolvedWorker,
        context: dict[str, Any],
        raw: dict[str, Any],
    ) -> None:
        try:
            plan = MutationPlan.model_validate(raw)
        except ValueError as exc:
            raise PluginWorkerError("plugin returned an invalid mutation plan") from exc
        query_snapshot = context.get("querySnapshot")
        expected_schema_revision = (
            query_snapshot.get("schemaRevision")
            if isinstance(query_snapshot, dict)
            and isinstance(query_snapshot.get("schemaRevision"), str)
            else None
        )
        profile = await self._profile(
            plan.collection,
            expected_schema_revision=expected_schema_revision,
        )
        if plan.preview.affected_count != len(plan.operations):
            raise PluginWorkerError("mutation preview affectedCount must match the operation count")
        grant = self._write_grant(resolved.permissions, context, plan.collection)
        if grant is None:
            raise PluginWorkerError(f"collection {plan.collection!r} was not declared for mutation")
        allowed_fields = self._allowed_fields(grant, profile)
        operations = set(grant.get("operations", []))
        for operation in plan.operations:
            if operation.kind not in operations:
                raise PluginWorkerError(f"mutation operation {operation.kind!r} was not declared")
            denied = set(operation.values) - allowed_fields
            if denied:
                raise PluginWorkerError(
                    f"mutation fields were not declared: {', '.join(sorted(denied))}"
                )
            profile.require_fields(set(operation.values), operation=operation.kind)

    async def _profile(
        self,
        collection: str,
        *,
        expected_schema_revision: str | None = None,
    ) -> Any:
        profile = self._profiles.get(collection)
        if profile is not None and (
            not self._dynamic_profiles
            or (
                expected_schema_revision is not None
                and profile.schema_revision == expected_schema_revision
            )
        ):
            return profile
        try:
            async with asyncio.timeout(self._timeout):
                definition = await self._client.describe_table(collection)
            profile = collection_profile_from_definition(definition)
        except TimeoutError as exc:
            raise PluginWorkerError(
                f"collection {collection!r} schema validation timed out"
            ) from exc
        except Exception as exc:
            raise PluginWorkerError(
                f"collection {collection!r} is outside the product schema"
            ) from exc
        self._profiles[collection] = profile
        return profile

    def _resolve(
        self,
        worker_entry: str,
        context: dict[str, Any],
        execution: dict[str, Any] | None,
    ) -> _ResolvedWorker:
        project_key = context.get("projectKey")
        if not isinstance(project_key, str) or not project_key:
            raise PluginWorkerError("Worker context has no projectKey")
        if not isinstance(execution, dict):
            raise PluginWorkerError("plugin Worker execution identity is unavailable")
        plugin_id = execution.get("pluginId")
        plugin_version = execution.get("pluginVersion")
        package_hash = execution.get("packageHash")
        action_id = execution.get("actionId")
        if not all(
            isinstance(value, str) and value
            for value in (plugin_id, plugin_version, package_hash, action_id)
        ):
            raise PluginWorkerError("plugin Worker execution identity is invalid")
        if execution.get("projectKey") != project_key:
            raise PluginWorkerError("plugin Worker project identity does not match context")
        installation = self._store.get_installation(project_key, plugin_id)
        if installation is None:
            raise PluginWorkerError("plugin installation is unavailable")
        if installation.version != plugin_version or installation.package_hash != package_hash:
            raise PluginWorkerError("plugin Worker package identity is stale")
        action = next(
            (item for item in installation.manifest.actions if item.action_id == action_id),
            None,
        )
        if action is None or action.worker_entry != worker_entry:
            raise PluginWorkerError("plugin action does not own the requested Worker entry")
        revisions = self._store.list_package_revisions(project_key, installation.plugin_id)
        revision = next(
            (
                item
                for item in revisions
                if item.state == "current"
                and item.version == installation.version
                and item.package_hash == installation.package_hash
            ),
            None,
        )
        if revision is None:
            raise PluginWorkerError("installed plugin package revision is unavailable")
        try:
            source_bytes = read_plugin_package_member(revision.local_path, worker_entry)
            source = source_bytes.decode("utf-8")
        except (OSError, UnicodeError, ValueError) as exc:
            raise PluginWorkerError("plugin Worker entry could not be loaded") from exc
        if len(source_bytes) > self._max_message_bytes:
            raise PluginWorkerError("plugin Worker entry exceeds the size limit")
        return _ResolvedWorker(
            project_key=project_key,
            plugin_id=installation.plugin_id,
            permissions=installation.manifest.permissions,
            source=source,
        )

    @staticmethod
    def _read_grant(
        permissions: dict[str, Any], context: dict[str, Any], collection: str
    ) -> dict[str, Any] | None:
        grants = permissions.get("data", [])
        if not isinstance(grants, list):
            return None
        active = context.get("collection")
        for grant in grants:
            if not isinstance(grant, dict) or "read" not in grant.get("operations", []):
                continue
            declared = grant.get("collection")
            if declared == collection or (declared == "$active" and active == collection):
                return grant
        return None

    @staticmethod
    def _write_grant(
        permissions: dict[str, Any], context: dict[str, Any], collection: str
    ) -> dict[str, Any] | None:
        grants = permissions.get("data", [])
        if not isinstance(grants, list):
            return None
        active = context.get("collection")
        for grant in grants:
            if not isinstance(grant, dict):
                continue
            operations = grant.get("operations", [])
            if not isinstance(operations, list) or not {"create", "update"} & set(operations):
                continue
            declared = grant.get("collection")
            if declared == collection or (declared == "$active" and active == collection):
                return grant
        return None

    @staticmethod
    def _allowed_fields(grant: dict[str, Any], profile: Any) -> set[str]:
        declared = grant.get("fields", [])
        if not isinstance(declared, list):
            return set()
        if "$configured" in declared or "*" in declared:
            return set(profile.fields)
        return set(declared) & set(profile.fields)

    def _bounded_json(self, value: Any, label: str, *, limit: int | None = None) -> bytes:
        try:
            encoded = json.dumps(
                value,
                ensure_ascii=False,
                separators=(",", ":"),
                allow_nan=False,
            ).encode("utf-8")
        except (TypeError, ValueError) as exc:
            raise PluginWorkerError(f"{label} is not valid JSON") from exc
        if len(encoded) > (limit or self._max_message_bytes):
            raise PluginWorkerError(f"{label} exceeds the size limit")
        return encoded

    async def _write_message(
        self, process: asyncio.subprocess.Process, message: dict[str, Any]
    ) -> None:
        assert process.stdin is not None
        process.stdin.write(self._bounded_json(message, "Worker protocol message") + b"\n")
        await process.stdin.drain()

    async def _read_message(self, process: asyncio.subprocess.Process) -> dict[str, Any]:
        assert process.stdout is not None
        try:
            line = await process.stdout.readline()
        except (ValueError, asyncio.LimitOverrunError) as exc:
            raise PluginWorkerError("plugin Worker message exceeds the size limit") from exc
        if not line:
            stderr = b""
            if process.stderr is not None:
                stderr = await process.stderr.read(4096)
            detail = stderr.decode("utf-8", errors="replace").strip()
            raise PluginWorkerError(
                "plugin Worker exited before returning a result" + (f": {detail}" if detail else "")
            )
        if len(line) > self._max_message_bytes:
            raise PluginWorkerError("plugin Worker message exceeds the size limit")
        try:
            value = json.loads(line)
        except json.JSONDecodeError as exc:
            raise PluginWorkerError("plugin Worker emitted invalid JSON") from exc
        if not isinstance(value, dict):
            raise PluginWorkerError("plugin Worker emitted an invalid protocol message")
        return value

    @staticmethod
    def _child_environment() -> dict[str, str]:
        # Node needs SystemRoot on Windows to initialize.  No PATH, tokens,
        # proxy settings, HOME, or application variables cross the boundary.
        environment: dict[str, str] = {}
        for name in ("SystemRoot", "WINDIR"):
            value = os.environ.get(name)
            if value:
                environment[name] = value
        return environment


@dataclass
class InMemoryPluginWorkerAdapter:
    prepare_results: dict[str, dict[str, Any]] = field(default_factory=dict)
    run_results: dict[str, dict[str, Any]] = field(default_factory=dict)
    trace: list[str] | None = None
    executions: list[dict[str, Any]] = field(default_factory=list)

    @property
    def available(self) -> bool:
        return True

    async def prepare(
        self,
        worker_entry: str,
        context: dict[str, Any],
        input_payload: dict[str, Any],
        *,
        execution: dict[str, Any] | None = None,
    ) -> dict[str, Any]:
        del context, input_payload
        if execution is not None:
            self.executions.append(execution)
        if self.trace is not None:
            self.trace.append("worker.prepare")
        return self.prepare_results.get(worker_entry, {})

    async def run(
        self,
        worker_entry: str,
        context: dict[str, Any],
        input_payload: dict[str, Any],
        *,
        execution: dict[str, Any] | None = None,
    ) -> dict[str, Any]:
        del context, input_payload
        if execution is not None:
            self.executions.append(execution)
        if self.trace is not None:
            self.trace.append("worker.run")
        return self.run_results[worker_entry]


@dataclass
class InMemoryHostConfirmationAdapter:
    decisions: list[bool] = field(default_factory=list)
    trace: list[str] | None = None
    previews: list[ConfirmationPreview] = field(default_factory=list)

    async def confirm(
        self,
        preview: ConfirmationPreview,
        risk: PluginRisk,
        *,
        execution: dict[str, Any] | None = None,
    ) -> bool:
        del risk, execution
        if self.trace is not None:
            self.trace.append("host.confirm")
        self.previews.append(preview)
        return self.decisions.pop(0) if self.decisions else False


@dataclass
class InMemoryBulkMutationAdapter:
    result: dict[str, Any]
    trace: list[str] | None = None
    plans: list[MutationPlan] = field(default_factory=list)

    async def apply(self, plan: MutationPlan) -> dict[str, Any]:
        if self.trace is not None:
            self.trace.append("bulk.apply")
        self.plans.append(plan)
        return self.result


__all__ = [
    "InMemoryBulkMutationAdapter",
    "InMemoryHostConfirmationAdapter",
    "InMemoryPluginWorkerAdapter",
    "NodePluginWorkerAdapter",
    "PluginWorkerError",
]
