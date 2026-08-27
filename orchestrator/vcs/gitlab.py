import json
import os
import subprocess


_CI_STATE_MAP = {
    "success": "success",
    "failed": "failure",
    "canceled": "failure",
    "skipped": "success",
    "running": "pending",
    "pending": "pending",
    "created": "pending",
    "waiting_for_resource": "pending",
    "preparing": "pending",
    "scheduled": "pending",
    "manual": "pending",
}

_MAX_LOG_CHARS = 8000


class GitLabProvider:
    def __init__(self, host: str = "gitlab.com") -> None:
        self.host = host

    def _env(self) -> dict:
        env = os.environ.copy()
        env["GITLAB_HOST"] = self.host
        return env

    def create_pr(
        self,
        task_id: str,
        title: str,
        body: str,
        head: str,
        base: str,
    ) -> str | None:
        result = subprocess.run(
            [
                "glab", "mr", "create",
                "--title", title,
                "--description", body,
                "--source-branch", head,
                "--target-branch", base,
                "--yes",
            ],
            capture_output=True,
            text=True,
            env=self._env(),
        )
        if result.returncode != 0:
            return None
        # glab prints the MR URL on the last non-empty line
        lines = [l.strip() for l in result.stdout.splitlines() if l.strip()]
        return lines[-1] if lines else None

    def get_ci_status(self, pr_url: str) -> str:
        # Derive MR IID from URL (/merge_requests/<iid>)
        iid = self._iid_from_url(pr_url)
        if not iid:
            return "pending"

        result = subprocess.run(
            ["glab", "mr", "view", iid, "--output", "json"],
            capture_output=True,
            text=True,
            env=self._env(),
        )
        if result.returncode != 0:
            return "pending"

        try:
            data = json.loads(result.stdout)
        except (json.JSONDecodeError, ValueError):
            return "pending"

        pipeline_status = data.get("head_pipeline", {}).get("status", "pending")
        return _CI_STATE_MAP.get(pipeline_status, "pending")

    def get_ci_logs(self, pr_url: str) -> str:
        iid = self._iid_from_url(pr_url)
        if not iid:
            return ""

        # Get pipeline ID
        view_result = subprocess.run(
            ["glab", "mr", "view", iid, "--output", "json"],
            capture_output=True,
            text=True,
            env=self._env(),
        )
        if view_result.returncode != 0:
            return ""

        try:
            data = json.loads(view_result.stdout)
            pipeline_id = str(data.get("head_pipeline", {}).get("id", ""))
        except (json.JSONDecodeError, ValueError):
            return ""

        if not pipeline_id:
            return ""

        # Get failed job IDs
        jobs_result = subprocess.run(
            ["glab", "pipeline", "jobs", pipeline_id, "--output", "json"],
            capture_output=True,
            text=True,
            env=self._env(),
        )
        if jobs_result.returncode != 0:
            return ""

        try:
            jobs = json.loads(jobs_result.stdout)
            failed_ids = [str(j["id"]) for j in jobs if j.get("status") == "failed"]
        except (json.JSONDecodeError, ValueError, KeyError):
            return ""

        logs: list[str] = []
        for job_id in failed_ids[:3]:  # limit to first 3 failed jobs
            trace_result = subprocess.run(
                ["glab", "pipeline", "trace", job_id],
                capture_output=True,
                text=True,
                env=self._env(),
            )
            if trace_result.returncode == 0:
                logs.append(trace_result.stdout)

        combined = "\n---\n".join(logs)
        return combined[:_MAX_LOG_CHARS]

    def merge_pr(self, pr_url: str) -> bool:
        iid = self._iid_from_url(pr_url)
        if not iid:
            return False

        result = subprocess.run(
            ["glab", "mr", "merge", iid, "--squash", "--yes"],
            capture_output=True,
            text=True,
            env=self._env(),
        )
        return result.returncode == 0

    @staticmethod
    def _iid_from_url(url: str) -> str:
        parts = url.rstrip("/").split("/")
        idx = next((i for i, p in enumerate(parts) if p == "merge_requests"), -1)
        if idx != -1 and idx + 1 < len(parts):
            return parts[idx + 1]
        return ""
