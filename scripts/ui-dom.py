#!/usr/bin/env python3
"""ui-dom.py — dump a dashboard page's RENDERED DOM, and its console errors.

`chrome --headless --dump-dom` never returns on a page that opens a
long-lived connection: the `load` event never fires, and
`--virtual-time-budget` advances virtual time without closing a real socket.
The orch dashboard opens one (`/api/events/stream`) as soon as it is
authenticated, so `--dump-dom` hangs on exactly the view worth looking at.

This drives CDP instead: navigate, wait a fixed settle, then read
`document.documentElement.outerHTML` — no `load` required. Console errors and
uncaught exceptions go to stderr, which is the half that turns "the page is
blank" into "this TypeError, at this line".

    # once, in the background:
    chrome --headless=new --no-sandbox --disable-gpu \
           --user-data-dir=/tmp/some-fresh-profile \
           --remote-debugging-port=9333 about:blank

    python3 scripts/ui-dom.py 9333 http://127.0.0.1:7420/ 8 > dom.html

Stdlib only — a 40-line RFC 6455 client is cheaper than a dependency in a
repo whose Python side is frozen. See docs/UI-CHECKS.md for the full recipe.
"""
import base64, json, os, socket, struct, sys, time, urllib.request


def ws_connect(ws_url):
    _, rest = ws_url.split("://", 1)
    hostport, path = rest.split("/", 1)
    host, port = hostport.split(":")
    s = socket.create_connection((host, int(port)), timeout=20)
    key = base64.b64encode(os.urandom(16)).decode()
    s.sendall((
        f"GET /{path} HTTP/1.1\r\nHost: {hostport}\r\nUpgrade: websocket\r\n"
        f"Connection: Upgrade\r\nSec-WebSocket-Key: {key}\r\n"
        "Sec-WebSocket-Version: 13\r\n\r\n"
    ).encode())
    buf = b""
    while b"\r\n\r\n" not in buf:
        buf += s.recv(4096)
    return s


def ws_send(s, payload):
    data = json.dumps(payload).encode()
    mask = os.urandom(4)
    masked = bytes(b ^ mask[i % 4] for i, b in enumerate(data))
    n = len(data)
    if n < 126:
        header = struct.pack("!BB", 0x81, 0x80 | n)
    elif n < 1 << 16:
        header = struct.pack("!BBH", 0x81, 0x80 | 126, n)
    else:
        header = struct.pack("!BBQ", 0x81, 0x80 | 127, n)
    s.sendall(header + mask + masked)


def ws_recv(s):
    def read(n):
        out = b""
        while len(out) < n:
            chunk = s.recv(n - len(out))
            if not chunk:
                raise ConnectionError("websocket closed")
            out += chunk
        return out

    b0, b1 = read(2)
    n = b1 & 0x7F
    if n == 126:
        n = struct.unpack("!H", read(2))[0]
    elif n == 127:
        n = struct.unpack("!Q", read(8))[0]
    return json.loads(read(n).decode())


def main():
    port, url = sys.argv[1], sys.argv[2]
    settle = float(sys.argv[3]) if len(sys.argv) > 3 else 6.0

    targets = json.load(urllib.request.urlopen(f"http://127.0.0.1:{port}/json/list"))
    page = next(t for t in targets if t["type"] == "page")
    s = ws_connect(page["webSocketDebuggerUrl"])

    # Console and exceptions BEFORE navigating, or the ones thrown during the
    # first render are missed — which are the interesting ones.
    ws_send(s, {"id": 10, "method": "Runtime.enable"})
    ws_send(s, {"id": 11, "method": "Log.enable"})
    ws_send(s, {"id": 1, "method": "Page.navigate", "params": {"url": url}})

    notes = []
    s.settimeout(0.5)
    end = time.time() + settle
    while time.time() < end:
        try:
            msg = ws_recv(s)
        except (socket.timeout, TimeoutError):
            continue
        m = msg.get("method")
        if m == "Runtime.exceptionThrown":
            d = msg["params"]["exceptionDetails"]
            notes.append("EXCEPTION: " + (d.get("exception", {}).get("description") or d.get("text", "")))
        elif m == "Runtime.consoleAPICalled" and msg["params"]["type"] in ("error", "warning"):
            args = " ".join(str(a.get("value", a.get("description", ""))) for a in msg["params"]["args"])
            notes.append(msg["params"]["type"].upper() + ": " + args)
        elif m == "Log.entryAdded" and msg["params"]["entry"]["level"] in ("error", "warning"):
            e = msg["params"]["entry"]
            notes.append("LOG " + e["level"].upper() + ": " + e["text"] + " " + e.get("url", ""))
    s.settimeout(20)
    for n in notes:
        print("### " + n, file=sys.stderr)
    ws_send(s, {"id": 2, "method": "Runtime.evaluate", "params": {
        "expression": "document.documentElement.outerHTML", "returnByValue": True}})
    deadline = time.time() + 30
    while time.time() < deadline:
        msg = ws_recv(s)
        if msg.get("id") == 2:
            print(msg["result"]["result"]["value"])
            return
    sys.exit("no Runtime.evaluate reply within 30s")


main()
