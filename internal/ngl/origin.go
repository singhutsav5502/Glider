package ngl

import (
	"net"
	"net/http"
)

// HostWithoutPort strips a trailing ":port" from r.Host before an
// adapter's own Matches compares it against a bare hostname suffix/pattern.
//
// Real, live-confirmed bug (2026-07-28): cursor-agent's and agy's own
// OriginAdapters both compared r.Host directly against a bare hostname
// suffix (e.g. ".cursor.sh") — correct for the CONNECT-based/gateway path,
// where Go's http.Client conventionally omits a :443 port for the default
// HTTPS port, but wrong for transparent interception, where the client's
// own HTTP/2 :authority pseudo-header (which net/http maps to r.Host) is
// NOT guaranteed to omit it — cursor-agent's real client includes it
// explicitly (confirmed live: r.Host was literally
// "agentn.global.api5.cursor.sh:443", not the bare hostname), so
// Matches() silently never fired even though the request genuinely
// reached Glider — traffic fell through to origin passthrough instead of
// ever reaching ExtractUserInstruction, no error, no log line pointing at
// why, just an unclaimed request.
func HostWithoutPort(r *http.Request) string {
	if h, _, err := net.SplitHostPort(r.Host); err == nil {
		return h
	}
	return r.Host // already bare (SplitHostPort errors when there's no ":port" to split)
}

// OriginAdapter is the interception-boundary counterpart of the existing
// outgoing-direction ParseXTurn family (which parses a vendor's own
// stdout/headless stream, not its network traffic). Where ParseXTurn
// answers "what did this vendor's own CLI just print," OriginAdapter
// answers "is this HTTP request a vendor CLI's own live traffic to its own
// backend, and if so, what did the human actually type, and how does a
// reply need to be shaped for that CLI to accept it as real."
//
// This exists because of a real, live bug (2026-07-27): internal/mitm's
// delegate handler hardcoded `r.URL.Path != "/v1/messages"` as its entry
// gate, which only happens to be true when Claude Code is the front CLI
// (its own traffic genuinely is Anthropic Messages API-shaped). cursor-agent
// and agy's own real traffic to their own backends is never that shape —
// Connect-RPC for cursor-agent, a Gemini-style streamGenerateContent REST
// call for agy — so typing a delegate flag directly into either of those
// CLIs silently did nothing; the hardcoded gate rejected the request before
// any flag-parsing logic ever ran. Fixed by replacing the hardcoded path
// check with per-vendor Matches() dispatch through a registry, so no vendor
// name is ever compared literally in the shared interception code path
// (internal/mitm/delegate_handler.go) — see that file's own doc comment for
// the call site.
//
// Registration is deliberately per-adapter (RegisterOriginAdapter, called
// from each adapter's own file's init()), not a hardcoded list here — same
// reasoning as scaffoldStrippers in ngl.go: adding a vendor is "one more
// adapter file," never a diff to shared dispatch code.
type OriginAdapter interface {
	// Vendor is this adapter's vendor name, matching vendors.Vendor.Name
	// (ngl intentionally does not import internal/vendors — that would be
	// a dependency inversion; the string is the contract, not a shared type).
	Vendor() string

	// Matches reports whether r is this vendor's own request to its own
	// backend, checked structurally (host/path shape) before any body
	// read — must be side-effect-free and must not consume r.Body.
	Matches(r *http.Request) bool

	// ExtractUserInstruction parses this vendor's own request body and
	// returns the newest human-authored instruction text, with this
	// vendor's own auto-injected scaffolding already stripped, plus the
	// model name (for echoing back in WriteReply) and whether the
	// request is a streaming request.
	//
	// ok=false (with a nil error) is a distinct, deliberate outcome from
	// err!=nil: it means the body parsed structurally fine but this
	// adapter has no confirmed, verified way to separate genuine human
	// text from this vendor's own scaffolding for what it found (e.g. an
	// unconfirmed/opaque wire shape). Callers MUST treat ok=false
	// exactly like a parse failure — fall through to real origin
	// passthrough — never fall back to a naive raw-body substring scan.
	// That exact shortcut, tried once for Claude before NGL existed, is
	// what caused the original live bug ngl.go's package doc describes
	// (auto-injected scaffolding accidentally containing the flag
	// substring, hijacking real conversation turns). Guessing at an
	// unverified schema carries the identical risk, just for a different
	// vendor, so it gets the identical refusal.
	ExtractUserInstruction(body []byte) (text, model string, stream, ok bool, err error)

	// WriteReply renders a synthetic reply in this vendor's own expected
	// wire shape, writing directly to w in place of forwarding to the
	// real origin.
	//
	// header, if non-empty, is meaningful text the caller wants on the
	// wire before replyText is known — e.g. "Delegated to agy...".
	// Implementations must write it (or otherwise get real bytes flowing)
	// as early as possible, before blocking on replyText. This exists
	// because of a real, live-confirmed bug (2026-07-29): cursor-agent's
	// own HTTP/2 client abandoned a delegate reply stream
	// (`http2: stream closed`) that received zero bytes for the whole
	// duration of a slow delegate call — a headless run of another
	// vendor's CLI can take far longer than a normal completion, and the
	// old signature only ever handed WriteReply an already-fully-resolved
	// string, so there was no way to get anything on the wire early. See
	// cursorOriginAdapter.WriteReply and
	// cursorrpc.WriteDelegateReplyWithKeepAlive for the vendor that
	// actually needs this; the header also doubles as telling the human
	// what was delegated to whom, since a synthesized reply otherwise
	// gives no indication a different CLI produced it (DelegateHandler
	// builds header).
	//
	// replyText delivers the final reply text once known — exactly one
	// value, then closed. Implementations must not assume it is already
	// closed by the time WriteReply is called; a slow delegate call fills
	// it asynchronously from a separate goroutine.
	WriteReply(w http.ResponseWriter, model string, stream bool, header string, replyText <-chan string) error
}

var originAdapters []OriginAdapter

// RegisterOriginAdapter adds a to the registry consulted by
// ResolveOriginAdapter. Called from each adapter's own init(); never call
// this from shared dispatch code.
func RegisterOriginAdapter(a OriginAdapter) {
	originAdapters = append(originAdapters, a)
}

// ResolveOriginAdapter returns the first registered adapter whose Matches
// reports true for r, or nil if none recognize this request — a request
// that no adapter recognizes is not this codebase's own traffic to a known
// vendor backend, and callers must treat nil exactly like "not our
// concern," the same safe default vendors.ResolveOriginVendorName's ""
// result already establishes for scaffold-stripping.
func ResolveOriginAdapter(r *http.Request) OriginAdapter {
	for _, a := range originAdapters {
		if a.Matches(r) {
			return a
		}
	}
	return nil
}
