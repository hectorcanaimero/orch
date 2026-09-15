# testdata

`releases.json` is a real response from the GitHub releases API, captured on
2026-09-15 with gh 2.100.0. Only the fields `internal/update` reads are kept:

    gh api 'repos/hectorcanaimero/orch/releases?per_page=6' \
      --jq '[.[] | {tag_name, html_url, draft, prerelease, body, assets: [.assets[] | {name}]}]'

It covers the cases the parser has to handle: a Go release with curated notes
(v0.13.0), Go releases whose notes are empty (v0.12.x), and Python-line releases
that ship a wheel instead of an `orch_<tag>_<os>_<arch>.tar.gz`, which must never
be offered as an update. Do not edit it. Capture a new file instead.
