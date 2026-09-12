# Checking the dashboard in a browser

The dashboard is the one part of orch whose bugs a green test suite cannot
see. Three real ones were found by opening a page, and none of them would have
shown up any other way:

| what looked fine | what the browser showed |
|---|---|
| `ProtectedRoute` had passing tests | an **operator** dashboard demanded a token that does not exist |
| every read route returned 200 | the landing page mounted **nothing** — a `TypeError` on a route the server answered with HTML |
| the portfolio page had its own tests | its "is this a portfolio?" check read a page of markup as data |

This page is how to look, and what to look at. It is headless — there is no
display on the machines this runs on — and every recipe here has been run.

---

## The quick check: a page that is not logged in

```bash
CHROME=~/.cache/ms-playwright/chromium-1243/chrome-linux64/chrome

"$CHROME" --headless=new --no-sandbox --disable-gpu \
  --user-data-dir=/tmp/orch-ui-check \
  --virtual-time-budget=8000 --dump-dom http://127.0.0.1:7420/
```

Good for the login wall and anything else that opens no long-lived connection.

Use a **fresh** `--user-data-dir` per run: `localStorage` survives between
them, and a token left over from an earlier check silently changes which
branch you are testing.

## Why that is not enough

`--dump-dom` prints after the `load` event. A page that holds a connection
open never fires it, and `--virtual-time-budget` advances virtual time without
closing a real socket — so the command hangs until `timeout` kills it
(`rc=124`).

The dashboard opens one as soon as it is authenticated
(`/api/events/stream`, `web/src/hooks/useEventStream.ts`). So `--dump-dom`
works on the view nobody is worried about and hangs on the one worth seeing.

## The real recipe: CDP

`scripts/ui-dom.py` navigates, waits a fixed settle, and reads
`document.documentElement.outerHTML` — no `load` needed. Stdlib only.

```bash
# 1. The dashboard, and WAIT for it (see the traps below).
orch dashboard --project-root /path/to/project --port 7420 &
until curl -sf -o /dev/null http://127.0.0.1:7420/api/whoami; do sleep 0.3; done

# 2. One browser, reused for every page you want to look at.
"$CHROME" --headless=new --no-sandbox --disable-gpu \
  --user-data-dir=/tmp/orch-ui-check --remote-debugging-port=9333 about:blank &
until curl -sf -o /dev/null http://127.0.0.1:9333/json/version; do sleep 0.3; done

# 3. The page. stdout is the DOM; stderr is the console.
python3 scripts/ui-dom.py 9333 "http://127.0.0.1:7420/" 8 > dom.html 2> console.txt

# 4. Always.
pkill -f chrome-linux64/chrome
```

### stderr is the half that matters

`ui-dom.py` subscribes to `Runtime.exceptionThrown`, `Runtime.consoleAPICalled`
and `Log.entryAdded` **before** navigating, because the interesting throws
happen during the first render. It is the difference between a symptom and a
diagnosis:

```
### ERROR: TypeError: Cannot read properties of undefined (reading 'done')
    at PT (http://127.0.0.1:7420/assets/index-gib8RKXV.js:368:62905)
```

Without it the finding is "the page is blank", which nobody can act on. With
it, `reading 'done'` pointed straight at `summary.done` on the landing page,
and from there to a route answering HTML with a 200.

A 404 in that log is not necessarily a bug: the SPA asks for `/api/portfolio`
on every dashboard and handles the 404. Read what the page did, not only what
it logged.

---

## Traps, all of them hit while writing this page

**A page that failed to load still produces a DOM.** Navigate before the
server is listening and you get Chrome's own error page — hundreds of KB of
markup that greps like a real render. Check the title:

```bash
rg -o '<title>[^<]*</title>' dom.html     # "127.0.0.1" means it never loaded
```

That is why step 1 waits on `/api/whoami` instead of sleeping.

**`pgrep` matches its own shell.** `pgrep -f chrome-linux64/chrome` counts the
command you just typed, so a clean machine reports one survivor. Filter it —
`ps aux | rg … | rg -v 'rg |bash -c'` — before concluding a browser is still
running.

**Close the browser.** Headless Chrome does not exit with the shell that
started it. `pkill -f chrome-linux64/chrome`, then verify with the caveat
above.

**One browser, many pages.** Step 2 is the slow part; `ui-dom.py` takes the
debug port as an argument and can be run repeatedly against the same browser.

---

## What to look at for a dashboard change

Access and profile changes touch four states, and three are easy to forget.
All four were needed to review the `ProtectedRoute` fix:

| profile | token | expected |
|---|---|---|
| operator | none | the app, **no** login form — an operator dashboard asks for nothing |
| stakeholder | none | the login form |
| stakeholder | valid | the app |
| stakeholder | wrong | the login form |

A stakeholder project is two lines on top of any project's config:

```yaml
dashboard:
  profile: stakeholder
  token: test-token-stakeholder
```

and the token reaches the browser through the documented flow —
`http://127.0.0.1:7420/?token=test-token-stakeholder`, which
`adoptTokenFromQuery()` moves into `localStorage` and scrubs from the URL.

Then compare **bytes and the mount point**, not only the status code. A page
that throws during render still returns 200 and still serves the shell:

```bash
wc -c dom.html                             # ~600 bytes is an empty shell
rg -o '<div id="root">.{0,40}' dom.html    # `<div id="root"></div>` = nothing mounted
```

That pair is what turned three PRs into a measurable progression rather than
three assertions — 597 bytes and an empty root, then 9471 with the SPA's own
error alert, then 21692 with the page it was supposed to show.

---

## Related

- [`DASHBOARD-PROFILES.md`](DASHBOARD-PROFILES.md) — profiles, tokens, the
  portfolio view and the 404 rule.
- [`CI-REVIEW.md`](CI-REVIEW.md) — the automated review, which does not open a
  browser and never will.
