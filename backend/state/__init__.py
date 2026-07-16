"""Local user-state storage for B3 grid state.

This package isolates the local-state SQLite database from the business
database. The store lives under ``%LOCALAPPDATA%/VibeTable/state/vibetable-state.db``
and is owned by Python; it is never opened by the WPF host or WebView2.
"""
