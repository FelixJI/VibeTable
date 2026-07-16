from __future__ import annotations

import pytest
from pydantic import ValidationError

from backend.adapters.directus import DirectusCapabilities, DirectusSourceConfig


def test_source_config_normalizes_url_without_secret_material() -> None:
    config = DirectusSourceConfig(
        url="HTTPS://DIRECTUS.EXAMPLE.COM/api/",
        project="production",
        token_ref="windows-credential:vibetable/directus-prod",
    )

    assert config.url == "https://directus.example.com/api"
    assert config.verify_tls is True
    assert "token" not in config.model_dump()
    assert config.model_dump()["token_ref"].startswith("windows-credential:")


@pytest.mark.parametrize(
    "url",
    [
        "directus.example.com",
        "ftp://directus.example.com",
        "https://user:secret@directus.example.com",
        "https://directus.example.com?access_token=secret",
        "https://directus.example.com/#secret",
    ],
)
def test_source_config_rejects_unsafe_urls(url: str) -> None:
    with pytest.raises(ValidationError):
        DirectusSourceConfig(
            url=url,
            project="production",
            token_ref="credential:prod",
        )


def test_phase_zero_capabilities_are_read_only_and_explicit() -> None:
    capabilities = DirectusCapabilities()

    assert capabilities.read is True
    assert capabilities.create is False
    assert capabilities.update is False
    assert capabilities.delete is False
    assert capabilities.batch_atomic is False
    assert capabilities.optimistic_concurrency is False
    assert capabilities.explicit_null_ordering is False
