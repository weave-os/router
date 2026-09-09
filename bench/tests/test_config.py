from __future__ import annotations

from pathlib import Path

import pytest

from weave_bench.config import HarborEnvironment, load_config, require_secret


def test_defaults_without_a_file() -> None:
    config = load_config(None, env={})
    assert config.router.base_url == "http://localhost:8080"
    assert config.router.analytics_url == config.router.base_url
    assert config.harbor.environment is HarborEnvironment.DOCKER
    assert config.openrouter_tap.listen_port == 8787


def test_toml_values_are_typed(tmp_path: Path) -> None:
    toml = tmp_path / "bench.toml"
    toml.write_text(
        """
[router]
base_url = "https://router.example.test"
analytics_base_url = "https://analytics.example.test"

[harbor]
environment = "modal"

[openrouter_tap]
listen_port = 9000
"""
    )
    config = load_config(toml, env={})
    assert config.router.base_url == "https://router.example.test"
    assert config.router.analytics_url == "https://analytics.example.test"
    assert config.harbor.environment is HarborEnvironment.MODAL
    assert config.openrouter_tap.listen_port == 9000
    assert config.openrouter_tap.listen_host == "172.17.0.1"


def test_env_overrides_beat_the_file(tmp_path: Path) -> None:
    toml = tmp_path / "bench.toml"
    toml.write_text('[router]\nbase_url = "http://from-file"\n')
    config = load_config(
        toml,
        env={
            "WEAVE_BENCH_ROUTER_BASE_URL": "http://from-env",
            "WEAVE_BENCH_HARBOR_ENVIRONMENT": "modal",
            "WEAVE_BENCH_OPENROUTER_TAP_LISTEN_PORT": "9001",
        },
    )
    assert config.router.base_url == "http://from-env"
    assert config.harbor.environment is HarborEnvironment.MODAL
    assert config.openrouter_tap.listen_port == 9001


def test_unknown_section_and_key_are_rejected(tmp_path: Path) -> None:
    toml = tmp_path / "bench.toml"
    toml.write_text("[nope]\nx = 1\n")
    with pytest.raises(ValueError, match="unknown sections"):
        load_config(toml, env={})
    toml.write_text('[router]\napi_key = "literal-secret"\n')
    with pytest.raises(ValueError, match="unknown keys"):
        load_config(toml, env={})


def test_invalid_environment_value_is_rejected(tmp_path: Path) -> None:
    toml = tmp_path / "bench.toml"
    toml.write_text('[harbor]\nenvironment = "kubernetes"\n')
    with pytest.raises(ValueError):
        load_config(toml, env={})


def test_require_secret_names_the_variable() -> None:
    assert require_secret("K", env={"K": "v"}) == "v"
    with pytest.raises(RuntimeError, match="MISSING_KEY is not set"):
        require_secret("MISSING_KEY", env={"MISSING_KEY": ""})
