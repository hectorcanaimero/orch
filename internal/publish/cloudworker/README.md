# internal/publish/cloudworker

`worker.js` is the orch-cloud Worker that `orch cloud setup` deploys. It is a
build artefact, never edited here.

- **Source:** <https://github.com/hectorcanaimero/orch-cloud>, commit `3bb572c`
  ("fix: the Worker would not start — a named export workerd read as an
  entrypoint").
- **Contract:** API 1 (`docs/CLOUD.md`), the version `internal/publish`'s client
  speaks.
- **Size / sha256:** 14,254 bytes,
  `35248672dd1317ee4d51153ff88c78febb5f7fe35b28553f9190025f7597211f`.

## Regenerating

From a checkout of orch-cloud at the commit you want to ship:

```bash
npm ci
npx wrangler@4.131.1 deploy --dry-run --outdir dist
cp dist/index.js <orch>/internal/publish/cloudworker/worker.js
```

Do not copy `index.js.map`. Update the commit, size and sha256 above in the
same PR, and bump `WranglerVersion` in `cloudworker.go` only together with a
look at the deploy output `orch cloud setup` parses (`internal/publish/cloudsetup.go`).

`TestScriptCarriesTheContractRoutes` fails on an empty file or a bundle that
no longer serves the contract's routes; it does not prove the bundle matches
the commit above — that is what the sha256 here is for.
