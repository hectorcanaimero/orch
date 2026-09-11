#!/usr/bin/env python3
"""Capture what the PYTHON atomizer says about this package's fixtures.

The Go package (internal/atomize) has to parse, merge and diff exactly the
way orchestrator/atomize.py does. Run with a venv holding the orch v0.11.0
Python package (see the module docstring in ../../graph/testdata/make-goldens.py
for the same pattern):

    <venv>/bin/python internal/atomize/testdata/make-goldens.py

Three goldens:
  - parse-sample.golden.json      parse_spec_file(specs/sample_spec.md)
  - parse-orch-spec.golden.json   parse_spec_file(specs/orch_spec_output.md),
                                   expected_project_id="sample-project"
                                   (exercises frontmatter parsing)
  - merge.golden.json             parse_specs([specs/merge_a.md, specs/merge_b.md])
                                   merged into existing/merge_existing.json —
                                   exercises new/updated/unchanged/orphan/
                                   dep-warning buckets and cross-file
                                   duplicate-ID resolution (last file wins)
                                   in one scenario.
"""
import json
from dataclasses import asdict
from pathlib import Path

from orchestrator.atomize import (
    _iter_spec_files,
    load_raw_tasks_json,
    merge_tasks,
    parse_spec_file,
    parse_specs,
)

HERE = Path(__file__).resolve().parent
SPECS = HERE / "specs"
EXISTING = HERE / "existing"
GOLDEN = HERE / "golden"
GOLDEN.mkdir(exist_ok=True)


def _portable_task(t) -> dict:
    """asdict(t) with source_file trimmed to a basename.

    The absolute path is CI/checkout-location-dependent — normalize it the
    same way filesScanned already is, so the golden is byte-for-byte
    reproducible across machines.
    """
    row = asdict(t)
    row["source_file"] = Path(row["source_file"]).name
    return row


def dump_parse_result(result) -> dict:
    return {
        "tasks": [_portable_task(t) for t in result.tasks],
        "filesScanned": [str(p.name) for p in result.files_scanned],
        "warnings": [_portable_warning(w) for w in result.warnings],
    }


def _portable_warning(w: str) -> str:
    """Warnings interpolate absolute source paths for duplicate-ID messages
    (see parse_specs) — trim any testdata/specs/ absolute prefix so the
    golden doesn't bake in this checkout's path."""
    return w.replace(str(SPECS) + "/", "")


def write(name: str, data) -> None:
    (GOLDEN / name).write_text(
        json.dumps(data, indent=2, sort_keys=True, ensure_ascii=False) + "\n",
        encoding="utf-8",
    )


# ---- 1. sample_spec.md (plain, no frontmatter) ----------------------------
sample = parse_spec_file(SPECS / "sample_spec.md", SPECS)
write("parse-sample.golden.json", dump_parse_result(sample))

# ---- 2. orch_spec_output.md (frontmatter + accented ES labels) ------------
orch_spec = parse_spec_file(
    SPECS / "orch_spec_output.md", SPECS, expected_project_id="sample-project"
)
write("parse-orch-spec.golden.json", dump_parse_result(orch_spec))

# ---- 3. merge scenario: two files, duplicate ID, dep warning, orphans -----
files = _iter_spec_files(SPECS, None)
merge_files = [p for p in files if p.name in ("merge_a.md", "merge_b.md")]
assert len(merge_files) == 2, merge_files

parsed = parse_specs(merge_files, SPECS)
existing = load_raw_tasks_json(EXISTING / "merge_existing.json")
merged, diff = merge_tasks(existing, parsed.tasks)

write(
    "merge.golden.json",
    {
        "parse": dump_parse_result(parsed),
        "merged": merged,
        "diff": {
            "newTasks": [r["id"] for r in diff.new_tasks],
            "updated": [
                {"id": new["id"], "changed": sorted(changed)}
                for new, old, changed in diff.updated
            ],
            "unchanged": [r["id"] for r in diff.unchanged],
            "orphans": [r["id"] for r in diff.orphans],
            "depWarnings": diff.dep_warnings,
        },
    },
)

print(
    f"sample: {len(sample.tasks)} tasks, {len(sample.warnings)} warnings\n"
    f"orch_spec: {len(orch_spec.tasks)} tasks, {len(orch_spec.warnings)} warnings\n"
    f"merge: {len(diff.new_tasks)} new, {len(diff.updated)} updated, "
    f"{len(diff.unchanged)} unchanged, {len(diff.orphans)} orphans, "
    f"{len(diff.dep_warnings)} dep warnings"
)
