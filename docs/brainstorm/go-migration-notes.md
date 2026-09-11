# Go migration notes

Bugs found in the Python `orch` while the Go rewrite (see CLAUDE.md → "Migración a Go (en curso)") is in progress get logged here instead of fixed with a new feature or a large refactor in Python. Small, targeted fixes are still fine — this file is for anything that would otherwise tempt scope creep in the version being replaced.

## Open notes

- Stale `jinja2` mentions: `orchestrator/orch.py`'s dashboard-missing-deps hint
  still tells the user to install `jinja2 >= 3.1`, and
  `orchestrator/dashboard/__init__.py`'s module docstring still calls out
  `jinja2` as a "heavy import". Neither is true anymore — the dashboard is
  the React SPA, `jinja2` isn't in `pyproject.toml` deps, and no templates
  ship in the wheel. Low priority; drop both mentions whenever that file is
  touched for something else, or wait for the Go rewrite (`internal/dashboard/`)
  to make it moot.
