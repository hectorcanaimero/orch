import json
import subprocess
import sys


_CI_STATE_MAP = {
    "success": "success",
    "completed": "success",
    "failure": "failure",
    "timed_out": "failure",
    "action_required": "failure",
    "cancelled": "failure",
    "neutral": "success",
    "skipped": "success",
    "stale": "failure",
}

_MAX_LOG_CHARS = 8000


class GitHubProvider:
    def create_pr(
        self,
        task_id: str,
        title: str,
        body: str,
        head: str,
        base: str,
    ) -> str | None:
        result = subprocess.run(
            ["gh", "pr", "create", "--title", title, "--body", body, "--head", head, "--base", base],
            capture_output=True,
            text=True,
        )
        if result.returncode != 0:
            return None
        return result.stdout.strip() or None

    def get_ci_status(self, pr_url: str) -> str:
        # Bug 27 of the Go port: `gh pr checks` has no `conclusion` field, so
        # asking for one was a usage error (exit 1) that the branch below
        # turned into "pending" — for every PR, forever. And `state` is one
        # field doing two jobs (GitHub's status while a check runs, its
        # conclusion once it finished) and gh emits it UPPERCASE; the maps are
        # lowercase, so even with the field fixed every lookup missed. CI
        # polling never resolved in either binary. Fixtures captured from the
        # real gh 2.100.0 live in tests/fixtures/gh/2.100.0/.
        result = subprocess.run(
            ["gh", "pr", "checks", pr_url, "--json", "name,state,bucket"],
            capture_output=True,
            text=True,
        )
        if result.returncode != 0:
            # "cannot run" is not "still waiting"; say why on stderr so the
            # operator can tell a broken gh from a slow CI.
            print(
                f"[vcs] gh pr checks failed for {pr_url}: "
                f"{(result.stderr or '').strip().splitlines()[0] if (result.stderr or '').strip() else 'exit ' + str(result.returncode)}",
                file=sys.stderr,
            )
            return "pending"

        try:
            checks = json.loads(result.stdout)
        except (json.JSONDecodeError, ValueError):
            return "pending"

        if not checks:
            return "pending"

        states = [str(c.get("state", "")).lower() for c in checks]

        if any(s in ("in_progress", "queued", "waiting", "requested", "pending") for s in states):
            return "pending"

        mapped = [_CI_STATE_MAP.get(s, "pending") for s in states]
        if any(m == "failure" for m in mapped):
            return "failure"
        if all(m == "success" for m in mapped):
            return "success"
        return "pending"

    def get_ci_logs(self, pr_url: str) -> str:
        # Resolve run ID from the PR
        pr_result = subprocess.run(
            ["gh", "pr", "view", pr_url, "--json", "statusCheckRollup"],
            capture_output=True,
            text=True,
        )
        if pr_result.returncode != 0:
            return ""

        try:
            data = json.loads(pr_result.stdout)
            checks = data.get("statusCheckRollup", [])
            # Find a failed workflow run
            run_url = next(
                (c.get("detailsUrl", "") for c in checks if c.get("conclusion") in ("failure", "timed_out")),
                "",
            )
        except (json.JSONDecodeError, ValueError, StopIteration):
            return ""

        if not run_url:
            return ""

        # Extract run ID from URL (…/runs/<id>)
        parts = run_url.rstrip("/").split("/")
        run_id = next((p for p in reversed(parts) if p.isdigit()), "")
        if not run_id:
            return ""

        log_result = subprocess.run(
            ["gh", "run", "view", run_id, "--log-failed"],
            capture_output=True,
            text=True,
        )
        return log_result.stdout[:_MAX_LOG_CHARS]

    def merge_pr(self, pr_url: str) -> bool:
        result = subprocess.run(
            ["gh", "pr", "merge", "--squash", "--auto", pr_url],
            capture_output=True,
            text=True,
        )
        return result.returncode == 0
