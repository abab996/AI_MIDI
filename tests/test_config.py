from __future__ import annotations

import pytest

import config


def test_base_url_accepts_whitelisted_path_and_normalizes_trailing_slash():
    assert config.validate_base_url("https://api.openai.com/v1/") == "https://api.openai.com/v1"


@pytest.mark.parametrize(
    "url",
    [
        "http://api.openai.com/v1",
        "https://example.com/v1",
        "https://api.openai.com:8443/v1",
        "https://api.openai.com/v1?key=value",
        "https://api.openai.com/v1#fragment",
    ],
)
def test_base_url_rejects_unsafe_variants(url):
    with pytest.raises(ValueError):
        config.validate_base_url(url)
