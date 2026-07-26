# Transparent redirector — interface design and mechanics

How Glider intercepts a CLI's HTTPS traffic without that CLI cooperating in any way — no proxy setting, no env var, no relaunch — and why the interface is split the way it is. Windows implementation shipped (WinDivert); Linux/macOS designed but not built. Code: `internal/mitm/redirector_windows.go`, `internal/mitm/redirector_other.go` (stub).

## The one-paragraph mental model

Every cooperative way Glider gets in front of traffic — a proxy setting, a base-URL override — works because the client **agrees to ask Glider first**. That's a favor, and it has a real limit: it only takes effect for a process that hasn't started yet, so it can never reach a CLI session someone already has open. A transparent redirector doesn't ask for the favor. It sits at the one layer no application can opt out of — the OS's own decision about where a port-443 packet actually goes — and answers that question itself, for every process already running or not, the instant Glider turns on. The interface is deliberately thin: its entire job is "get the bytes to Glider instead of the real host." What happens to those bytes once they arrive — host allowlist match, forge a cert, decrypt, fulfill locally or pass through — already exists in `internal/mitm` and doesn't change based on which OS-level trick got the bytes there.

## What this interface solves, and what it deliberately doesn't

**Solves:** landing an outbound connection a CLI opened to an allowlisted vendor host on a socket Glider controls, without the CLI's cooperation, regardless of when that process started.

**Explicitly not this interface's job** (already solved elsewhere, would be duplicated code otherwise):

| Already solved by | Not the redirector's job |
|---|---|
| `internal/mitm/hosts.go` (`matchHostPattern`) | Deciding which hostnames are worth decrypting |
| `internal/mitm/proxy.go` (`mitmSession`) | Forging a leaf cert, completing TLS, running the harness pipeline |
| `internal/mitm/proxy.go` (`blindTunnel`) | Passing non-matched traffic straight through |
| `internal/router`, `internal/orchestrator` | Local vs. cloud vs. delegate |

The redirector's whole contract: hand `mitmSession`/`blindTunnel` a normal, already-accepted `net.Conn`, plus the answer to "what host was this connection actually trying to reach" — the one piece of information a transparent path doesn't get for free the way a CONNECT-based path does (a `CONNECT host:443` request line states it outright).

## The interface

```go
// TransparentRedirector owns exactly one OS-specific fact: how to make outbound
// connections to MatchPorts land on ListenPort instead of their real destination.
// It knows nothing about TLS, hosts, or routing — that's what makes it swappable
// per OS without touching anything downstream.
type TransparentRedirector interface {
	Start(ctx context.Context, cfg RedirectConfig) error
	// Stop must fully undo whatever Start did — no leftover kernel state, no
	// orphaned driver, no dangling firewall rule.
	Stop() error
}

type RedirectConfig struct {
	ListenPort int   // Glider's local transparent-ingress port (e.g. 8083)
	MatchPorts []int // destination ports to intercept — []int{443} today
}

// OriginResolver answers the question a transparent path can't get for free:
// given a connection that already landed on Glider's listener, what host/port
// was the client actually trying to reach?
type OriginResolver interface {
	ResolveOriginalDestination(conn net.Conn) (host string, port int, err error)
}
```

Split into two interfaces rather than one call because their frequency differs (`Start`/`Stop` once at startup/shutdown; `ResolveOriginalDestination` on every accepted connection) and their per-OS cost is wildly asymmetric — Linux's resolver is nearly free (one syscall), Windows' requires the redirector to keep its own bookkeeping (below). `RedirectConfig` stays host-unaware on purpose: host-level policy already lives in `matchHostPattern`, once; teaching the redirector about hosts would mean two places decide "is this host interesting."

## Steady state, once running

```mermaid
sequenceDiagram
    participant App as CLI process (already running, unaware)
    participant OS as OS TCP/IP stack
    participant Redir as TransparentRedirector (OS-specific)
    participant Glider as Glider transparent listener
    participant Engine as internal/mitm (matchHostPattern → mitmSession / blindTunnel)
    participant Origin as Real vendor host

    App->>OS: connect(vendor_ip:443)
    OS->>Redir: outbound SYN, dst port 443
    Redir->>Redir: rewrite destination -> 127.0.0.1:ListenPort (remember original dest)
    Redir->>Glider: (re-injected) SYN now targets Glider
    Glider-->>App: SYN-ACK (App's OS completes the handshake normally)
    App->>Glider: TLS ClientHello (SNI = real vendor host)
    Glider->>Redir: OriginResolver.ResolveOriginalDestination(conn)
    Redir-->>Glider: (host, port) recovered
    Glider->>Engine: matchHostPattern(host)?
    alt allowlisted vendor host
        Engine->>Engine: mitmSession — forge leaf, decrypt, fulfill locally
        Engine->>Origin: or origin passthrough (real creds replayed)
    else not allowlisted
        Engine->>Origin: blindTunnel (undecrypted)
    end
```

The load-bearing detail: from `App`'s perspective nothing here happened — it called `connect()` once, got a handshake, sent its ClientHello. It didn't need to be launched by Glider or read an env var Glider set.

## Windows (WinDivert) — shipped

```
Original IPv4 packet (SYN, client -> vendor_ip:443):
  WinDivertRecv() delivers it to Glider's redirector before the OS sends it
                        │
                        ▼
  rewrite dst -> 127.0.0.1:ListenPort
  record {src_ip:src_port -> vendor_ip:443} in an in-process flow table
  WinDivertHelperCalcChecksums() (IP+TCP checksums cover the fields just changed)
                        │
                        ▼
  WinDivertSend() reinjects — client's own TCP stack thinks it opened a
  connection to vendor_ip:443; it never sees the rewrite.
```

Windows has no OS-native "original destination" lookup, unlike Linux — the flow table above is the redirector's own responsibility, keyed by the client's source `ip:port` (constant across the rewrite, and what `conn.RemoteAddr()` sees on the accepted socket). `ResolveOriginalDestination` on Windows is a map lookup against that table. Open item: eviction policy (TTL-on-insert + removal on `conn.Close()` is the obvious shape, not yet built — a long-lived entry needs to survive as long as the connection, but an ever-growing table is a leak).

Live-verified: a sniff-mode capture (`WINDIVERT_FLAG_SNIFF | WINDIVERT_FLAG_RECV_ONLY`, no packets touched) caught 712 real outbound TLS segments system-wide in 25s, including an already-running Claude Code session's own traffic — proving visibility into pre-existing connections with zero app cooperation. `Stop()` was verified to leave no trace (`sc query` confirms the driver service is fully gone after `sc delete`).

## Linux (iptables/nftables + `SO_ORIGINAL_DST`) — designed, not proven

```
iptables -t nat -A OUTPUT -p tcp --dport 443 -j REDIRECT --to-port <ListenPort>
```

`REDIRECT` is a first-class netfilter target — the kernel rewrites the destination before the packet leaves the machine, no userspace rewrite-and-reinject round trip. `OriginResolver` becomes `getsockopt(fd, SOL_IP, SO_ORIGINAL_DST)` on the accepted socket — the kernel hands back the pre-NAT destination directly, no flow table or eviction policy needed. This is the same mechanism mitmproxy's own transparent mode and most corporate MITM appliances run in production.

Not independently proven yet: an attempt inside WSL2 Ubuntu 24.04 installed `iptables` and accepted the rule with no error, but a `curl` from an uncooperating process failed to connect at all. Root cause: the stock Microsoft-built WSL2 kernel ships without netfilter's NAT modules compiled in (`lsmod`/`/proc/net/ip_tables_names` both empty) — a documented WSL2 packaging limitation, not evidence against the technique. Needs a real Linux host or cloud VM to actually verify.

## macOS — documented only, no hardware tested

Apple's Network Extension framework (`NETransparentProxyProvider`/`NEFilterDataProvider`) is the supported, notarizable API for this; `pfctl` NAT `rdr` rules are the lower-level fallback (what mitmproxy/Charles already use there). No live evidence either way — nothing implemented.

## Side-by-side

| | `Start`/`Stop` | `ResolveOriginalDestination` | Confidence |
|---|---|---|---|
| **Windows** | WinDivert: rewrite + checksum + reinject; `Stop` closes the handle and verifies the driver service is gone | In-process flow table keyed by client `ip:port` | **Proven live** |
| **Linux** | `iptables`/`nftables` `REDIRECT`; `Stop` flushes the rule | `SO_ORIGINAL_DST` — free from the kernel | Designed, industry-standard elsewhere, blocked from live proof by WSL2's stripped kernel |
| **macOS** | Network Extension framework, or `pfctl` `rdr` | Provided by the framework, or BSD original-destination lookup | Documented only |

Selected by Go build tag (`redirector_windows.go` / a future `redirector_linux.go`), not runtime dispatch — a platform without an implementation simply doesn't compile that code in.

## Does the CLI actually trust Glider's forged cert?

Routing and TLS trust are independent failure modes — WinDivert can redirect a connection perfectly and the handshake can still fail if the client doesn't trust Glider's CA. Tested for Claude Code: minted a real leaf (signed by Glider's own CA, already in the OS Trusted Root store from Cursor setup), pointed `ANTHROPIC_BASE_URL` at a local test server presenting it, ran `claude -p` from a shell with `NODE_EXTRA_CA_CERTS` explicitly unset. The handshake completed and the real request landed.

**Claude Code's bundled Node runtime trusts the OS certificate store directly** — it does not need the `NODE_EXTRA_CA_CERTS` cooperation step Cursor's Electron/Node stack does (see `docs/MITM_NETWORK.md`'s CA setup, which requires exactly that env var for Cursor). This matters because CA trust for Claude Code is then a one-time, system-level action (install into Trusted Root once), not a per-process env var — so it doesn't inherit the "only affects processes launched after Glider starts" problem that rules out `ANTHROPIC_BASE_URL` as a transport in the first place. Combined with the redirector, both halves of interception (routing the bytes, and having them trusted once decrypted) are cooperation-free and retroactive for Claude Code specifically. Cursor's TLS-trust half is still env-var-shaped, so it keeps the "already-open session" caveat on the trust side that Claude Code doesn't have.

Not tested: `cursor-agent`'s and `agy`'s TLS-trust behavior specifically (as opposed to Cursor IDE's, which the MITM docs already cover).

## Open questions

- **IPv6** — the Windows packet parser only handles IPv4 headers today; an IPv6-resolved vendor host needs a second parser, not designed yet.
- **Encrypted ClientHello (ECH)** — everything above assumes the transparent listener can read a plaintext SNI. Not observed for the three vendors so far; if it appears, `OriginResolver` stops being "the Windows answer, redundant on Linux" and becomes the only source of truth for host on every OS.
- **Windows flow-table eviction** — noted above, not yet built.
- **System-wide vs. process-scoped matching** — everything above redirects system-wide (every process's port-443 traffic). A narrower alternative (a connect-hook DLL injected into one launched process, à la Proxifier/proxychains4) is the fallback if system-wide interception ever proves too broad for a given deployment — not adopted, kept in the design space.

## Integration point

- `internal/mitm/redirector_windows.go` (`//go:build windows`) implements both interfaces via WinDivert; `redirector_other.go` is the no-op stub on other platforms.
- A transparent-ingress path alongside `handleCONNECT` peeks the TLS ClientHello for SNI (falling back to `OriginResolver` if absent), then dispatches into the same `matchHostPattern` → `mitmSession`/`blindTunnel` sequence `handleCONNECT` already uses — no changes to either of those functions.
- Lifecycle: gated by `mitm.transparent` in config, started/stopped alongside Glider's other listeners in the server's own run context.
