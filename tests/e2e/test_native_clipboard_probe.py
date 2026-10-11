"""Pure fake-Win32 tests for the native clipboard probe logic.

The probe's real user32/kernel32 bindings are swapped for in-memory fakes, so
these tests cover the emptiness, sequence-number, exact-text and conditional
clear decisions without touching the real clipboard.
"""

from __future__ import annotations

import ctypes

import pytest

from tests.e2e import native_clipboard_probe as probe

pytestmark = pytest.mark.skipif(not probe._IS_WINDOWS, reason="Windows-only probe")


class FakeClipboard:
    """user32 surface with an in-memory clipboard state."""

    def __init__(
        self,
        *,
        formats: int = 0,
        text: str | None = None,
        sequence: int = 7,
        open_ok: bool = True,
        count_error: int | None = None,
        empty_ok: bool = True,
    ) -> None:
        self.formats = formats
        self.sequence = sequence
        self.open_ok = open_ok
        self.count_error = count_error
        self.empty_ok = empty_ok
        self.emptied = False
        self._buffer = ctypes.create_unicode_buffer(text) if text is not None else None

    def _refresh_text(self, text: str | None) -> None:
        self._buffer = ctypes.create_unicode_buffer(text) if text is not None else None

    # user32 surface (Win32-mirroring names are intentional)
    def OpenClipboard(self, hwnd) -> int:  # noqa: N802
        return int(self.open_ok)

    def CloseClipboard(self) -> int:  # noqa: N802
        return 1

    def CountClipboardFormats(self) -> int:  # noqa: N802
        if self.count_error is not None:
            ctypes.set_last_error(self.count_error)
            return 0
        return self.formats

    def GetClipboardSequenceNumber(self) -> int:  # noqa: N802
        return self.sequence

    def GetClipboardData(self, format_) -> object | None:  # noqa: N802
        return self._buffer

    def EmptyClipboard(self) -> int:  # noqa: N802
        if not self.empty_ok:
            return 0
        self.formats = 0
        self._refresh_text(None)
        self.emptied = True
        self.sequence += 1
        return 1


class FakeKernel:
    # kernel32 surface over the fake unicode buffer.
    @staticmethod
    def GlobalSize(handle) -> int:  # noqa: N802
        return ctypes.sizeof(handle)

    @staticmethod
    def GlobalLock(handle) -> int:  # noqa: N802
        return ctypes.addressof(handle)

    @staticmethod
    def GlobalUnlock(handle) -> int:  # noqa: N802
        return 1


@pytest.fixture
def fake_clipboard(monkeypatch: pytest.MonkeyPatch) -> FakeClipboard:
    fake = FakeClipboard()
    monkeypatch.setattr(probe, "_user32", fake)
    monkeypatch.setattr(probe, "_kernel32", FakeKernel)
    return fake


def test_inspect_reports_empty_with_sequence(fake_clipboard: FakeClipboard) -> None:
    ctypes.set_last_error(1418)
    assert probe._inspect() == {"ok": True, "empty": True, "formatCount": 0, "sequence": 7}


def test_inspect_reports_non_empty_format_count(fake_clipboard: FakeClipboard) -> None:
    fake_clipboard.formats = 3
    assert probe._inspect() == {"ok": True, "empty": False, "formatCount": 3, "sequence": 7}


def test_inspect_fails_closed_when_zero_formats_carries_an_error(
    fake_clipboard: FakeClipboard,
) -> None:
    fake_clipboard.count_error = 1418
    with pytest.raises(probe._ProbeError, match="count-clipboard-formats-failed"):
        probe._inspect()


def test_open_failure_is_a_labeled_error(fake_clipboard: FakeClipboard) -> None:
    fake_clipboard.open_ok = False
    with pytest.raises(probe._ProbeError, match="open-clipboard-failed"):
        probe._inspect()


def test_verify_sequence_matches_and_reports_the_number(fake_clipboard: FakeClipboard) -> None:
    assert probe._verify_sequence(7) == {"ok": True, "verified": True, "sequence": 7}
    assert probe._verify_sequence(6) == {
        "ok": True,
        "verified": False,
        "refused": "sequence-changed",
    }


def test_confirm_owned_requires_the_exact_text(fake_clipboard: FakeClipboard) -> None:
    fake_clipboard.formats = 1
    fake_clipboard._refresh_text("HT-000001\r\n")
    assert probe._confirm_owned("HT-000001\r\n") == {"ok": True, "verified": True, "sequence": 7}
    assert probe._confirm_owned("HT-000001") == {
        "ok": True,
        "verified": False,
        "refused": "unowned-text",
    }
    assert probe._confirm_owned("HT-000001\n")["verified"] is False


def test_confirm_owned_refuses_when_no_text_is_present(fake_clipboard: FakeClipboard) -> None:
    fake_clipboard.formats = 2
    fake_clipboard._refresh_text(None)
    assert probe._confirm_owned("anything") == {
        "ok": True,
        "verified": False,
        "refused": "unowned-text",
    }


def test_read_unicode_text_preserves_exact_bytes(fake_clipboard: FakeClipboard) -> None:
    fake_clipboard._refresh_text("12.5 \r\n")
    assert probe._read_unicode_text() == "12.5 \r\n"
    fake_clipboard._refresh_text("a\x00hidden")
    assert probe._read_unicode_text() == "a"


def test_clear_owned_clears_exactly_the_confirmed_state(fake_clipboard: FakeClipboard) -> None:
    fake_clipboard.formats = 1
    fake_clipboard._refresh_text('{"nested":{"value":8}}')
    result = probe._clear_owned(['{"nested":{"value":8}}'], 7)
    assert result == {"ok": True, "cleared": True, "alreadyEmpty": False, "emptyAfter": True}
    assert fake_clipboard.emptied is True


def test_clear_owned_keeps_content_when_the_sequence_changed(
    fake_clipboard: FakeClipboard,
) -> None:
    fake_clipboard.formats = 1
    fake_clipboard._refresh_text("12.5")
    result = probe._clear_owned(["12.5"], 6)
    assert result == {"ok": True, "cleared": False, "refused": "sequence-changed"}
    assert fake_clipboard.emptied is False
    assert fake_clipboard.formats == 1


def test_clear_owned_never_clears_text_differing_only_by_whitespace(
    fake_clipboard: FakeClipboard,
) -> None:
    fake_clipboard.formats = 1
    fake_clipboard._refresh_text("12.5 ")
    result = probe._clear_owned(["12.5", "12.5\n", "12.5\r\n"], 7)
    assert result == {
        "ok": True,
        "cleared": False,
        "refused": "unowned-text",
        "formatCount": 1,
    }
    assert fake_clipboard.emptied is False


def test_clear_owned_accepts_an_unchanged_empty_clipboard(fake_clipboard: FakeClipboard) -> None:
    assert probe._clear_owned([], 7) == {"ok": True, "cleared": False, "alreadyEmpty": True}


def test_clear_owned_surfaces_a_failed_empty(fake_clipboard: FakeClipboard) -> None:
    fake_clipboard.formats = 1
    fake_clipboard._refresh_text("12.5")
    fake_clipboard.empty_ok = False
    with pytest.raises(probe._ProbeError, match="empty-clipboard-failed"):
        probe._clear_owned(["12.5"], 7)


def test_clear_owned_fails_closed_when_post_clear_verification_errors(
    fake_clipboard: FakeClipboard,
) -> None:
    fake_clipboard.formats = 1
    fake_clipboard._refresh_text("12.5")
    original_empty = fake_clipboard.EmptyClipboard

    def empty_and_break_verification() -> int:
        result = original_empty()
        fake_clipboard.count_error = 1418
        return result

    monkey = pytest.MonkeyPatch()
    monkey.setattr(fake_clipboard, "EmptyClipboard", empty_and_break_verification)
    try:
        result = probe._clear_owned(["12.5"], 7)
    finally:
        monkey.undo()
    assert result == {"ok": False, "error": "count-clipboard-formats-failed"}
    assert fake_clipboard.emptied is True
