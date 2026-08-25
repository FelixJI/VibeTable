"""Generate the current product E2E scenario/capability index."""

from __future__ import annotations

import argparse
import html
import re
import subprocess
import sys
from collections import Counter, defaultdict
from collections.abc import Mapping, Sequence
from pathlib import Path
from urllib.parse import urlparse

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
_MANIFEST_GAP_LINE_PATTERN = re.compile(r"(?m)^- 当前 manifest gap：([^\r\n]+)$")
_MANIFEST_SURPLUS_LINE_PATTERN = re.compile(r"(?m)^- 当前 manifest surplus：([^\r\n]+)$")
_TEMPORARY_REPORT_PATTERN = re.compile(
    r"build[\\/](?:q|qa)(?:[\\/]|(?=[\s`)]|$))",
    re.IGNORECASE,
)
_TRUSTED_REPOSITORY_IDENTITY = ("github.com", "felixji", "vibetable")

if str(REPO_ROOT) not in sys.path:
    sys.path.insert(0, str(REPO_ROOT))

from tests.e2e.product_scenario_manifest import (  # noqa: E402
    SCENARIO_ID_PATTERN_TEXT,
    Scenario,
    load_scenarios,
    parse_scenarios_text,
)

_MANIFEST_DELTA_DETAILS_PATTERN = re.compile(
    rf"^([1-9][0-9]*)（(`{SCENARIO_ID_PATTERN_TEXT}`"
    rf"(?:、`{SCENARIO_ID_PATTERN_TEXT}`)*)）。$"
)
_SCENARIO_REFERENCE_PATTERN = re.compile(rf"`({SCENARIO_ID_PATTERN_TEXT})`")


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


def _parse_manifest_delta(
    current_evidence: str,
    *,
    line_pattern: re.Pattern[str],
    label: str,
    display_path: str,
    errors: list[str],
) -> tuple[Counter[str], str]:
    matches = list(line_pattern.finditer(current_evidence))
    if len(matches) != 1:
        errors.append(f"{display_path}: current manifest {label} must be declared exactly once")
        return Counter(), current_evidence
    match = matches[0]
    declaration = match.group(1)
    evidence_without_delta = current_evidence[: match.start()] + current_evidence[match.end() :]
    if declaration == "无。":
        return Counter(), evidence_without_delta
    details = _MANIFEST_DELTA_DETAILS_PATTERN.fullmatch(declaration)
    if details is None:
        errors.append(
            f"{display_path}: current manifest {label} must be '无。' or an exact "
            "positive count with backtick-delimited scenario ids"
        )
        return Counter(), evidence_without_delta
    declared_count = int(details.group(1))
    scenario_ids = Counter(_SCENARIO_REFERENCE_PATTERN.findall(details.group(2)))
    if declared_count != sum(scenario_ids.values()) or any(
        count != 1 for count in scenario_ids.values()
    ):
        errors.append(
            f"{display_path}: current manifest {label} count and scenario ids must match "
            "exactly without duplicates"
        )
    return scenario_ids, evidence_without_delta


def _run_git(*args: str, check: bool = True) -> subprocess.CompletedProcess[bytes]:
    return subprocess.run(
        ["git", *args],
        cwd=REPO_ROOT,
        check=check,
        capture_output=True,
    )


def _repository_identity(remote_url: str) -> tuple[str, str, str] | None:
    value = remote_url.strip()
    if "://" in value:
        parsed = urlparse(value)
        if parsed.scheme.lower() not in {"https", "ssh"} or parsed.hostname is None:
            return None
        host = parsed.hostname
        path = parsed.path
    else:
        scp = re.fullmatch(r"(?:[^@/:]+@)?([^/:]+):(.+)", value)
        if scp is None:
            return None
        host, path = scp.groups()
    parts = path.strip("/").split("/")
    if len(parts) != 2:
        return None
    owner, repository = parts
    if repository.lower().endswith(".git"):
        repository = repository[:-4]
    return host.lower(), owner.lower(), repository.lower()


def _is_trusted_repository_url(remote_url: str) -> bool:
    return _repository_identity(remote_url) == _TRUSTED_REPOSITORY_IDENTITY


def _trusted_main_refs() -> list[str]:
    refs = (
        _run_git("for-each-ref", "--format=%(refname)", "refs/remotes")
        .stdout.decode("utf-8")
        .splitlines()
    )
    trusted: list[str] = []
    for ref in refs:
        match = re.fullmatch(r"refs/remotes/([^/]+)/main", ref)
        if match is None:
            continue
        remote = match.group(1)
        remote_url = _run_git("remote", "get-url", remote).stdout.decode("utf-8").strip()
        if _is_trusted_repository_url(remote_url):
            trusted.append(ref)
    return trusted


def _load_verified_scenarios(source_sha: str) -> list[Scenario]:
    try:
        trusted_refs = _trusted_main_refs()
    except (subprocess.CalledProcessError, UnicodeDecodeError) as error:
        raise ValueError("trusted GitHub main refs cannot be inspected") from error
    if not trusted_refs or not any(
        _run_git("merge-base", "--is-ancestor", source_sha, ref, check=False).returncode == 0
        for ref in trusted_refs
    ):
        raise ValueError("source SHA is not reachable from a trusted GitHub main ref")
    manifest_ref = f"{source_sha}:{MANIFEST_PATH.relative_to(REPO_ROOT).as_posix()}"
    try:
        content = _run_git("show", manifest_ref).stdout.decode("utf-8")
        return parse_scenarios_text(content)
    except (subprocess.CalledProcessError, UnicodeDecodeError, ValueError) as error:
        raise ValueError("source manifest cannot be read or validated") from error


def check_product_e2e_evidence_documents(
    documents: Mapping[Path, str],
    scenarios: Sequence[Scenario],
    *,
    report_contract_version: str,
    verified_scenarios: Sequence[Scenario] | None = None,
    require_closed: bool = False,
) -> list[str]:
    """Validate the single current-run claim shared by product E2E documents."""
    errors: list[str] = []
    verified = list(verified_scenarios if verified_scenarios is not None else scenarios)
    verified_result_counts: tuple[int, int] | None = None
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
        current_by_id = {scenario.id: scenario for scenario in scenarios}
        verified_by_id = {scenario.id: scenario for scenario in verified}
        rewritten_ids = sorted(
            scenario_id
            for scenario_id in current_by_id.keys() & verified_by_id.keys()
            if current_by_id[scenario_id] != verified_by_id[scenario_id]
        )
        if rewritten_ids:
            errors.append(
                f"{display_path}: same-id scenario semantics differ from the source manifest: "
                + ", ".join(rewritten_ids)
            )

        manifest_gap, verified_evidence = _parse_manifest_delta(
            current_evidence,
            line_pattern=_MANIFEST_GAP_LINE_PATTERN,
            label="gap",
            display_path=display_path,
            errors=errors,
        )
        manifest_surplus, verified_evidence = _parse_manifest_delta(
            verified_evidence,
            line_pattern=_MANIFEST_SURPLUS_LINE_PATTERN,
            label="surplus",
            display_path=display_path,
            errors=errors,
        )
        expected_gap = Counter(current_by_id.keys() - verified_by_id.keys())
        expected_surplus = Counter(verified_by_id.keys() - current_by_id.keys())
        if manifest_gap != expected_gap:
            errors.append(
                f"{display_path}: current manifest gap must exactly list scenarios added "
                "since the source manifest"
            )
        if manifest_surplus != expected_surplus:
            errors.append(
                f"{display_path}: current manifest surplus must exactly list source "
                "manifest scenarios absent from the current manifest"
            )
        if require_closed and (expected_gap or expected_surplus):
            errors.append(
                f"{display_path}: release evidence reconciliation must be closed; "
                "current manifest gap and surplus must both be empty"
            )

        observed_scenarios = Counter(_SCENARIO_REFERENCE_PATTERN.findall(verified_evidence))
        expected_verified_scenarios = Counter(verified_by_id.keys())
        if observed_scenarios != expected_verified_scenarios:
            errors.append(
                f"{display_path}: current product E2E evidence must list every manifest "
                "scenario exactly once as defined by the source manifest"
            )
        verified_scenario_count = len(verified)
        expected_result = (
            verified_scenario_count,
            verified_scenario_count,
            0,
            0,
        )
        observed_results = [
            tuple(int(value) for value in match)
            for match in _CURRENT_RESULT_PATTERN.findall(current_evidence)
        ]
        if observed_results != [expected_result]:
            errors.append(
                f"{display_path}: current product E2E result must be "
                f"{expected_result[0]}/{expected_result[1]} passed, 0 failed, 0 skipped"
            )
        else:
            verified_result_counts = expected_result[:2]
        canonical_without_current = canonical.replace(current_evidence, "", 1)
        if _contains_product_e2e_run_metadata(canonical_without_current):
            errors.append(
                f"{display_path}: duplicate product E2E run metadata must stay in the "
                "canonical evidence section"
            )

    scenario_ids = {scenario.id for scenario in (*scenarios, *verified)}
    for suffix, (path, content) in resolved_documents.items():
        display_path = _normalized_document_path(path)
        for passed_text in _PASS_COUNT_PATTERN.finditer(content):
            observed_counts = (int(passed_text.group(1)), int(passed_text.group(2)))
            if verified_result_counts is not None and observed_counts != verified_result_counts:
                errors.append(
                    f"{display_path}: current product E2E result must be "
                    f"{verified_result_counts[0]}/{verified_result_counts[1]} passed; found "
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
    *,
    require_closed: bool = False,
) -> list[str]:
    documents = {path: path.read_text(encoding="utf-8") for path in document_paths}
    scenarios = load_scenarios(manifest_path)
    verified_scenarios = scenarios
    source_error: str | None = None
    canonical_document = _document_by_suffix(documents, _CANONICAL_EVIDENCE_SUFFIX)
    if canonical_document is not None:
        current_evidence = _markdown_section(canonical_document[1], _CURRENT_EVIDENCE_HEADING)
        source_shas = _SOURCE_SHA_PATTERN.findall(current_evidence)
        if len(source_shas) == 1:
            try:
                verified_scenarios = _load_verified_scenarios(source_shas[0])
            except ValueError as error:
                source_error = str(error)
    errors = check_product_e2e_evidence_documents(
        documents,
        scenarios,
        report_contract_version=PRODUCT_E2E_REPORT_CONTRACT_VERSION,
        verified_scenarios=verified_scenarios,
        require_closed=require_closed,
    )
    if source_error is not None:
        errors.append(f"source manifest verification failed: {source_error}")
    return errors


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
    parser.add_argument("--require-closed-evidence", action="store_true")
    return parser


def main(argv: list[str] | None = None) -> int:
    args = _parser().parse_args(argv)
    if args.write and args.require_closed_evidence:
        _parser().error("--require-closed-evidence is only valid with --check")
    try:
        if args.write:
            for path in write_index():
                print(_display_path(path))
            return 0
        errors = check_generated()
        errors.extend(
            check_repository_product_e2e_evidence(require_closed=args.require_closed_evidence)
        )
    except (OSError, ValueError) as exc:
        errors = [str(exc)]
    if errors:
        for error in errors:
            print(error, file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
