from __future__ import annotations

from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
RUNNER = ROOT / "tests" / "e2e" / "webview_product_scenarios.mjs"


def _helper(source: str, name: str) -> str:
    helper_start = source.index(f"async function {name}")
    helper_end = source.index("\nasync function ", helper_start + 1)
    return source[helper_start:helper_end]


def test_single_select_waits_for_its_teleported_menu_to_close() -> None:
    source = RUNNER.read_text(encoding="utf-8")
    helper = _helper(source, "selectVisibleNOption")

    click = "await option.click();"
    hidden = 'await option.waitFor({ state: "hidden", timeout: 10_000 });'
    assert click in helper
    assert hidden in helper
    assert helper.index(click) < helper.index(hidden)
    assert "force:" not in helper
    assert ".evaluate(" not in helper


def test_multi_select_reads_chips_and_confirms_each_selection() -> None:
    source = RUNNER.read_text(encoding="utf-8")
    helper = _helper(source, "selectVisibleNOptions")

    chip_lookup = "select.getByText(label, { exact: true }).first()"
    skip_selected = "if (await selectedChip.isVisible())"
    menu_lookup = 'page.locator(".n-base-select-menu:visible").first()'
    option_lookup = 'const option = openMenu\n      .locator(".n-base-select-option")'

    assert chip_lookup in helper
    assert skip_selected in helper
    assert helper.index(chip_lookup) < helper.index(skip_selected) < helper.index(option_lookup)
    assert "continue;" in helper[helper.index(skip_selected) : helper.index(option_lookup)]
    assert menu_lookup in helper
    assert "if (!(await openMenu.count()))" in helper
    assert 'await openMenu.waitFor({ state: "visible", timeout: 10_000 })' in helper
    assert 'await selectedChip.waitFor({ state: "visible", timeout: 10_000 })' in helper
    assert "selectVisibleNOption(page" not in helper
    assert "force:" not in helper
    assert ".evaluate(" not in helper
