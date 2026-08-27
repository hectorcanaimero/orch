from typing import Protocol


class VcsProvider(Protocol):
    def create_pr(
        self,
        task_id: str,
        title: str,
        body: str,
        head: str,
        base: str,
    ) -> str | None:
        """Open a PR/MR. Returns the PR URL or None on failure."""

    def get_ci_status(self, pr_url: str) -> str:
        """
        Returns one of: 'success' | 'failure' | 'pending'.
        'pending' covers in_progress, queued, waiting states.
        """

    def get_ci_logs(self, pr_url: str) -> str:
        """Return failed CI job logs as plain text (truncated to ~8000 chars)."""

    def merge_pr(self, pr_url: str) -> bool:
        """Squash-merge the PR/MR. Returns True on success."""
