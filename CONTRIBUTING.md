# Contributing to orch

## Releasing

Cutting a new release is a tag push:

```bash
# bump the version in pyproject.toml first
git commit -am "chore: bump version to 0.5.1"
git tag v0.5.1
git push origin main v0.5.1
```

GitHub Actions will:
1. Build the SPA + wheel
2. Verify the wheel contains the SPA
3. Create a GitHub Release with auto-generated notes
4. Attach the wheel

Users install via:

```bash
pipx install https://github.com/hectorcanaimero/orch/releases/download/v0.5.1/orchestrator-0.5.1-py3-none-any.whl
```

If CI fails, fix on main, push, then re-tag (delete the tag remotely first: `git push --delete origin v0.5.1`).
