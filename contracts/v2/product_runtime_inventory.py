#!/usr/bin/env python3
"""Load and validate the Product runtime ownership inventory."""

from __future__ import annotations

import argparse
import json
import sys
from collections.abc import Mapping
from dataclasses import dataclass, field
from enum import StrEnum
from pathlib import Path, PurePosixPath, PureWindowsPath
from types import MappingProxyType
from typing import Literal

from pydantic import BaseModel, ConfigDict, Field, ValidationError

REPO_ROOT = Path(__file__).resolve().parents[2]
INVENTORY_PATH = Path(__file__).with_name("product-runtime-ownership-inventory.json")
SCHEMA_PATH = Path(__file__).with_name("product-runtime-ownership-inventory.schema.json")
CATALOG_PATH = Path(__file__).with_name("fixtures") / "product-rpc-catalog.json"
SCENARIOS_PATH = REPO_ROOT / "tests" / "e2e" / "pocketbase_product_scenarios.json"

type EntryKind = Literal["rpc", "event", "state"]


class RuntimeHop(StrEnum):
    RENDERER = "renderer"
    WPF_HOST = "wpfHost"
    PYTHON_BFF = "pythonBff"
    GO_SIDECAR = "goSidecar"
    POCKETBASE = "pocketBase"
    PYTHON_WORKER = "pythonWorker"
    NODE_WORKER = "nodeWorker"


class RouteOwner(StrEnum):
    WPF_HOST = "wpfHost"
    PYTHON_BFF = "pythonBff"
    GO_SIDECAR = "goSidecar"
    PYTHON_WORKER = "pythonWorker"


class OwnershipClass(StrEnum):
    GO_AUTHORITY = "GO_AUTHORITY"
    HOST_NATIVE = "HOST_NATIVE"
    PYTHON_WORKER = "PYTHON_WORKER"
    TEMPORARY_BFF = "TEMPORARY_BFF"


class StableOwner(StrEnum):
    GO_AUTHORITY = "GO_AUTHORITY"
    HOST_NATIVE = "HOST_NATIVE"
    PYTHON_WORKER = "PYTHON_WORKER"


class StateLifetime(StrEnum):
    REQUEST = "request"
    TASK = "task"
    PROCESS = "process"
    WORKSPACE = "workspace"
    DEVICE = "device"


class CancellationPolicy(StrEnum):
    NONE = "none"
    CLIENT_PENDING_ONLY = "clientPendingOnly"
    COOPERATIVE = "cooperative"
    PROCESS_BOUND = "processBound"


class TimeoutPolicy(StrEnum):
    NONE = "none"
    PER_HOP_BOUNDED = "perHopBounded"
    OPERATION_BOUNDED = "operationBounded"
    STARTUP_BOUNDED = "startupBounded"


class _ClosedModel(BaseModel):
    model_config = ConfigDict(extra="forbid", frozen=True, populate_by_name=True)


class _Effects(_ClosedModel):
    read: tuple[str, ...]
    write: tuple[str, ...]
    notification: tuple[str, ...]
    state: tuple[str, ...]


class _SourceGroup(_ClosedModel):
    id: str = Field(min_length=1, pattern=r"^[a-z0-9][a-z0-9.-]*$")
    kind: EntryKind
    names: tuple[str, ...] = Field(min_length=1)
    current_path: tuple[RuntimeHop, ...] = Field(alias="currentPath", min_length=1)
    current_route: RouteOwner = Field(alias="currentRoute")
    classification: OwnershipClass
    target_owner: StableOwner = Field(alias="targetOwner")
    authority: StableOwner
    effects: _Effects
    state_holders: tuple[RuntimeHop, ...] = Field(alias="stateHolders", min_length=1)
    state_lifetime: StateLifetime = Field(alias="stateLifetime")
    cancellation: CancellationPolicy
    timeout: TimeoutPolicy
    product_scenarios: tuple[str, ...] = Field(alias="productScenarios")
    target_slice: str = Field(alias="targetSlice", min_length=1)
    target_pr: str | None = Field(alias="targetPr")
    delete_when: str = Field(alias="deleteWhen", min_length=1)
    evidence: tuple[str, ...] = Field(min_length=1)


class _SourceInventory(_ClosedModel):
    inventory_version: Literal["1.0"] = Field(alias="inventoryVersion")
    catalog_contract_version: str = Field(alias="catalogContractVersion", min_length=1)
    groups: tuple[_SourceGroup, ...] = Field(min_length=1)


@dataclass(frozen=True)
class OwnershipRecord:
    kind: EntryKind
    name: str
    group_id: str
    current_path: tuple[str, ...]
    current_route: str
    classification: str
    target_owner: str
    authority: str
    effect: str
    state_holders: tuple[str, ...]
    state_lifetime: str
    cancellation: str
    timeout: str
    product_scenarios: tuple[str, ...]
    target_slice: str
    target_pr: str | None
    delete_when: str
    evidence: tuple[str, ...]


class ProductRuntimeInventoryError(ValueError):
    """Stable fail-closed diagnostic from the ownership inventory seam."""

    def __init__(self, code: str, subjects: tuple[str, ...], detail: str) -> None:
        self.code = code
        self.subjects = subjects
        self.detail = detail
        suffix = f" subjects={list(subjects)!r}" if subjects else ""
        super().__init__(f"{code}: {detail}{suffix}")


@dataclass(frozen=True)
class ProductRuntimeInventory:
    rpc_methods: tuple[OwnershipRecord, ...]
    events: tuple[OwnershipRecord, ...]
    states: tuple[OwnershipRecord, ...]
    _index: Mapping[tuple[str, str], OwnershipRecord] = field(repr=False)

    def require(self, kind: EntryKind, name: str) -> OwnershipRecord:
        try:
            return self._index[(kind, name)]
        except KeyError as error:
            raise ProductRuntimeInventoryError(
                "unknownEntry",
                (f"{kind}:{name}",),
                "runtime ownership entry is not declared",
            ) from error


class _DuplicatePropertyError(ValueError):
    pass


def _closed_object(pairs: list[tuple[str, object]]) -> dict[str, object]:
    result: dict[str, object] = {}
    for key, value in pairs:
        if key in result:
            raise _DuplicatePropertyError(key)
        result[key] = value
    return result


def _load_json(path: Path, *, duplicate_keys: bool = False) -> object:
    text = path.read_text(encoding="utf-8")
    try:
        if duplicate_keys:
            return json.loads(text, object_pairs_hook=_closed_object)
        return json.loads(text)
    except _DuplicatePropertyError as error:
        raise ProductRuntimeInventoryError(
            "duplicateProperty", (str(error),), f"{path.name} contains a duplicate property"
        ) from error
    except json.JSONDecodeError as error:
        raise ProductRuntimeInventoryError(
            "invalidJson", (), f"{path.name}:{error.lineno}:{error.colno}: {error.msg}"
        ) from error


def _effect_for(group: _SourceGroup, name: str) -> str:
    matches = [
        effect
        for effect in ("read", "write", "notification", "state")
        if name in getattr(group.effects, effect)
    ]
    if len(matches) != 1:
        raise ProductRuntimeInventoryError(
            "invalidSemantics",
            (f"{group.kind}:{name}",),
            "each name must appear in exactly one effects list",
        )
    return matches[0]


def _validate_group(
    group: _SourceGroup,
    *,
    scenario_ids: set[str],
) -> dict[str, OwnershipRecord]:
    if tuple(sorted(group.names)) != group.names or len(set(group.names)) != len(group.names):
        raise ProductRuntimeInventoryError(
            "nonCanonicalOrder", (group.id,), "group names must be unique and ordinal-sorted"
        )
    if group.current_route.value not in {hop.value for hop in group.current_path}:
        raise ProductRuntimeInventoryError(
            "invalidSemantics", (group.id,), "currentRoute must appear in currentPath"
        )
    if group.classification is OwnershipClass.TEMPORARY_BFF:
        if (
            group.current_route is not RouteOwner.PYTHON_BFF
            or group.target_owner is StableOwner.PYTHON_WORKER
        ):
            raise ProductRuntimeInventoryError(
                "invalidSemantics",
                (group.id,),
                "TEMPORARY_BFF must be a Python route with a non-Python stable owner",
            )
    elif group.target_owner.value != group.classification.value:
        raise ProductRuntimeInventoryError(
            "invalidSemantics",
            (group.id,),
            "stable classifications must match their targetOwner",
        )
    allowed_effects = {
        "rpc": {"read", "write"},
        "event": {"notification"},
        "state": {"state"},
    }[group.kind]
    declared_effects = {
        effect
        for effect in ("read", "write", "notification", "state")
        if getattr(group.effects, effect)
    }
    if not declared_effects <= allowed_effects:
        raise ProductRuntimeInventoryError(
            "invalidSemantics",
            (group.id,),
            f"{group.kind} groups may only use effects {sorted(allowed_effects)}",
        )
    effect_names = tuple(
        name
        for effect in ("read", "write", "notification", "state")
        for name in getattr(group.effects, effect)
    )
    if len(set(effect_names)) != len(effect_names) or set(effect_names) != set(group.names):
        raise ProductRuntimeInventoryError(
            "invalidSemantics",
            (group.id,),
            "effects must partition the declared group names exactly once",
        )
    if len(set(group.product_scenarios)) != len(group.product_scenarios):
        raise ProductRuntimeInventoryError(
            "invalidReference", (group.id,), "productScenarios must be unique"
        )
    unknown_scenarios = tuple(sorted(set(group.product_scenarios) - scenario_ids))
    if unknown_scenarios:
        raise ProductRuntimeInventoryError(
            "invalidReference", unknown_scenarios, "unknown Product E2E scenario"
        )
    if len(set(group.evidence)) != len(group.evidence):
        raise ProductRuntimeInventoryError(
            "invalidReference", (group.id,), "evidence paths must be unique"
        )
    invalid_evidence: list[str] = []
    repository_root = REPO_ROOT.resolve()
    for value in group.evidence:
        candidate = PurePosixPath(value)
        windows_candidate = PureWindowsPath(value)
        if (
            candidate.is_absolute()
            or ".." in candidate.parts
            or "\\" in value
            or bool(windows_candidate.drive)
        ):
            invalid_evidence.append(value)
            continue
        resolved = (repository_root / Path(*candidate.parts)).resolve()
        if not resolved.is_relative_to(repository_root) or not resolved.is_file():
            invalid_evidence.append(value)
    if invalid_evidence:
        raise ProductRuntimeInventoryError(
            "invalidReference",
            tuple(sorted(invalid_evidence)),
            "evidence must name existing repository-relative files",
        )

    return {
        name: OwnershipRecord(
            kind=group.kind,
            name=name,
            group_id=group.id,
            current_path=tuple(hop.value for hop in group.current_path),
            current_route=group.current_route.value,
            classification=group.classification.value,
            target_owner=group.target_owner.value,
            authority=group.authority.value,
            effect=_effect_for(group, name),
            state_holders=tuple(hop.value for hop in group.state_holders),
            state_lifetime=group.state_lifetime.value,
            cancellation=group.cancellation.value,
            timeout=group.timeout.value,
            product_scenarios=group.product_scenarios,
            target_slice=group.target_slice,
            target_pr=group.target_pr,
            delete_when=group.delete_when,
            evidence=group.evidence,
        )
        for name in group.names
    }


def _exact_records(
    kind: Literal["rpc", "event"],
    expected: tuple[str, ...],
    records: Mapping[tuple[str, str], OwnershipRecord],
) -> tuple[OwnershipRecord, ...]:
    actual = {name for record_kind, name in records if record_kind == kind}
    missing = tuple(sorted(set(expected) - actual))
    unknown = tuple(sorted(actual - set(expected)))
    if missing or unknown:
        subjects = tuple(f"missing:{name}" for name in missing) + tuple(
            f"unknown:{name}" for name in unknown
        )
        raise ProductRuntimeInventoryError(
            "coverageMismatch", subjects, f"{kind} inventory must exactly cover the Product catalog"
        )
    return tuple(records[(kind, name)] for name in expected)


def load_product_runtime_inventory(
    *,
    inventory_path: Path = INVENTORY_PATH,
    catalog_path: Path = CATALOG_PATH,
    scenarios_path: Path = SCENARIOS_PATH,
) -> ProductRuntimeInventory:
    """Load the complete normalized inventory through its public validation seam."""

    raw = _load_json(inventory_path, duplicate_keys=True)
    try:
        source = _SourceInventory.model_validate(raw)
    except ValidationError as error:
        locations = tuple(".".join(str(item) for item in entry["loc"]) for entry in error.errors())
        raise ProductRuntimeInventoryError(
            "invalidShape", tuple(sorted(locations)), "inventory does not match the closed schema"
        ) from error

    catalog = _load_json(catalog_path)
    if not isinstance(catalog, dict):
        raise ProductRuntimeInventoryError(
            "invalidCatalog", (), "Product catalog must be an object"
        )
    catalog_version = catalog.get("contractVersion")
    methods = catalog.get("rpcMethods")
    topics = catalog.get("eventTopics")
    if (
        not isinstance(catalog_version, str)
        or not isinstance(methods, list)
        or not all(isinstance(item, str) for item in methods)
        or not isinstance(topics, list)
        or not all(isinstance(item, str) for item in topics)
    ):
        raise ProductRuntimeInventoryError(
            "invalidCatalog", (), "Product catalog method/event registry is malformed"
        )
    if source.catalog_contract_version != catalog_version:
        raise ProductRuntimeInventoryError(
            "versionMismatch",
            (source.catalog_contract_version, catalog_version),
            "inventory and Product catalog contract versions differ",
        )

    scenario_source = _load_json(scenarios_path)
    if not isinstance(scenario_source, list) or not all(
        isinstance(item, dict) and isinstance(item.get("id"), str) for item in scenario_source
    ):
        raise ProductRuntimeInventoryError(
            "invalidScenarioCatalog", (), "Product E2E scenario manifest is malformed"
        )
    scenario_ids = {str(item["id"]) for item in scenario_source}

    group_ids = tuple(group.id for group in source.groups)
    if tuple(sorted(group_ids)) != group_ids or len(set(group_ids)) != len(group_ids):
        raise ProductRuntimeInventoryError(
            "nonCanonicalOrder", group_ids, "group ids must be unique and ordinal-sorted"
        )

    records: dict[tuple[str, str], OwnershipRecord] = {}
    duplicates: list[str] = []
    for group in source.groups:
        for name, record in _validate_group(group, scenario_ids=scenario_ids).items():
            key = (group.kind, name)
            if key in records:
                duplicates.append(f"{group.kind}:{name}")
            records[key] = record
    if duplicates:
        raise ProductRuntimeInventoryError(
            "duplicateEntry", tuple(sorted(set(duplicates))), "entry is declared by multiple groups"
        )

    rpc_methods = _exact_records("rpc", tuple(methods), records)
    events = _exact_records("event", tuple(topics), records)
    states = tuple(record for (kind, _name), record in sorted(records.items()) if kind == "state")
    if not states:
        raise ProductRuntimeInventoryError(
            "coverageMismatch", (), "runtime state evidence inventory must not be empty"
        )
    return ProductRuntimeInventory(
        rpc_methods=rpc_methods,
        events=events,
        states=states,
        _index=MappingProxyType(records),
    )


def _render_schema() -> str:
    schema = _SourceInventory.model_json_schema(by_alias=True)
    schema["$id"] = (
        "https://vibetable.local/contracts/v2/product-runtime-ownership-inventory.schema.json"
    )
    schema["title"] = "VibeTable Product Runtime Ownership Inventory"
    return json.dumps(schema, ensure_ascii=False, indent=2) + "\n"


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--check", action="store_true", help="fail if schema or inventory is stale")
    args = parser.parse_args(argv)

    try:
        load_product_runtime_inventory()
        rendered = _render_schema()
        if args.check:
            current = SCHEMA_PATH.read_text(encoding="utf-8") if SCHEMA_PATH.exists() else ""
            if current != rendered:
                raise ProductRuntimeInventoryError(
                    "staleSchema", (SCHEMA_PATH.name,), "generated ownership schema is stale"
                )
        else:
            SCHEMA_PATH.write_text(rendered, encoding="utf-8", newline="\n")
    except (OSError, ProductRuntimeInventoryError) as error:
        print(error, file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
