"""Structured failure reported by the owning Host's file capability."""


class PathGrantError(Exception):
    def __init__(self, message: str, *, code: str) -> None:
        super().__init__(message)
        self.code = code
