# Elicitation-scoped keepalive: ping the client only while a dialog is open

**Date:** 2026-10-20 (revised 2026-10-20, alpha.8)
**Status:** alpha.8 implementation
**Owner:** fork

## Problem (final root cause, two faults stacked)

Pod forensics disproved the original "nightly cert-rotation restart wipes sessions"
hypothesis: `restartCount: 0` over 10 days on alpha.5. Two server-side defects were actually
responsible for the chronic **404 session-not-found**, and the alpha.6/alpha.7 attempts to fix
them introduced a third:

1. **alpha.6 (SDK built-in keepalive):** `MCP_KEEP_ALIVE=30s` mapped straight onto
   `ServerOptions.KeepAlive`. go-sdk v1.7.0 `startKeepalive` pings **every** session
   unconditionally, waits `interval/2` for a pong, tolerates **one** failure, then calls
   `session.Close()`. Ping delivery prefers an in-flight request's response stream, but a
   session with nothing in flight falls back to the **standalone GET SSE stream** — which
   POST-only clients (dsh, most HTTP MCP clients) never open. Those pings could never be
   delivered, so every non-ponging session was killed ~45 s after creation. → chronic 404s
   for every long-lived agent.
2. **alpha.7 (alpha.7-band-aid, "swallow the ping" middleware):** to keep the benefit for
   elicitation dialogs without the kill, a sending middleware intercepted the SDK's keepalive
   ping and returned a locally-fabricated success result when no dialog was open. **This
   panics:** SDK `handleSend[R]` type-asserts the result with `res.(R)` where `R` is the
   method's **concrete, unexported** type (`*emptyResult` for ping). No value the middleware
   can construct satisfies an unexported type assertion, so the first swallowed ping crashed
   the whole server process (exit 2) → all sessions wiped → clients re-init → ~30 s later
   crash again. This was the alpha.7 crash-loop (`restartCount` 3 within 8 minutes) reported
   as "dsh 一直 404".

## Design (alpha.8)

**Stop intercepting the SDK's ping entirely. The server owns the pinging loop directly, but
only in the window where a response stream actually needs warming.**

- `cmd/serve.go` leaves `ServerOptions.KeepAlive` at its zero default — the SDK spawns no
  per-session keepalive goroutine, so there is nothing to swallow and nothing to panic.
  `--keep-alive`/`MCP_KEEP_ALIVE` stay registered (old envs from alpha.6/alpha.7 images
  parse) but are **ignored**, with a help text explaining why.
- `pkg/keepalivegate` installs a **sending** `mcp.Middleware` that watches for outbound
  server's `elicitation/create` calls (which is exactly how `confirm.Gate` shows the
  confirmation dialog). For each session:
  - first open dialog → arm a watchdog goroutine that calls the SDK's **exported**
    `(*ServerSession).Ping` every 30 s. The SDK writes a genuine ping onto the hanging tool
    call's POST-response SSE stream — the one stream that exists at that moment and the one
    the ingress would otherwise idle-kill after 360 s.
  - last dialog closes → watchdog exits; the session goes back to **complete server→client
    silence**, which is the strongest possible guarantee that clients who cannot pong are
    never disconnected for missing one. No synthetic results are ever fabricated; the gate
    never fabricates an `mcp.Result`.
  - successive dialogs on one session are throttled (`minSpacing = 2 s`) so rapid
    open/close/open (token re-validation, multi-step confirms) can never run two watchdogs
    over the same session.
- Liveness on a dead client: `Ping` error ⇒ watchdog exits silently. The watchdog **never**
  closes the session — session teardown belongs to the transport, not to the gate.

### Behavior matrix

| Client / phase | pings on the wire | outcome |
|---|---|---|
| plain POST client (dsh), normal work | none | session lives indefinitely; 404s gone |
| any client, confirmation dialog open | one real ping every 30 s on the tool call's response stream | stream survives ingress idle timeout (original #157 benefit kept), dialog still pops |
| dialog answered / cancelled / errored | stops at last close | silence resumes |
| client vanishes mid-dialog | watchdog's `Ping` errors → watchdog exits | session is left to the transport's own teardown; no zombie kill |

### Why this fixes alpha.7's panic class permanently

The alpha.7 test suite passed because every test exercise stopped **before** the real
`handleSend` concrete assertion. The new test (`pkg/keepalivegate/keepalivegate_test.go`)
spins a real in-memory server+client through the full SDK with the gate installed, counts
pings that actually hit the wire, and asserts both invariants: **pings while the dialog is
open, silence after it closes** — exercising every line of the production send path the
alpha.7 panics lived in. A panic anywhere in that path fails the test loudly instead of
shipping.

## What this does NOT change

- `confirm.Gate` (token issue/consume/TTL/nonce) untouched — transport-layer change only.
- The ingress `rancher-mcp-ingress` keeps its 360 s idle timeout; we just never rely on
  surviving it except while a dialog is open.
- No image rebuild or rollout is triggered by the code change itself.

## Follow-ups (not in scope)

- `confirm.Gate` HMAC key is per-process and `TokenTTL` is 10 min; any pod restart
  invalidates outstanding confirmation tokens ("bad signature"). Pre-existing, independent
  fork robustness item.
- Upstream: worth an issue against `github.com/modelcontextprotocol/go-sdk` that
  `startKeepalive`'s one-missed-pong `session.Close()` is unsafe for POST-only clients —
  pings undeliverable on the standalone SSE stream must not be lethal. (Today we route around
  the SDK keepalive entirely.)
- Clients without 404→re-init (dsh, older Claude Code / TS SDK stacks) still benefit from
  fixing re-init client-side as defence-in-depth against any *other* session loss, but the
  server must stop killing live sessions first.
