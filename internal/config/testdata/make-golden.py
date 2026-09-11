#!/usr/bin/env python3
"""Dump the effective config Python produces for each input, as sorted YAML.

These goldens are the specification for internal/config: whatever Go loads for
the same input must equal the file here. Generated with orch v0.11.0.

    /tmp/orch-py/bin/python internal/config/testdata/make-golden.py
"""
import sys
from pathlib import Path

import yaml

from orchestrator.config_loader import load_config

HERE = Path(__file__).resolve().parent
REPO = HERE.parents[2]
TEMPLATES = REPO / "orchestrator" / "templates" / "projects"

CASES: list[tuple[str, str]] = []
for tpl in sorted(p.name for p in TEMPLATES.iterdir() if p.is_dir()):
    src = TEMPLATES / tpl / "config.yaml.tmpl"
    if src.exists():
        CASES.append((f"template-{tpl}", src.read_text(encoding="utf-8")))

CASES.append(("packaged", (REPO / "orchestrator" / "config.yaml").read_text(encoding="utf-8")))
CASES.append(("empty", ""))
CASES.append(("legacy-keys", """
findings:
  publish_repo: "someone/somewhere"
  label: "auto-reported"
dashboard:
  board_url: "https://draw.example.invalid/editor/abc"
  profile: stakeholder
  token: s3cr3t
spec_root: docs/specs
"""))
CASES.append(("backend-file", "state:\n  backend: file\n  sqlite_path: null\n"))

out_dir = HERE
for name, text in CASES:
    (out_dir / f"{name}.input.yaml").write_text(text, encoding="utf-8")
    cfg_path = out_dir / f"{name}.input.yaml"
    effective = load_config(cfg_path, project_root=out_dir)
    dumped = yaml.safe_dump(effective, sort_keys=True, default_flow_style=False,
                            allow_unicode=True, width=100)
    (out_dir / f"{name}.effective.yaml").write_text(dumped, encoding="utf-8")
    print(f"{name}: {len(dumped)} bytes")
