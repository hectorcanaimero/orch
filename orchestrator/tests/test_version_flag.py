"""`orch --version` prints the installed version and exits 0 (G-0 CLI polish)."""

from __future__ import annotations

import importlib.metadata

import pytest

from orchestrator import orch as orch_mod


def test_version_flag_prints_version_and_exits_zero(
    capsys: pytest.CaptureFixture,
) -> None:
    with pytest.raises(SystemExit) as exc:
        orch_mod.main(["--version"])
    assert exc.value.code == 0
    out = capsys.readouterr().out
    expected = importlib.metadata.version("orchestrator")
    assert out.strip() == f"orch {expected}"
