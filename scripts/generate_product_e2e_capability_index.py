"""Generate the current product E2E scenario/capability index."""

from __future__ import annotations

import argparse
import html
import sys
from collections import defaultdict
from collections.abc import Sequence
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parents[1]
MANIFEST_PATH = REPO_ROOT / "tests/e2e/pocketbase_product_scenarios.json"
OUTPUT_PATH = REPO_ROOT / "docs/quality/product-e2e-capability-index.md"

if str(REPO_ROOT) not in sys.path:
    sys.path.insert(0, str(REPO_ROOT))

from tests.e2e.product_scenario_manifest import Scenario, load_scenarios  # noqa: E402


def _escape_cell(value: str) -> str:
    escaped = html.escape(value, quote=False)
    return (
        escaped.replace("\\", "\\\\")
        .replace("|", "\\|")
        .replace("`", "\\`")
        .replace("\r\n", "<br>")
        .replace("\r", "<br>")
        .replace("\n", "<br>")
    )


def _code_cell(value: str) -> str:
    escaped = html.escape(value, quote=False)
    escaped = (
        escaped.replace("|", "&#124;")
        .replace("\r\n", "<br>")
        .replace("\r", "<br>")
        .replace("\n", "<br>")
    )
    return f"<code>{escaped}</code>"


def render_index(scenarios: Sequence[Scenario]) -> str:
    """Render the manifest projection without adding runtime pass/fail claims."""
    capability_scenarios: dict[str, list[Scenario]] = defaultdict(list)
    for scenario in scenarios:
        for capability in scenario.capabilities:
            capability_scenarios[capability].append(scenario)

    smoke_count = sum("release.smoke" in scenario.capabilities for scenario in scenarios)
    lines = [
        "# 产品 E2E 能力索引",
        "",
        "> 本文件由 `tests/e2e/pocketbase_product_scenarios.json` 确定性生成；",
        "> 请运行 `uv run python scripts/generate_product_e2e_capability_index.py --write` 更新，",
        "> 不要手工编辑。这里的 capability 是 E2E selector tag，不等同于 Host/runtime 广告能力；",
        "> 本索引只描述 manifest 声明的覆盖范围，不代表某次运行已通过。",
        "",
        "## 当前声明范围",
        "",
        f"- 场景：{len(scenarios)}",
        f"- 唯一能力：{len(capability_scenarios)}",
        f"- 场景—能力关联：{sum(len(scenario.capabilities) for scenario in scenarios)}",
        f"- `release.smoke` 场景：{smoke_count}",
        "",
        "## 能力到场景",
        "",
        "| 能力 | 场景 |",
        "|---|---|",
    ]
    for capability in sorted(capability_scenarios):
        references = "、".join(
            f"{_code_cell(scenario.id)}（{_escape_cell(scenario.title)}）"
            for scenario in capability_scenarios[capability]
        )
        lines.append(f"| `{capability}` | {references} |")

    lines.extend(
        (
            "",
            "## 场景到能力",
            "",
            "| 场景 | 标题 | 需求 | 能力 |",
            "|---|---|---|---|",
        )
    )
    for scenario in scenarios:
        capabilities = "、".join(f"`{capability}`" for capability in scenario.capabilities)
        lines.append(
            f"| {_code_cell(scenario.id)} | {_escape_cell(scenario.title)} | "
            f"{_escape_cell(scenario.requirement)} | {capabilities} |"
        )
    return "\n".join(lines) + "\n"


def generated_contents(
    manifest_path: Path = MANIFEST_PATH,
    output_path: Path = OUTPUT_PATH,
) -> dict[Path, str]:
    return {output_path: render_index(load_scenarios(manifest_path))}


def write_index(
    manifest_path: Path = MANIFEST_PATH,
    output_path: Path = OUTPUT_PATH,
) -> list[Path]:
    changed: list[Path] = []
    for path, content in generated_contents(manifest_path, output_path).items():
        content_bytes = content.encode("utf-8")
        if path.is_file() and path.read_bytes() == content_bytes:
            continue
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_bytes(content_bytes)
        changed.append(path)
    return changed


def _display_path(path: Path) -> str:
    try:
        return path.relative_to(REPO_ROOT).as_posix()
    except ValueError:
        return path.name


def check_generated(
    manifest_path: Path = MANIFEST_PATH,
    output_path: Path = OUTPUT_PATH,
) -> list[str]:
    expected = generated_contents(manifest_path, output_path)[output_path]
    display_path = _display_path(output_path)
    if not output_path.is_file():
        return [f"missing generated product E2E capability index: {display_path}"]
    if output_path.read_bytes() != expected.encode("utf-8"):
        return [f"stale generated product E2E capability index: {display_path}"]
    return []


def _parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(description=__doc__)
    action = parser.add_mutually_exclusive_group(required=True)
    action.add_argument("--write", action="store_true")
    action.add_argument("--check", action="store_true")
    return parser


def main(argv: list[str] | None = None) -> int:
    args = _parser().parse_args(argv)
    try:
        if args.write:
            for path in write_index():
                print(_display_path(path))
            return 0
        errors = check_generated()
    except (OSError, ValueError) as exc:
        errors = [str(exc)]
    if errors:
        for error in errors:
            print(error, file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
