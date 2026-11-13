import hashlib
import json
import re
from pathlib import Path

import pytest

from contract import BUDGET_HEADER, MAX_BUDGET_MILLISECONDS, PROJECTION, REQUIRED_FILES, SCHEMA, SYSTEM_PROMPT, verify_release


def test_go_and_python_prompt_contract_match():
    go_source = (Path(__file__).resolve().parents[3] / "internal/router/taskdomain/release.go").read_text()
    assert f"const SystemPrompt = {json.dumps(SYSTEM_PROMPT)}" in go_source


def test_release_pins_complete_model_inventory(tmp_path):
    model = tmp_path / "model"
    model.mkdir()
    files = {}
    for name in REQUIRED_FILES:
        content = ("synthetic " + name).encode()
        (model / name).write_bytes(content)
        files[name] = hashlib.sha256(content).hexdigest()
    manifest = tmp_path / "release.json"
    manifest.write_text(json.dumps({"schema_version": SCHEMA, "projection_version": PROJECTION,
        "prompt_sha256": hashlib.sha256(SYSTEM_PROMPT.encode()).hexdigest(),
        "files": files, "evidence": {"a" * 64: "b" * 64}}))
    digest = hashlib.sha256(manifest.read_bytes()).hexdigest()
    assert verify_release(manifest, model, digest) == digest
    with pytest.raises(ValueError, match="release digest mismatch"):
        verify_release(manifest, model, "c" * 64)
    (model / "extra.json").write_text("unreviewed tokenizer config")
    with pytest.raises(ValueError, match="inventory"):
        verify_release(manifest, model, digest)
    (model / "extra.json").unlink()
    (model / "tokenizer.json").write_text("tampered")
    with pytest.raises(ValueError, match="artifact digest mismatch"):
        verify_release(manifest, model, digest)


def test_go_and_python_budget_header_match():
    go_source = (Path(__file__).resolve().parents[3] / "internal/router/taskdomain/contracts.go").read_text()
    assert f'BudgetHeader = "{BUDGET_HEADER}"' in go_source
    assert re.search(rf"MaxBudgetMilliseconds\s*=\s*{MAX_BUDGET_MILLISECONDS:_}\b", go_source)
