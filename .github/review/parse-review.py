#!/usr/bin/env python3
"""Turn the reviewer's raw stdout into a validated verdict, or fail loudly.

The model is asked for JSON and nothing else. It will sometimes wrap it in a
```json fence or add a sentence first, so this is deliberately forgiving about
the envelope and strict about the contents: a verdict outside the enum, or a
finding missing `rule`, is a parse failure — and a parse failure means a
`neutral` check, never a green one.

Usage:
    parse-review.py --raw raw.txt --out review.json [--comment comment.md]
                    [--eligible true|false] [--eligible-reason "..."]
                    [--pr 12] [--run-url https://...]

Exit status:
    0  valid verdict written to --out
    1  could not parse (stderr says why) — the caller marks the check neutral
"""

from __future__ import annotations

import argparse
import json
import re
import sys

VERDICTS = ("approve", "request_changes", "comment")

# Lets a re-run find and update its own comment instead of stacking a new one.
MARKER = "<!-- orch:gemini-review -->"


def extract_json(raw: str) -> dict:
    """Pull the JSON object out of whatever the model actually printed."""
    text = raw.strip()
    if not text:
        raise ValueError("the reviewer produced no output at all")

    # ```json ... ``` or ``` ... ```
    fenced = re.search(r"```(?:json)?\s*\n(.*?)\n\s*```", text, re.DOTALL)
    if fenced:
        text = fenced.group(1).strip()

    try:
        return json.loads(text)
    except json.JSONDecodeError:
        pass

    # Prose before or after the object: take the outermost braces.
    start, end = text.find("{"), text.rfind("}")
    if start == -1 or end <= start:
        raise ValueError(
            "no JSON object found in the reviewer output "
            f"(first 200 chars: {text[:200]!r})"
        )
    return json.loads(text[start : end + 1])


def normalise_findings(value, bucket: str) -> list[dict]:
    if value is None:
        return []
    if not isinstance(value, list):
        raise ValueError(f"`{bucket}` must be a list, got {type(value).__name__}")
    out = []
    for i, item in enumerate(value):
        if not isinstance(item, dict):
            raise ValueError(f"`{bucket}[{i}]` must be an object")
        for required in ("file", "rule", "why"):
            if not str(item.get(required, "")).strip():
                raise ValueError(f"`{bucket}[{i}]` is missing `{required}`")
        entry = {
            "file": str(item["file"]).strip(),
            "rule": str(item["rule"]).strip(),
            "why": str(item["why"]).strip(),
        }
        line = item.get("line")
        if line not in (None, "", 0):
            try:
                entry["line"] = int(line)
            except (TypeError, ValueError):
                pass  # a non-numeric line is not worth failing the review over
        out.append(entry)
    return out


def validate(payload: dict) -> dict:
    if not isinstance(payload, dict):
        raise ValueError("the reviewer returned a JSON value that is not an object")

    verdict = str(payload.get("verdict", "")).strip().lower()
    if verdict not in VERDICTS:
        raise ValueError(
            f"`verdict` must be one of {VERDICTS}, got {payload.get('verdict')!r}"
        )

    blocking = normalise_findings(payload.get("blocking"), "blocking")
    minor = normalise_findings(payload.get("minor"), "minor")
    summary = str(payload.get("summary", "")).strip()
    if not summary:
        raise ValueError("`summary` is empty")

    # A verdict that contradicts its own findings is a model slip, not a
    # judgement call — trust the findings, which are the checkable part.
    if blocking and verdict != "request_changes":
        verdict = "request_changes"

    return {
        "verdict": verdict,
        "blocking": blocking,
        "minor": minor,
        "summary": summary,
    }


def render_findings(title: str, findings: list[dict]) -> list[str]:
    if not findings:
        return []
    lines = [f"**{title}**", ""]
    for f in findings:
        where = f["file"] + (f":{f['line']}" if "line" in f else "")
        lines.append(f"- `{where}` — **{f['rule']}**: {f['why']}")
    lines.append("")
    return lines


def render_comment(
    review: dict,
    *,
    eligible: str | None,
    eligible_reason: str | None,
    run_url: str | None,
) -> str:
    headline = {
        "approve": "✅ **Approve** — nothing blocking.",
        "request_changes": "🛑 **Changes requested.**",
        "comment": "💬 **Comments only** — nothing blocking.",
    }[review["verdict"]]

    lines = [
        MARKER,
        "## Gemini review",
        "",
        headline,
        "",
        review["summary"],
        "",
    ]
    lines += render_findings("Blocking", review["blocking"])
    lines += render_findings("Minor", review["minor"])

    if eligible is not None:
        if eligible == "true":
            lines += [f"🔓 **Auto-merge eligible** — {eligible_reason}."]
        else:
            lines += [f"🔒 **Not auto-merge eligible** — {eligible_reason}."]
        lines += [
            "",
            "_Eligibility is informational: nothing merges automatically yet "
            "(see `docs/CI-REVIEW.md`)._",
            "",
        ]

    lines += [
        "---",
        "",
        "_Advisory. A human still owns the merge._ "
        + (f"[Run log]({run_url})" if run_url else ""),
    ]
    return "\n".join(lines).rstrip() + "\n"


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--raw", required=True)
    ap.add_argument("--out", required=True)
    ap.add_argument("--comment")
    ap.add_argument("--eligible", choices=["true", "false"])
    ap.add_argument("--eligible-reason", default="")
    ap.add_argument("--run-url", default="")
    args = ap.parse_args()

    try:
        with open(args.raw, encoding="utf-8") as fh:
            raw = fh.read()
    except OSError as exc:
        print(f"parse-review: cannot read {args.raw}: {exc}", file=sys.stderr)
        return 1

    try:
        review = validate(extract_json(raw))
    except (ValueError, json.JSONDecodeError) as exc:
        print(f"parse-review: {exc}", file=sys.stderr)
        return 1

    with open(args.out, "w", encoding="utf-8") as fh:
        json.dump(review, fh, indent=2)
        fh.write("\n")

    if args.comment:
        body = render_comment(
            review,
            eligible=args.eligible,
            eligible_reason=args.eligible_reason or "no reason given",
            run_url=args.run_url or None,
        )
        with open(args.comment, "w", encoding="utf-8") as fh:
            fh.write(body)

    print(review["verdict"])
    return 0


if __name__ == "__main__":
    sys.exit(main())
