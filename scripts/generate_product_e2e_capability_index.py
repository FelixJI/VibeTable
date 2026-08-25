"""Generate the current product E2E scenario/capability index."""

from __future__ import annotations

import argparse
import html
import re
import sys
from collections import Counter, defaultdict
from collections.abc import Mapping, Sequence
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parents[1]
MANIFEST_PATH = REPO_ROOT / "tests/e2e/pocketbase_product_scenarios.json"
OUTPUT_PATH = REPO_ROOT / "docs/quality/product-e2e-capability-index.md"
PRODUCT_E2E_REPORT_CONTRACT_VERSION = "2.0"
PRODUCT_E2E_EVIDENCE_PATHS = (
    REPO_ROOT / "docs/e2e-performance.md",
    REPO_ROOT / "docs/quality/capability-matrix.md",
    REPO_ROOT / "docs/quality/stabilization-ledger.md",
)

_CANONICAL_EVIDENCE_SUFFIX = "docs/e2e-performance.md"
_LINKED_EVIDENCE_SUFFIXES = (
    "docs/quality/capability-matrix.md",
    "docs/quality/stabilization-ledger.md",
)
_CANONICAL_EVIDENCE_LINK = "../e2e-performance.md#当前产品-e2e-证据"
_CURRENT_EVIDENCE_HEADING = "## 当前产品 E2E 证据"
_METADATA_TOKEN_END = r"(?=$|[\s`)\]>,，。；;])"
_SOURCE_SHA_PATTERN = re.compile(r"GitHub/main@([0-9a-f]{40})" + _METADATA_TOKEN_END)
_ANY_SOURCE_PATTERN = re.compile(r"GitHub/main@([^\s`)\]>,，。；;]+)")
_RUN_URL_PATTERN = re.compile(
    r"https://github\.com/FelixJI/VibeTable/actions/runs/([1-9][0-9]*)" + _METADATA_TOKEN_END
)
_ANY_RUN_URL_PATTERN = re.compile(
    r"https://github\.com/FelixJI/VibeTable/actions/runs/([^\s`)\]>,，。；;]+)"
)
_REPORT_VERSION_PATTERN = re.compile(r"contractVersion=([0-9]+\.[0-9]+)" + _METADATA_TOKEN_END)
_ANY_REPORT_VERSION_PATTERN = re.compile(r"contractVersion=([^\s`)\]>,，。；;]+)")
_PASS_COUNT_PATTERN = re.compile(r"(?<![0-9])([0-9]+)/([0-9]+)\s*(?:passed|通过)")
_CURRENT_RESULT_PATTERN = re.compile(
    r"(?<![0-9])([0-9]+)/([0-9]+)\s+passed[、,]\s*"
    r"([0-9]+)\s+failed[、,]\s*([0-9]+)\s+skipped"
)
_TEMPORARY_REPORT_PATTERN = re.compile(
    r"build[\\/](?:q|qa)(?:[\\/]|(?=[\s`)]|$))",
    re.IGNORECASE,
)
_SCENARIO_REFERENCE_PATTERN = re.compile(r"(?<![0-9a-z])([0-9]{2}-[a-z][a-z0-9-]*)")

if str(REPO_ROOT) not in sys.path:
    sys.path.insert(0, str(REPO_ROOT))

from tests.e2e.product_scenario_manifest import Scenario, load_scenarios  # noqa: E402


def _normalized_document_path(path: Path) -> str:
    return path.as_posix().replace("\\", "/")


def _document_by_suffix(
    documents: Mapping[Path, str],
    suffix: str,
) -> tuple[Path, str] | None:
    return next(
        (
            (path, content)
            for path, content in documents.items()
            if _normalized_document_path(path).endswith(suffix)
        ),
        None,
    )


def _markdown_section(content: str, heading: str) -> str:
    start = content.find(heading)
    if start < 0:
        return ""
    following_heading = content.find("\n## ", start + len(heading))
    return content[start : following_heading if following_heading >= 0 else None]


def _contains_product_e2e_run_metadata(content: str) -> bool:
    return any(
        pattern.search(content)
        for pattern in (
            _ANY_SOURCE_PATTERN,
            _ANY_RUN_URL_PATTERN,
            _ANY_REPORT_VERSION_PATTERN,
            _PASS_COUNT_PATTERN,
        )
    )


def check_product_e2e_evidence_documents(
    documents: Mapping[Path, str],
    scenarios: Sequence[Scenario],
    *,
    report_contract_version: str,
) -> list[str]:
    """Validate the single current-run claim shared by product E2E documents."""
    errors: list[str] = []
    required_suffixes = (_CANONICAL_EVIDENCE_SUFFIX, *_LINKED_EVIDENCE_SUFFIXES)
    resolved_documents: dict[str, tuple[Path, str]] = {}
    for suffix in required_suffixes:
        document = _document_by_suffix(documents, suffix)
        if document is None:
            errors.append(f"missing product E2E evidence document: {suffix}")
            continue
        resolved_documents[suffix] = document

    canonical_document = resolved_documents.get(_CANONICAL_EVIDENCE_SUFFIX)
    if canonical_document is not None:
        canonical_path, canonical = canonical_document
        display_path = _normalized_document_path(canonical_path)
        if canonical.count(_CURRENT_EVIDENCE_HEADING) != 1:
            errors.append(
                f"{display_path}: expected exactly one {_CURRENT_EVIDENCE_HEADING!r} section"
            )
        current_evidence = _markdown_section(canonical, _CURRENT_EVIDENCE_HEADING)
        if (
            len(_SOURCE_SHA_PATTERN.findall(current_evidence)) != 1
            or len(_ANY_SOURCE_PATTERN.findall(current_evidence)) != 1
        ):
            errors.append(
                f"{display_path}: current product E2E evidence must bind one 40-character "
                "GitHub/main source SHA"
            )
        if (
            len(_RUN_URL_PATTERN.findall(current_evidence)) != 1
            or len(_ANY_RUN_URL_PATTERN.findall(current_evidence)) != 1
        ):
            errors.append(
                f"{display_path}: current product E2E evidence must bind one repository "
                "GitHub Actions run URL"
            )
        versions = _REPORT_VERSION_PATTERN.findall(current_evidence)
        if (
            versions != [report_contract_version]
            or len(_ANY_REPORT_VERSION_PATTERN.findall(current_evidence)) != 1
        ):
            errors.append(
                f"{display_path}: current product E2E evidence must declare "
                f"contractVersion={report_contract_version}"
            )
        expected_result = (len(scenarios), len(scenarios), 0, 0)
        observed_results = [
            tuple(int(value) for value in match)
            for match in _CURRENT_RESULT_PATTERN.findall(current_evidence)
        ]
        if observed_results != [expected_result]:
            errors.append(
                f"{display_path}: current product E2E result must be "
                f"{expected_result[0]}/{expected_result[1]} passed, 0 failed, 0 skipped"
            )
        expected_scenarios = Counter(scenario.id for scenario in scenarios)
        observed_scenarios = Counter(_SCENARIO_REFERENCE_PATTERN.findall(current_evidence))
        if observed_scenarios != expected_scenarios:
            errors.append(
                f"{display_path}: current product E2E evidence must list every manifest "
                "scenario exactly once"
            )
        canonical_without_current = canonical.replace(current_evidence, "", 1)
        if _contains_product_e2e_run_metadata(canonical_without_current):
            errors.append(
                f"{display_path}: duplicate product E2E run metadata must stay in the "
                "canonical evidence section"
            )

    scenario_ids = {scenario.id for scenario in scenarios}
    expected_counts = (len(scenarios), len(scenarios))
    for suffix, (path, content) in resolved_documents.items():
        display_path = _normalized_document_path(path)
        for passed_text in _PASS_COUNT_PATTERN.finditer(content):
            observed_counts = (int(passed_text.group(1)), int(passed_text.group(2)))
            if observed_counts != expected_counts:
                errors.append(
                    f"{display_path}: current product E2E result must be "
                    f"{expected_counts[0]}/{expected_counts[1]} passed; found "
                    f"{passed_text.group(0)}"
                )
        if _TEMPORARY_REPORT_PATTERN.search(content):
            errors.append(
                f"{display_path}: temporary product E2E report path cannot be current evidence"
            )
        for scenario_id in _SCENARIO_REFERENCE_PATTERN.findall(content):
            if scenario_id not in scenario_ids:
                errors.append(f"{display_path}: unknown product E2E scenario: {scenario_id}")
        if suffix in _LINKED_EVIDENCE_SUFFIXES:
            if _CANONICAL_EVIDENCE_LINK not in content:
                errors.append(
                    f"{display_path}: must link the canonical current product E2E evidence section"
                )
            if _contains_product_e2e_run_metadata(content):
                errors.append(
                    f"{display_path}: duplicate product E2E run metadata must stay in the "
                    "canonical evidence section"
                )
    return errors


def check_repository_product_e2e_evidence(
    document_paths: Sequence[Path] = PRODUCT_E2E_EVIDENCE_PATHS,
    manifest_path: Path = MANIFEST_PATH,
) -> list[str]:
    documents = {path: path.read_text(encoding="utf-8") for path in document_paths}
    return check_product_e2e_evidence_documents(
        documents,
        load_scenarios(manifest_path),
        report_contract_version=PRODUCT_E2E_REPORT_CONTRACT_VERSION,
    )


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
        errors.extend(check_repository_product_e2e_evidence())
    except (OSError, ValueError) as exc:
        errors = [str(exc)]
    if errors:
        for error in errors:
            print(error, file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
