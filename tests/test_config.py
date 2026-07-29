from __future__ import annotations

import pytest

import config


def test_base_url_accepts_whitelisted_path_and_normalizes_trailing_slash():
    assert config.validate_base_url("https://api.openai.com/v1/") == "https://api.openai.com/v1"


def test_base_url_combines_with_api_path_for_gemini():
    assert (
        config.validate_base_url("https://generativelanguage.googleapis.com", "/v1beta/openai/")
        == "https://generativelanguage.googleapis.com/v1beta/openai"
    )
    assert (
        config.validate_base_url("https://generativelanguage.googleapis.com", "v1beta/openai")
        == "https://generativelanguage.googleapis.com/v1beta/openai"
    )
    assert (
        config.validate_base_url("https://generativelanguage.googleapis.com/v1beta/openai", "")
        == "https://generativelanguage.googleapis.com/v1beta/openai"
    )


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
