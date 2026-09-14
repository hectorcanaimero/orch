#!/usr/bin/env python3
"""Turn the triager's raw stdout into a validated triage, or fail loudly.

Same envelope handling as parse-review.py (imported, not copied: bare JSON,
a ```json fence, prose around the object, the Gemini CLI's empty-response
envelope), strict about the contents. A priority outside the enum is a parse
failure, and a failed triage labels nothing.

Usage:
    parse-triage.py --raw raw.txt --out triage.json --comment comment.md
                    [--milestone v0.12.1] [--owner name] [--run-url https://...]

Exit status: 0 valid, 1 unreadable (do not retry), 2 empty (retry once).
"""

from __future__ import annotations

import argparse
import importlib.util
import json
import pathlib
import sys

_spec = importlib.util.spec_from_file_location(
    "parse_review", pathlib.Path(__file__).with_name("parse-review.py")
)
parse_review = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(parse_review)

PRIORITIES = ("urgent", "planned")
TYPES = ("bug", "improvement", "feature")
SEVERITIES = ("critical", "high", "medium", "low")

MARKER = "<!-- orch:gemini-triage -->"


def validate(payload) -> dict:
    if not isinstance(payload, dict):
        raise ValueError("the triager returned a JSON value that is not an object")

    def enum(key, allowed):
        value = str(payload.get(key, "")).strip().lower()
        if value not in allowed:
            raise ValueError(f"`{key}` must be one of {allowed}, got {payload.get(key)!r}")
        return value

    out = {
        "priority": enum("priority", PRIORITIES),
        "type": enum("type", TYPES),
        "severity": enum("severity", SEVERITIES),
    }
    for key in ("summary", "reasoning"):
        text = str(payload.get(key, "")).strip()
        if not text:
            raise ValueError(f"`{key}` is empty")
        out[key] = text
    out["workaround"] = str(payload.get("workaround") or "").strip()

    areas = payload.get("areas") or []
    if not isinstance(areas, list):
        raise ValueError("`areas` must be a list")
    out["areas"] = [str(a).strip() for a in areas if str(a).strip()][:8]
    return out


def render_comment(t: dict, *, milestone: str, owner: str, run_url: str) -> str:
    headline = {
        "urgent": f"🚨 **Urgent** — proposed for the patch release `{milestone}`.",
        "planned": f"🗓️ **Planned** — proposed for the next batch, `{milestone}`.",
    }[t["priority"]]
    lines = [MARKER, "## Gemini triage", "", headline, "", t["summary"], ""]
    lines += [
        f"- **Type**: {t['type']}",
        f"- **Severity**: {t['severity']}",
        f"- **Workaround**: {t['workaround'] or 'none found'}",
    ]
    if t["areas"]:
        lines.append("- **Likely areas**: " + ", ".join(f"`{a}`" for a in t["areas"]))
    lines += ["", "**Why**", "", t["reasoning"], ""]
    if t["priority"] == "urgent" and owner:
        lines += [f"cc @{owner}", ""]
    lines += [
        "---",
        "",
        "_Proposal, not a decision: change the labels or milestone if you disagree. "
        "Add `claude:go` to hand this issue to the hourly Claude routine._ "
        + (f"[Run log]({run_url})" if run_url else ""),
    ]
    return "\n".join(lines).rstrip() + "\n"


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--raw", required=True)
    ap.add_argument("--out", required=True)
    ap.add_argument("--comment")
    ap.add_argument("--milestone", default="")
    ap.add_argument("--owner", default="")
    ap.add_argument("--run-url", default="")
    args = ap.parse_args()

    try:
        raw = pathlib.Path(args.raw).read_text(encoding="utf-8")
    except OSError as exc:
        print(f"parse-triage: cannot read {args.raw}: {exc}", file=sys.stderr)
        return 1

    try:
        triage = validate(parse_review.extract_json(raw))
    except parse_review.EmptyReview as exc:
        print(f"parse-triage: {exc}", file=sys.stderr)
        return 2
    except (ValueError, json.JSONDecodeError) as exc:
        print(f"parse-triage: {exc}", file=sys.stderr)
        return 1

    pathlib.Path(args.out).write_text(json.dumps(triage, indent=2) + "\n", encoding="utf-8")
    if args.comment:
        pathlib.Path(args.comment).write_text(
            render_comment(triage, milestone=args.milestone, owner=args.owner, run_url=args.run_url),
            encoding="utf-8",
        )
    print(f"{triage['priority']} {triage['type']}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
