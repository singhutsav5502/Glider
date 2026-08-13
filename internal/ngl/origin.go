package ngl

import (
	"net"
	"net/http"
)

// HostWithoutPort removes a trailing ":port" from r.Host. Then the Matches
// function of an adapter can compare r.Host with a host name or a pattern
// that has no port.
//
// A live test confirmed this defect on 2026-07-28. The OriginAdapters of
// cursor-agent and of agy both compared r.Host directly with a host name that
// has no port, for example ".cursor.sh". That comparison is correct for the
// path with CONNECT and for the gateway path. The http.Client of Go usually
// gives no ":443" port for the default HTTPS port.
//
// But the comparison is incorrect for transparent interception. There, the
// :authority pseudo-header of HTTP/2 comes from the client, and net/http puts
// it in r.Host. That header does not always omit the port. The true client of
// cursor-agent puts the port in it. A live test confirmed that r.Host was
// exactly "agentn.global.api5.cursor.sh:443", and not the host name alone.
//
// Therefore Matches() never gave a positive result, and it gave no message,
// although the request truly arrived at Glider. The traffic continued to the
// passthrough to the origin, and it never arrived at ExtractUserInstruction.
// There was no error and no log line that gave the cause. The request only
// had no owner.
func HostWithoutPort(r *http.Request) string {
	if h, _, err := net.SplitHostPort(r.Host); err == nil {
		return h
	}
	return r.Host // already bare (SplitHostPort errors when there's no ":port" to split)
}

// OriginAdapter operates at the boundary of interception. The ParseXTurn
// family operates in the other direction, and it reads the output of a vendor
// with no console, and not its network traffic.
//
// ParseXTurn answers one question: "what did the CLI of this vendor write?"
// OriginAdapter answers three different questions. Is this HTTP request the
// live traffic of a vendor CLI to its own backend? If it is, what did the
// person truly type? And what shape must a reply have, thus that CLI accepts
// it as true?
//
// This exists because of a true defect that a live test found on 2026-07-27.
// The delegate handler in internal/mitm had a fixed test at its entry,
// `r.URL.Path != "/v1/messages"`. That test is correct only when Claude Code
// is the front CLI, because its traffic truly has the shape of the Messages
// API of Anthropic.
//
// The true traffic of cursor-agent and of agy to their own backends never has
// that shape. cursor-agent uses Connect-RPC. agy uses a REST call,
// streamGenerateContent, with the shape of Gemini. Therefore a delegate flag
// that a person typed in one of those two CLIs did nothing, and it gave no
// message. The fixed test refused the request before any code read the flag.
//
// The correction replaces that fixed test with a dispatch to Matches() for
// each vendor, through a registry. Thus the shared code for interception
// never compares the name of a vendor as text. Refer to
// internal/mitm/delegate_handler.go for the call site.
//
// Each adapter registers itself, in the init() of its own file, with
// RegisterOriginAdapter. There is no fixed list here, and this is on purpose.
// The cause is the same as for scaffoldStrippers in ngl.go. To add a vendor
// is "one more adapter file". It is never a change to the shared dispatch
// code.
type OriginAdapter interface {
	// Vendor is the name of the vendor of this adapter, and it agrees with
	// vendors.Vendor.Name. The ngl package does not import internal/vendors, and
	// this is on purpose, because that import would put the dependency in the
	// wrong direction. The text is the contract, and a shared type is not.
	Vendor() string

	// Matches says if r is the request of this vendor to its own backend. It
	// examines the structure, which is the shape of the host and the path, before
	// any code reads the body. It must change nothing, and it must not read
	// r.Body.
	Matches(r *http.Request) bool

	// ReadRequestBody reads the request body of this vendor by the method that
	// is safe for its wire protocol.
	//
	// Most vendors use a simple request and response, and io.ReadAll(r.Body) is
	// sufficient. But to do that for each vendor caused a true defect, and a
	// live test confirmed it on 2026-07-29.
	//
	// agent.v1.AgentService/Run of cursor-agent is a true RPC that streams in
	// two directions. Its client keeps the send side of the request stream open
	// for approximately 30 seconds, with envelopes that keep the connection
	// alive. It does this even for one turn with no console. Therefore
	// io.ReadAll(r.Body) blocked the full handler for that period, and it gave
	// no message, before the handler could write one byte back. At the end of
	// that period the client had already stopped and reset the stream.
	//
	// cursorOriginAdapter reads only the first Connect envelope. Refer to the
	// comment on cursorrpc.ReadFirstEnvelope for the full record of the
	// incident. claude and agy keep the plain io.ReadAll, because their traffic
	// is truly a simple request and response.
	ReadRequestBody(r *http.Request) ([]byte, error)

	// ExtractUserInstruction reads the request body of this vendor. It returns
	// three items:
	//
	//   1. The newest instruction that a person wrote, with the content that this
	//      vendor adds automatically already removed.
	//   2. The name of the model, thus WriteReply can send it back.
	//   3. A value that says if the request is a stream.
	//
	// A result of ok=false with a nil error is a different result from an error,
	// and it is on purpose.
	//
	// ok=false means that the structure of the body is correct. But this adapter
	// has no confirmed method to divide the true text of a person from the
	// content of the vendor, for what it found. An example is a shape on the
	// wire that no person confirmed.
	//
	// A caller MUST treat ok=false exactly as a failure of the parser. It must
	// continue to the passthrough to the true origin. It must never use a simple
	// search for a substring in the raw body.
	//
	// A person used that exact method one time for Claude, before NGL existed.
	// It caused the first live defect that the package comment in ngl.go
	// describes. The content that Claude adds automatically held the text of the
	// flag. Therefore it took control of true turns of the conversation.
	//
	// To estimate a schema that no person verified has the same risk, for a
	// different vendor. Therefore it gets the same refusal.
	ExtractUserInstruction(body []byte) (text, model string, stream, ok bool, err error)

	// WriteReply makes a reply in the wire shape that this vendor expects. It
	// writes directly to w, and it does not send the request to the true origin.
	//
	// If header is not empty, it holds text of value that the caller needs on the
	// wire before it knows replyText. An example is "Delegated to agy...". An
	// implementation must write it as early as it can, or it must make other true
	// bytes move. It must do this before it waits for replyText.
	//
	// This exists because of a true defect, and a live test confirmed it on
	// 2026-07-29. The HTTP/2 client of cursor-agent stopped a stream with a delegate
	// reply, and it gave "http2: stream closed". That stream received zero bytes for
	// the full time of a slow delegate call.
	//
	// A run of the CLI of a different vendor, with no console, can need much more
	// time than a usual completion. And the old signature only gave WriteReply a
	// text that was already complete. Therefore no code could put anything on the
	// wire early.
	//
	// Refer to cursorOriginAdapter.WriteReply and to
	// cursorrpc.WriteDelegateReplyWithKeepAlive for the vendor that needs this.
	//
	// The header has a second use: it tells the person what Glider delegated, and to
	// which CLI. A reply that Glider makes gives no other sign that a different CLI
	// produced it. DelegateHandler makes the header.
	//
	// replyText gives the final text of the reply when the code knows it: exactly
	// one value, and then the channel closes. An implementation must not assume that
	// the channel is already closed when WriteReply starts. A slow delegate call
	// fills it later, from a separate goroutine.
	WriteReply(w http.ResponseWriter, model string, stream bool, header string, replyText <-chan string) error

	// PriorUserInstructions returns a maximum of max true instructions from a
	// person, from earlier in the conversation of this request. The oldest is
	// first. It does not return the newest instruction, because that is already
	// the task of the delegate.
	//
	// It exists thus a delegated CLI gets true context of the session, and does
	// not start cold. Refer to vendors.ContextPack.
	//
	// A nil result is correct and honest, and it is not a failure. A vendor with
	// a request shape that truly carries no record returns nil. It does not
	// invent a record.
	//
	// An implementation MUST remove the added content, and MUST filter for the
	// true text of a person, in the same way as ExtractUserInstruction. A front
	// CLI adds content automatically. To give that content to a different
	// vendor, as if a person wrote it, is exactly the defect that NGL prevents.
	PriorUserInstructions(body []byte, max int) []string
}

var originAdapters []OriginAdapter

// RegisterOriginAdapter adds a to the registry consulted by
// ResolveOriginAdapter. Called from each adapter's own init(); never call
// this from shared dispatch code.
func RegisterOriginAdapter(a OriginAdapter) {
	originAdapters = append(originAdapters, a)
}

// ResolveOriginAdapter returns the first registered adapter with a Matches
// that gives true for r. It returns nil when no adapter recognizes this
// request. A request that no adapter recognizes is not the traffic of this
// repository to a vendor backend that it knows. Therefore a caller must treat
// nil exactly as "this is not my concern". That is the same safe default that
// the "" result of vendors.ResolveOriginVendorName already gives, for the
// removal of added content.
func ResolveOriginAdapter(r *http.Request) OriginAdapter {
	for _, a := range originAdapters {
		if a.Matches(r) {
			return a
		}
	}
	return nil
}
