from orchestrator.vcs.github import GitHubProvider
from orchestrator.vcs.gitlab import GitLabProvider
from orchestrator.vcs.protocol import VcsProvider

__all__ = ["VcsProvider", "GitHubProvider", "GitLabProvider", "get_vcs_provider"]


def get_vcs_provider(cfg: dict) -> VcsProvider:
    vcs_cfg = cfg.get("vcs", {})
    provider = vcs_cfg.get("provider", "github")
    host = vcs_cfg.get("host", "github.com")
    if provider == "gitlab":
        return GitLabProvider(host=host)
    return GitHubProvider()
