"""Revisioned application service for independently authored Interface surfaces."""

from __future__ import annotations

import re

from pydantic import ValidationError

from backend.application.revisioned_metadata_port import (
    MetadataConflictError,
    MetadataDelete,
    MetadataQuery,
    MetadataRecord,
    MetadataWrite,
    RevisionedMetadataPort,
    json_object,
)
from backend.contracts.generated_workbench import (
    InterfaceAction,
    InterfaceCommitRequest,
    InterfaceDefinition,
    InterfaceDeleteResult,
    InterfaceElement,
    InterfaceListEntry,
    InterfaceListResult,
    InterfaceSnapshot,
)

_INTERFACE_ID = re.compile(r"^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$")
_STRUCTURAL_KINDS = frozenset({"section", "columns", "tabs"})
_BOUND_KINDS = frozenset({"metric", "chart", "record-list", "record-detail", "form"})
_ACTION_KINDS = frozenset({"form", "button", "navigation"})
_MAX_ELEMENTS = 200
_MAX_ELEMENT_DEPTH = 8


class SurfaceError(Exception):
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


class SurfaceService:
    """Persist each Interface definition as one optimistic-concurrency aggregate."""

    def __init__(
        self,
        *,
        metadata_port: RevisionedMetadataPort,
    ) -> None:
        self._metadata = metadata_port

    async def list(self) -> InterfaceListResult:
        rows = await self._metadata.read(MetadataQuery("interfaces"))
        snapshots = [_snapshot_from_row(row) for row in rows]
        return InterfaceListResult(
            items=sorted(
                [
                    InterfaceListEntry(
                        interface_id=snapshot.definition.interface_id,
                        name=snapshot.definition.name,
                        revision=snapshot.revision,
                    )
                    for snapshot in snapshots
                ],
                key=lambda item: (item.name.casefold(), item.interface_id),
            )
        )

    async def load(self, interface_id: str) -> InterfaceSnapshot:
        _require_interface_id(interface_id)
        row = await self._current(interface_id)
        if row is None:
            raise SurfaceError("Interface not found.", code="surface.not_found")
        return _snapshot_from_row(row)

    async def commit(self, request: InterfaceCommitRequest) -> InterfaceSnapshot:
        definition = request.definition
        validate_interface_definition(definition)
        if not request.idempotency_key:
            raise SurfaceError(
                "idempotencyKey is required.",
                code="surface.idempotency_key_required",
                path="idempotencyKey",
            )

        current = await self._current(definition.interface_id)
        actual_revision = _optional_revision(current)
        if request.expected_revision != actual_revision:
            raise SurfaceError(
                "Interface changed elsewhere.",
                code="surface.edit_conflict",
                path="expectedRevision",
            )

        try:
            item = await self._metadata.write(
                MetadataWrite(
                    namespace="interfaces",
                    logical_id=definition.interface_id,
                    values=json_object(definition.model_dump(by_alias=True, mode="json")),
                    expected_revision=request.expected_revision,
                    idempotency_key=request.idempotency_key,
                )
            )
        except Exception as error:
            raise _persistence_error(error) from error
        return _snapshot_from_row(item)

    async def delete(
        self,
        interface_id: str,
        expected_revision: str,
        idempotency_key: str,
    ) -> InterfaceDeleteResult:
        _require_interface_id(interface_id)
        if not expected_revision:
            raise SurfaceError(
                "expectedRevision is required.",
                code="surface.revision_required",
                path="expectedRevision",
            )
        if not idempotency_key:
            raise SurfaceError(
                "idempotencyKey is required.",
                code="surface.idempotency_key_required",
                path="idempotencyKey",
            )
        current = await self._current(interface_id)
        if current is None:
            raise SurfaceError("Interface not found.", code="surface.not_found")
        if _optional_revision(current) != expected_revision:
            raise SurfaceError(
                "Interface changed elsewhere.",
                code="surface.edit_conflict",
                path="expectedRevision",
            )
        try:
            await self._metadata.delete(
                MetadataDelete(
                    namespace="interfaces",
                    logical_id=interface_id,
                    expected_revision=expected_revision,
                    idempotency_key=idempotency_key,
                )
            )
        except Exception as error:
            raise _persistence_error(error) from error
        return InterfaceDeleteResult(interface_id=interface_id)

    async def _current(self, interface_id: str) -> MetadataRecord | None:
        rows = await self._metadata.read(MetadataQuery("interfaces", keys=(interface_id,)))
        if not rows:
            return None
        if len(rows) != 1:
            raise SurfaceError(
                "Interface storage returned duplicate identities.",
                code="surface.storage_invalid",
            )
        return rows[0]


def validate_interface_definition(definition: InterfaceDefinition) -> None:
    _require_interface_id(definition.interface_id)
    if not definition.name.strip():
        raise _invalid("surface.name_required", "Interface name is required.", "name")
    if not definition.pages:
        raise _invalid(
            "surface.pages_required", "An Interface requires at least one page.", "pages"
        )

    binding_ids = _unique_ids(
        [binding.binding_id for binding in definition.bindings],
        "bindings",
        "surface.binding_duplicate",
    )
    for index, binding in enumerate(definition.bindings):
        if not binding.query.table_id:
            raise _invalid(
                "surface.binding_source_required",
                "Binding source is required.",
                f"bindings.{index}.query.tableId",
            )
        if not binding.query.fields:
            raise _invalid(
                "surface.binding_fields_required",
                "Binding requires at least one field.",
                f"bindings.{index}.query.fields",
            )
        if len(set(binding.query.fields)) != len(binding.query.fields):
            raise _invalid(
                "surface.binding_field_duplicate",
                "Binding fields must be unique.",
                f"bindings.{index}.query.fields",
            )
        _unique_ids(
            [variable.variable_id for variable in binding.variables],
            f"bindings.{index}.variables",
            "surface.binding_variable_duplicate",
        )

    for binding_index, binding in enumerate(definition.bindings):
        for variable_index, variable in enumerate(binding.variables):
            path = f"bindings.{binding_index}.variables.{variable_index}"
            if variable.target_field_id not in binding.query.fields:
                raise _invalid(
                    "surface.binding_variable_target_invalid",
                    "Variable target must be a field selected by its binding query.",
                    f"{path}.targetFieldId",
                )
            if variable.source == "literal":
                if variable.source_binding_id is not None or variable.source_field_id is not None:
                    raise _invalid(
                        "surface.binding_variable_source_invalid",
                        "Literal variables cannot reference another binding.",
                        path,
                    )
                continue
            if not variable.source_binding_id or not variable.source_field_id:
                raise _invalid(
                    "surface.binding_variable_source_required",
                    "Selected-record variables require a source binding and field.",
                    path,
                )
            if variable.source_binding_id == binding.binding_id:
                raise _invalid(
                    "surface.binding_variable_cycle",
                    "A binding cannot depend on its own selected record.",
                    f"{path}.sourceBindingId",
                )
            source = next(
                (
                    candidate
                    for candidate in definition.bindings
                    if candidate.binding_id == variable.source_binding_id
                ),
                None,
            )
            if source is None:
                raise _invalid(
                    "surface.binding_variable_source_missing",
                    "Variable source binding does not exist.",
                    f"{path}.sourceBindingId",
                )
            if variable.source_field_id not in source.query.fields:
                raise _invalid(
                    "surface.binding_variable_source_field_invalid",
                    "Variable source field is not selected by its source binding.",
                    f"{path}.sourceFieldId",
                )

    dependencies = {
        binding.binding_id: {
            variable.source_binding_id
            for variable in binding.variables
            if variable.source == "selectedRecordField" and variable.source_binding_id
        }
        for binding in definition.bindings
    }
    remaining = {binding_id: set(sources) for binding_id, sources in dependencies.items()}
    while remaining:
        ready = {binding_id for binding_id, sources in remaining.items() if not sources}
        if not ready:
            raise _invalid(
                "surface.binding_variable_cycle",
                "Binding variables must form an acyclic dependency graph.",
                "bindings",
            )
        for binding_id in ready:
            remaining.pop(binding_id)
        for sources in remaining.values():
            sources.difference_update(ready)

    page_ids = _unique_ids(
        [page.page_id for page in definition.pages], "pages", "surface.page_duplicate"
    )
    action_ids = _unique_ids(
        [action.action_id for action in definition.actions], "actions", "surface.action_duplicate"
    )
    actions_by_id = {action.action_id: action for action in definition.actions}
    for index, action in enumerate(definition.actions):
        _validate_action(action, index, binding_ids, page_ids)

    seen_elements: set[str] = set()
    element_count = 0

    def visit(element: InterfaceElement, path: str, depth: int) -> None:
        nonlocal element_count
        element_count += 1
        if element_count > _MAX_ELEMENTS:
            raise _invalid(
                "surface.element_limit",
                f"An Interface can contain at most {_MAX_ELEMENTS} elements.",
                path,
            )
        if depth > _MAX_ELEMENT_DEPTH:
            raise _invalid(
                "surface.element_depth",
                f"Element nesting cannot exceed {_MAX_ELEMENT_DEPTH} levels.",
                path,
            )
        if not element.element_id:
            raise _invalid(
                "surface.element_id_required", "Element ID is required.", f"{path}.elementId"
            )
        if element.element_id in seen_elements:
            raise _invalid(
                "surface.element_duplicate", "Element IDs must be unique.", f"{path}.elementId"
            )
        seen_elements.add(element.element_id)
        if element.binding_id is not None and element.binding_id not in binding_ids:
            raise _invalid(
                "surface.binding_missing", "Element binding does not exist.", f"{path}.bindingId"
            )
        if element.action_id is not None and element.action_id not in action_ids:
            raise _invalid(
                "surface.action_missing", "Element action does not exist.", f"{path}.actionId"
            )
        if element.kind in _BOUND_KINDS and element.binding_id is None:
            raise _invalid(
                "surface.binding_missing", "Element requires a binding.", f"{path}.bindingId"
            )
        if element.kind in _ACTION_KINDS and element.action_id is None:
            raise _invalid(
                "surface.action_missing", "Element requires an action.", f"{path}.actionId"
            )
        if element.kind in _STRUCTURAL_KINDS:
            if element.binding_id is not None or element.action_id is not None:
                raise _invalid(
                    "surface.structure_invalid",
                    "Structural elements cannot bind data or actions.",
                    path,
                )
        elif element.children:
            raise _invalid(
                "surface.children_invalid",
                "Only structural elements can contain children.",
                f"{path}.children",
            )
        if (
            element.kind == "navigation"
            and element.action_id is not None
            and actions_by_id[element.action_id].kind != "navigate"
        ):
            raise _invalid(
                "surface.navigation_action_invalid",
                "Navigation elements require a navigate action.",
                f"{path}.actionId",
            )
        if (
            element.kind == "form"
            and element.action_id is not None
            and actions_by_id[element.action_id].kind not in {"record.create", "record.update"}
        ):
            raise _invalid(
                "surface.form_action_invalid",
                "Form elements require a record create or update action.",
                f"{path}.actionId",
            )
        for child_index, child in enumerate(element.children):
            visit(child, f"{path}.children.{child_index}", depth + 1)

    for page_index, page in enumerate(definition.pages):
        if not page.title.strip():
            raise _invalid(
                "surface.page_title_required",
                "Page title is required.",
                f"pages.{page_index}.title",
            )
        for element_index, element in enumerate(page.elements):
            visit(element, f"pages.{page_index}.elements.{element_index}", 1)


def _validate_action(
    action: InterfaceAction,
    index: int,
    binding_ids: set[str],
    page_ids: set[str],
) -> None:
    path = f"actions.{index}"
    if action.kind in {"record.create", "record.update", "binding.refresh"}:
        if action.binding_id is None or action.binding_id not in binding_ids:
            raise _invalid(
                "surface.binding_missing", "Action binding does not exist.", f"{path}.bindingId"
            )
        if any((action.target_page_id, action.plugin_id, action.plugin_action_id)):
            raise _invalid(
                "surface.action_invalid",
                "Record and refresh actions contain unrelated targets.",
                path,
            )
        return
    if action.kind == "navigate":
        if action.target_page_id is None or action.target_page_id not in page_ids:
            raise _invalid(
                "surface.page_missing",
                "Navigation target page does not exist.",
                f"{path}.targetPageId",
            )
        if any((action.binding_id, action.plugin_id, action.plugin_action_id)):
            raise _invalid(
                "surface.action_invalid", "Navigate action contains unrelated targets.", path
            )
        return
    if action.plugin_id is None or action.plugin_action_id is None:
        raise _invalid(
            "surface.plugin_action_invalid", "Plugin action identity is incomplete.", path
        )
    if action.binding_id is not None or action.target_page_id is not None:
        raise _invalid("surface.action_invalid", "Plugin action contains unrelated targets.", path)


def _unique_ids(values: list[str], path: str, code: str) -> set[str]:
    result: set[str] = set()
    for index, value in enumerate(values):
        if not value:
            raise _invalid("surface.id_required", "ID is required.", f"{path}.{index}")
        if value in result:
            raise _invalid(code, "IDs must be unique.", f"{path}.{index}")
        result.add(value)
    return result


def _snapshot_from_row(row: MetadataRecord) -> InterfaceSnapshot:
    try:
        definition = InterfaceDefinition.model_validate(row.values)
        validate_interface_definition(definition)
    except (ValidationError, SurfaceError) as error:
        raise SurfaceError(
            "Stored Interface definition is invalid.", code="surface.storage_invalid"
        ) from error
    if definition.interface_id != row.logical_id:
        raise SurfaceError(
            "Stored Interface identity does not match its definition.",
            code="surface.storage_invalid",
        )
    return InterfaceSnapshot(definition=definition, revision=row.revision)


def _optional_revision(row: MetadataRecord | None) -> str | None:
    return None if row is None else row.revision


def _require_interface_id(interface_id: str) -> None:
    if not _INTERFACE_ID.fullmatch(interface_id):
        raise _invalid(
            "surface.interface_id_invalid",
            "Interface ID must use 1-128 safe identifier characters.",
            "interfaceId",
        )


def _persistence_error(error: Exception) -> SurfaceError:
    if isinstance(error, MetadataConflictError):
        return SurfaceError(
            "Interface changed elsewhere.",
            code="surface.edit_conflict",
            path="expectedRevision",
        )
    return SurfaceError("Interface persistence failed.", code="surface.persistence_failed")


def _invalid(code: str, message: str, path: str) -> SurfaceError:
    return SurfaceError(message, code=code, path=path)


__all__ = ["SurfaceError", "SurfaceService", "validate_interface_definition"]
