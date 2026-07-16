from __future__ import annotations

import logging
import sys

from backend.__main__ import _configure_logging


def test_backend_logging_removes_legacy_app_stdout_handler(capfd) -> None:
    root = logging.getLogger()
    app = logging.getLogger("app")
    saved_root = (list(root.handlers), root.level, root.propagate)
    saved_app = (list(app.handlers), app.level, app.propagate)

    try:
        app.handlers = [logging.StreamHandler(sys.stdout)]
        app.setLevel(logging.INFO)
        app.propagate = True

        _configure_logging()
        logging.getLogger("app.core.database.async_connection").info("protocol pollution sentinel")

        captured = capfd.readouterr()
        assert captured.out == ""
        assert "protocol pollution sentinel" in captured.err
    finally:
        root.handlers, root.level, root.propagate = saved_root
        app.handlers, app.level, app.propagate = saved_app
