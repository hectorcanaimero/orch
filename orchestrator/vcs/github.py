import json
import subprocess


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
        result = subprocess.run(
            ["gh", "pr", "checks", pr_url, "--json", "state,conclusion"],
            capture_output=True,
            text=True,
        )
        if result.returncode != 0:
            return "pending"

        try:
            checks = json.loads(result.stdout)
        except (json.JSONDecodeError, ValueError):
            return "pending"

        if not checks:
            return "pending"

        conclusions = [c.get("conclusion", "") for c in checks]
        states = [c.get("state", "") for c in checks]

        if any(s in ("in_progress", "queued", "waiting", "requested", "pending") for s in states):
            return "pending"

        mapped = [_CI_STATE_MAP.get(c, "pending") for c in conclusions]
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
