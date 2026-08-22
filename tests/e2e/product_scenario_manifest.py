"""Validated product E2E scenario manifest boundary."""

from __future__ import annotations

import json
import re
from collections.abc import Sequence
from dataclasses import dataclass
from pathlib import Path

SCENARIO_MANIFEST = Path(__file__).with_name("pocketbase_product_scenarios.json")


@dataclass(frozen=True)
class Scenario:
    id: str
    title: str
    requirement: str
    capabilities: tuple[str, ...] = ()


_SCENARIO_FIELDS = frozenset({"id", "title", "requirement", "capabilities"})
_CAPABILITY_PATTERN = re.compile(r"^[a-z][a-z0-9]*(?:[.-][a-z0-9]+)*$")


def load_scenarios(path: Path = SCENARIO_MANIFEST) -> list[Scenario]:
    raw = json.loads(path.read_text(encoding="utf-8"))
    if not isinstance(raw, list) or not raw:
        raise ValueError("scenario manifest must be a non-empty array")
    scenarios: list[Scenario] = []
    for index, item in enumerate(raw):
        if not isinstance(item, dict) or set(item) != _SCENARIO_FIELDS:
            raise ValueError(
                f"scenario[{index}] must contain exactly: " + ", ".join(sorted(_SCENARIO_FIELDS))
            )
        capabilities = item["capabilities"]
        if (
            not isinstance(capabilities, list)
            or not capabilities
            or any(
                not isinstance(capability, str) or _CAPABILITY_PATTERN.fullmatch(capability) is None
                for capability in capabilities
            )
        ):
            raise ValueError(f"scenario[{index}].capabilities must be non-empty stable names")
        if len(set(capabilities)) != len(capabilities):
            raise ValueError(f"scenario[{index}].capabilities must be unique")
        scenario = Scenario(
            id=str(item["id"]),
            title=str(item["title"]),
            requirement=str(item["requirement"]),
            capabilities=tuple(capabilities),
        )
        if not scenario.id or not scenario.title or not scenario.requirement:
            raise ValueError(f"scenario[{index}] identity, title and requirement are required")
        scenarios.append(scenario)
    if len({item.id for item in scenarios}) != len(scenarios):
        raise ValueError("scenario ids must be unique")
    return scenarios


def select_scenarios(
    scenarios: Sequence[Scenario],
    *,
    scenario_ids: Sequence[str] = (),
    capabilities: Sequence[str] = (),
) -> list[Scenario]:
    known_ids = {item.id for item in scenarios}
    unknown_ids = sorted(set(scenario_ids) - known_ids)
    if unknown_ids:
        raise ValueError(f"unknown scenarios: {', '.join(unknown_ids)}")
    known_capabilities = {capability for item in scenarios for capability in item.capabilities}
    unknown_capabilities = sorted(set(capabilities) - known_capabilities)
    if unknown_capabilities:
        raise ValueError(f"unknown capabilities: {', '.join(unknown_capabilities)}")
    if not scenario_ids and not capabilities:
        return list(scenarios)
    selected_ids = set(scenario_ids)
    selected_capabilities = set(capabilities)
    return [
        item
        for item in scenarios
        if item.id in selected_ids or bool(selected_capabilities.intersection(item.capabilities))
    ]
