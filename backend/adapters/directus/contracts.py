"""Configuration and capability contracts for a Directus data source."""

from __future__ import annotations

from urllib.parse import urlsplit, urlunsplit

from pydantic import BaseModel, ConfigDict, Field, field_validator


class DirectusSourceConfig(BaseModel):
    """Non-secret connection configuration.

    ``token_ref`` names an external credential.  The token value itself must
    never enter this model because the model may be serialized into RPC or
    diagnostics later in the migration.
    """

    model_config = ConfigDict(extra="forbid", frozen=True)

    url: str
    project: str = Field(min_length=1, max_length=128)
    token_ref: str = Field(min_length=1, max_length=512)
    verify_tls: bool = True
    request_timeout_seconds: float = Field(default=30.0, gt=0, le=120)

    @field_validator("url")
    @classmethod
    def validate_url(cls, value: str) -> str:
        raw = value.strip()
        parsed = urlsplit(raw)
        if parsed.scheme not in {"http", "https"} or not parsed.hostname:
            raise ValueError("Directus URL must be an absolute http(s) URL")
        if parsed.username or parsed.password:
            raise ValueError("Directus URL must not contain credentials")
        if parsed.query or parsed.fragment:
            raise ValueError("Directus URL must not contain query or fragment data")
        path = parsed.path.rstrip("/")
        return urlunsplit((parsed.scheme.lower(), parsed.netloc.lower(), path, "", ""))


class DirectusCapabilities(BaseModel):
    """Capabilities advertised by the Phase-0/1 read-only adapter."""

    model_config = ConfigDict(extra="forbid", frozen=True)

    read: bool = True
    create: bool = False
    update: bool = False
    delete: bool = False
    batch_atomic: bool = False
    optimistic_concurrency: bool = False
    explicit_null_ordering: bool = False
