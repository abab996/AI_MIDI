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


def test_base_url_accepts_any_openai_format_domain():
    assert (
        config.validate_base_url("https://opencode.ai/v1/")
        == "https://opencode.ai/v1"
    )
    assert config.validate_base_url("https://example.com") == "https://example.com"


def test_base_url_ignores_api_path_for_non_gemini_providers():
    assert (
        config.validate_base_url("https://opencode.ai", "/v1beta/openai")
        == "https://opencode.ai"
    )
    assert (
        config.validate_base_url("https://api.openai.com/v1", "/v1beta/openai")
        == "https://api.openai.com/v1"
    )


def test_is_gemini_provider():
    assert config.is_gemini_provider("https://generativelanguage.googleapis.com")
    assert config.is_gemini_provider("generativelanguage.googleapis.com")
    assert not config.is_gemini_provider("https://opencode.ai")
    assert not config.is_gemini_provider("https://api.openai.com/v1")
    assert not config.is_gemini_provider("")


@pytest.mark.parametrize(
    "url",
    [
        "http://api.openai.com/v1",
        "https://api.openai.com:8443/v1",
        "https://api.openai.com/v1?key=value",
        "https://api.openai.com/v1#fragment",
    ],
)
def test_base_url_rejects_unsafe_variants(url):
    with pytest.raises(ValueError):
        config.validate_base_url(url)
