"""Native clipboard probe for the isolated WebView product E2E runs.

Only invoked after the isolated-desktop gate (GitHub-hosted runner or
``VIBETABLE_NATIVE_CLIPBOARD_TESTS=1``). ``inspect`` proves all-format
emptiness before the first native write (zero formats must be confirmed with
``GetLastError``). ``verify-sequence``/``confirm-owned``/``clear-owned`` bind
cleanup to this run's writes via ``GetClipboardSequenceNumber`` and exact text
(no whitespace normalization). Output carries booleans, counts, sequence
numbers and error labels only, never clipboard content.
"""

from __future__ import annotations

import ctypes
import json
import sys
from ctypes import wintypes
from types import TracebackType

CF_UNICODETEXT = 13

_IS_WINDOWS = sys.platform == "win32"

if _IS_WINDOWS:
    _user32 = ctypes.WinDLL("user32", use_last_error=True)
    _user32.OpenClipboard.argtypes = [wintypes.HWND]
    _user32.OpenClipboard.restype = wintypes.BOOL
    _user32.CloseClipboard.argtypes = []
    _user32.CloseClipboard.restype = wintypes.BOOL
    _user32.EmptyClipboard.argtypes = []
    _user32.EmptyClipboard.restype = wintypes.BOOL
    _user32.CountClipboardFormats.argtypes = []
    _user32.CountClipboardFormats.restype = ctypes.c_int
    _user32.GetClipboardSequenceNumber.argtypes = []
    _user32.GetClipboardSequenceNumber.restype = wintypes.DWORD
    _user32.GetClipboardData.argtypes = [wintypes.UINT]
    _user32.GetClipboardData.restype = wintypes.HANDLE

    _kernel32 = ctypes.WinDLL("kernel32", use_last_error=True)
    _kernel32.GlobalLock.argtypes = [wintypes.HGLOBAL]
    _kernel32.GlobalLock.restype = wintypes.LPVOID
    _kernel32.GlobalUnlock.argtypes = [wintypes.HGLOBAL]
    _kernel32.GlobalUnlock.restype = wintypes.BOOL
    _kernel32.GlobalSize.argtypes = [wintypes.HGLOBAL]
    _kernel32.GlobalSize.restype = ctypes.c_size_t


class _ProbeError(Exception):
    """A labeled probe failure that never carries clipboard content."""


class _ClipboardSession:
    def __enter__(self) -> _ClipboardSession:
        if not _user32.OpenClipboard(None):
            raise _ProbeError("open-clipboard-failed")
        return self

    def __exit__(
        self,
        exc_type: type[BaseException] | None,
        exc_value: BaseException | None,
        traceback: TracebackType | None,
    ) -> None:
        _user32.CloseClipboard()


def _emit(payload: dict[str, object]) -> None:
    sys.stdout.write(f"{json.dumps(payload, ensure_ascii=True)}\n")
    sys.stdout.flush()


def _sequence() -> int:
    return int(_user32.GetClipboardSequenceNumber())


def _format_count() -> int:
    # Zero formats means empty only when no error is recorded; a genuine
    # failure must fail closed instead of masquerading as an empty clipboard.
    ctypes.set_last_error(0)
    count = int(_user32.CountClipboardFormats())
    if count == 0 and ctypes.get_last_error() != 0:
        raise _ProbeError("count-clipboard-formats-failed")
    return count


def _read_unicode_text() -> str | None:
    handle = _user32.GetClipboardData(CF_UNICODETEXT)
    if not handle:
        return None
    size = int(_kernel32.GlobalSize(handle))
    if size < 2:
        return ""
    locked = _kernel32.GlobalLock(handle)
    if not locked:
        raise _ProbeError("lock-clipboard-text-failed")
    try:
        raw = ctypes.string_at(locked, size - (size % 2))
    finally:
        _kernel32.GlobalUnlock(handle)
    return raw.decode("utf-16-le", errors="replace").split("\x00", 1)[0]


def _inspect() -> dict[str, object]:
    with _ClipboardSession():
        count = _format_count()
        sequence = _sequence()
    return {"ok": True, "empty": count == 0, "formatCount": count, "sequence": sequence}


def _verify_sequence(expected: int) -> dict[str, object]:
    current = _sequence()
    if current != expected:
        return {"ok": True, "verified": False, "refused": "sequence-changed"}
    return {"ok": True, "verified": True, "sequence": current}


def _confirm_owned(text: str) -> dict[str, object]:
    # The open lock keeps the verified text and the captured sequence number
    # stable against concurrent modifiers while both are read.
    with _ClipboardSession():
        if _read_unicode_text() != text:
            return {"ok": True, "verified": False, "refused": "unowned-text"}
        sequence = _sequence()
    return {"ok": True, "verified": True, "sequence": sequence}


def _clear_owned(owned_texts: list[str], expected_sequence: int) -> dict[str, object]:
    owned = set(owned_texts)
    with _ClipboardSession():
        if _sequence() != expected_sequence:
            return {"ok": True, "cleared": False, "refused": "sequence-changed"}
        count = _format_count()
        if count == 0:
            return {"ok": True, "cleared": False, "alreadyEmpty": True}
        text = _read_unicode_text()
        if text is None or text not in owned:
            return {"ok": True, "cleared": False, "refused": "unowned-text", "formatCount": count}
        if not _user32.EmptyClipboard():
            raise _ProbeError("empty-clipboard-failed")
    try:
        verification = _inspect()
    except _ProbeError as error:
        return {"ok": False, "error": str(error)}
    return {"ok": True, "cleared": True, "alreadyEmpty": False, "emptyAfter": verification["empty"]}


def _read_request() -> dict[str, object]:
    request = json.loads(sys.stdin.read() or "{}")
    if not isinstance(request, dict):
        raise _ProbeError("request-must-be-object")
    return request


def _require_int(request: dict[str, object], key: str) -> int:
    value = request.get(key)
    if isinstance(value, bool) or not isinstance(value, int):
        raise _ProbeError(f"{key}-must-be-int")
    return value


def _require_str(request: dict[str, object], key: str) -> str:
    value = request.get(key)
    if not isinstance(value, str):
        raise _ProbeError(f"{key}-must-be-string")
    return value


def _require_str_list(request: dict[str, object], key: str) -> list[str]:
    value = request.get(key)
    if not isinstance(value, list) or any(not isinstance(item, str) for item in value):
        raise _ProbeError(f"{key}-must-be-strings")
    return value


def main(argv: list[str]) -> int:
    try:
        if not _IS_WINDOWS:
            _emit({"ok": False, "error": "windows-only-probe"})
            return 2
        command = argv[0] if argv else ""
        if command == "inspect":
            _emit(_inspect())
            return 0
        if command in ("verify-sequence", "confirm-owned", "clear-owned"):
            request = _read_request()
            if command == "verify-sequence":
                result = _verify_sequence(_require_int(request, "expectedSequence"))
            elif command == "confirm-owned":
                result = _confirm_owned(_require_str(request, "text"))
            else:
                result = _clear_owned(
                    _require_str_list(request, "ownedTexts"),
                    _require_int(request, "expectedSequence"),
                )
            _emit(result)
            return 0
        _emit({"ok": False, "error": "unknown-command"})
        return 2
    except _ProbeError as error:
        _emit({"ok": False, "error": str(error)})
        return 1
    except (OSError, UnicodeError, json.JSONDecodeError) as error:
        _emit({"ok": False, "error": f"probe-failure:{type(error).__name__}"})
        return 1


if __name__ == "__main__":
    raise SystemExit(main(sys.argv[1:]))
