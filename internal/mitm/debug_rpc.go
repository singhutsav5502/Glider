package mitm

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/glider-ai/glider/internal/cursorrpc"
	"github.com/glider-ai/glider/internal/metrics"
)

const (
	defaultDebugRingSize    = 128
	defaultDumpPreviewBytes = 256
	// Large Agent context BidiAppends often gunzip past 256 KiB; 4 MiB keeps
	// outer+inner wire inspect usable without retaining the full body on disk.
	maxGunzipForDebug = 4 << 20 // 4 MiB cap for debug inflate
)

type connectSessionKey struct{}

// WithConnectSession attaches a CONNECT tunnel id for correlation with HTTP requests.
func WithConnectSession(ctx context.Context, id string) context.Context {
	if id == "" {
		return ctx
	}
	return context.WithValue(ctx, connectSessionKey{}, id)
}

// ConnectSessionFrom extracts the CONNECT session id from a request context.
func ConnectSessionFrom(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	v, _ := ctx.Value(connectSessionKey{}).(string)
	return v
}

// RPCObservation is one decrypted HTTP request observed under agent RPC debug.
type RPCObservation struct {
	Time            time.Time         `json:"time"`
	ConnectSession  string            `json:"connect_session,omitempty"`
	Host            string            `json:"host"`
	Path            string            `json:"path"`
	Method          string            `json:"method"`
	Kind            string            `json:"kind"` // openai_compat | agent_rpc | agent_rpc_opaque | agent_rpc_fulfillable | cursor_control | other
	ContentType     string            `json:"content_type,omitempty"`
	ContentEncoding string            `json:"content_encoding,omitempty"`
	ContentLength   int64             `json:"content_length"`
	ConnectProtoVer string            `json:"connect_protocol_version,omitempty"`
	RequestID       string            `json:"request_id,omitempty"`
	SessionID       string            `json:"session_id,omitempty"` // x-session-id (redacted length only in dumps)
	BodyBytes       int               `json:"body_bytes"`
	FrameCount      int               `json:"frame_count,omitempty"`
	FrameSizes      []int             `json:"frame_sizes,omitempty"`
	FrameFlags      []uint8           `json:"frame_flags,omitempty"`
	HeadersRedacted map[string]string `json:"headers_redacted,omitempty"`
	DumpFile        string            `json:"dump_file,omitempty"`
	Outcome         string            `json:"outcome,omitempty"` // opaque | decode | local | passthrough | skip_control | skip | …
	PreviewHex      string            `json:"preview_hex,omitempty"`
	PreviewLen      int               `json:"preview_len,omitempty"`
	// ProtoWire is a safe field-tag / wire-type summary (no string contents).
	ProtoWire string `json:"proto_wire,omitempty"`
	// InflatedBytes is set when Content-Encoding gzip was inflated for wire summary.
	InflatedBytes int `json:"inflated_bytes,omitempty"`
	// InflateTruncated is true when gunzip hit maxGunzipForDebug (wire walk may be incomplete).
	InflateTruncated bool `json:"inflate_truncated,omitempty"`
	// Bidi* fields are set for BidiAppend envelopes (stub inspect; not fulfillable).
	BidiRequestID string `json:"bidi_request_id,omitempty"`
	BidiSeq       uint64 `json:"bidi_seq,omitempty"`
	BidiHasSeq    bool   `json:"bidi_has_seq,omitempty"`
	BidiHint      string `json:"bidi_hint,omitempty"` // short printable hint only
	// BidiInspect is the structured Path B R&D summary (hex unwrap / role guess / strings).
	BidiInspect *cursorrpc.BidiAppendInspect `json:"bidi_inspect,omitempty"`
	// RunSSEInspect summarizes AgentService/RunSSE request frames (UUID only today).
	RunSSEInspect *cursorrpc.RunSSEInspect `json:"runsse_inspect,omitempty"`
	// RunSSEResponse summarizes a peeked origin response body (Phase 2 Observe).
	RunSSEResponse *cursorrpc.RunSSEResponseInspect `json:"runsse_response,omitempty"`
}

// AgentRPCDebugger records structured observations for Path B R&D.
// When Enabled is false, all methods are no-ops.
type AgentRPCDebugger struct {
	Enabled  bool
	DumpDir  string
	Log      *slog.Logger
	Metrics  *metrics.Collector
	PreviewN int // first N body bytes to dump (default 256)
	RingSize int
	// DumpControl, when true, writes dump files for cursor_control paths.
	// Default false: control/telemetry still hit the ring + slog, but skip disk dumps
	// so ~/.glider/mitm-debug stays chat-bearing (BidiAppend / RunSSE / …).
	DumpControl bool

	mu         sync.Mutex
	ring       []RPCObservation
	ringPos    int
	ringLen    int
	pathCounts map[string]int // "kind|path" → count
	seq        atomic.Uint64
}

// DefaultDebugDumpDir returns ~/.glider/mitm-debug (or ./.glider-debug if home unknown).
func DefaultDebugDumpDir() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return filepath.Join(".", ".glider-debug")
	}
	return filepath.Join(home, ".glider", "mitm-debug")
}

func (d *AgentRPCDebugger) log() *slog.Logger {
	if d != nil && d.Log != nil {
		return d.Log
	}
	return slog.Default()
}

func (d *AgentRPCDebugger) previewN() int {
	if d != nil && d.PreviewN > 0 {
		return d.PreviewN
	}
	return defaultDumpPreviewBytes
}

func (d *AgentRPCDebugger) dumpDir() string {
	if d != nil && strings.TrimSpace(d.DumpDir) != "" {
		return ExpandPath(d.DumpDir)
	}
	return DefaultDebugDumpDir()
}

// ClassifyDebugKind returns a finer label for debug/metrics (fulfillable vs opaque).
func ClassifyDebugKind(path string) string {
	kind := ClassifyPath(path)
	switch kind {
	case PathOpenAICompat:
		return "openai_compat"
	case PathCursorControl:
		return "cursor_control"
	case PathAgentRPC:
		if cursorrpc.IsFulfillableAgentPath(path) {
			return "agent_rpc_fulfillable"
		}
		return "agent_rpc_opaque"
	default:
		return "other"
	}
}

// Observe records one request. body may be nil when not yet read; pass body when available.
// Does not mutate the request. Safe for concurrent use.
func (d *AgentRPCDebugger) Observe(r *http.Request, body []byte, outcome string) *RPCObservation {
	if d == nil || !d.Enabled || r == nil {
		return nil
	}
	host := r.Host
	if host == "" && r.URL != nil {
		host = r.URL.Host
	}
	path := ""
	if r.URL != nil {
		path = r.URL.Path
	}
	kind := ClassifyDebugKind(path)
	ct := r.Header.Get("Content-Type")
	ce := r.Header.Get("Content-Encoding")
	cpv := r.Header.Get("Connect-Protocol-Version")
	reqID := firstNonEmpty(
		r.Header.Get("X-Request-Id"),
		r.Header.Get("x-request-id"),
		r.Header.Get("X-Amzn-Trace-Id"),
	)
	sessHdr := r.Header.Get("X-Session-Id")
	if sessHdr == "" {
		sessHdr = r.Header.Get("x-session-id")
	}

	cl := r.ContentLength
	if cl < 0 {
		cl = int64(len(body))
	}
	if len(body) > 0 {
		cl = int64(len(body))
	}

	frames := ScanConnectFrames(body)
	obs := RPCObservation{
		Time:            time.Now().UTC(),
		ConnectSession:  ConnectSessionFrom(r.Context()),
		Host:            host,
		Path:            path,
		Method:          r.Method,
		Kind:            kind,
		ContentType:     ct,
		ContentEncoding: ce,
		ContentLength:   cl,
		ConnectProtoVer: cpv,
		RequestID:       reqID,
		SessionID:       redactTokenPreview(sessHdr),
		BodyBytes:       len(body),
		HeadersRedacted: RedactHeaders(r.Header),
		Outcome:         outcome,
	}
	if len(frames) > 0 {
		obs.FrameCount = len(frames)
		obs.FrameSizes = make([]int, len(frames))
		obs.FrameFlags = make([]uint8, len(frames))
		for i, f := range frames {
			obs.FrameSizes[i] = f.Size
			obs.FrameFlags[i] = f.Flags
		}
	}
	n := d.previewN()
	if len(body) > 0 {
		prev := body
		if len(prev) > n {
			prev = prev[:n]
		}
		obs.PreviewLen = len(prev)
		obs.PreviewHex = hex.EncodeToString(prev)
	}

	wireBody, inflated, truncated := debugBodyForWire(body, ce)
	if inflated > 0 {
		obs.InflatedBytes = inflated
	}
	if truncated {
		obs.InflateTruncated = true
	}
	// Prefer innermost Connect frame payload for wire summary when framed.
	wireScan := wireBody
	if len(frames) == 1 && frames[0].Size > 0 && len(wireBody) >= 5+frames[0].Size {
		wireScan = wireBody[5 : 5+frames[0].Size]
	}
	if summary := SummarizeProtoWire(wireScan, 48); summary != "" {
		obs.ProtoWire = summary
	}
	if cursorrpc.IsBidiAppendPath(path) {
		if insp, err := cursorrpc.InspectBidiAppend(wireScan); err == nil && insp != nil {
			obs.BidiRequestID = insp.RequestID
			obs.BidiHasSeq = insp.HasSeq
			obs.BidiSeq = insp.Seq
			obs.BidiHint = insp.PrintableHint
			obs.BidiInspect = insp
			if insp.InnerWire != "" {
				pw := insp.OuterWire + " | inner:" + insp.InnerWire
				if insp.InnerNestedWire != "" {
					pw += " | nested:" + insp.InnerNestedWire
				}
				obs.ProtoWire = pw
			}
		}
	}
	if cursorrpc.IsRunSSEPath(path) {
		// Prefer original body (Connect-framed); fall back to unwrapped wireScan.
		runBody := body
		if len(runBody) == 0 {
			runBody = wireScan
		}
		if insp, err := cursorrpc.InspectRunSSE(runBody); err == nil && insp != nil {
			obs.RunSSEInspect = insp
			if insp.ProtoWire != "" {
				obs.ProtoWire = insp.ProtoWire
			}
			if insp.RequestID != "" && obs.RequestID == "" {
				obs.RequestID = insp.RequestID
			}
		}
	}

	shouldDump := kind != "cursor_control" || d.DumpControl
	if shouldDump {
		if dumpPath, err := d.writeDump(obs, body); err == nil && dumpPath != "" {
			obs.DumpFile = dumpPath
		} else if err != nil {
			d.log().Debug("mitm agent rpc debug dump failed", "err", err, "path", path)
		}
	}

	d.push(obs)
	d.bumpPath(kind, path)

	attrs := []any{
		"host", obs.Host,
		"path", obs.Path,
		"method", obs.Method,
		"kind", obs.Kind,
		"content_type", obs.ContentType,
		"content_encoding", obs.ContentEncoding,
		"content_length", obs.ContentLength,
		"connect_protocol_version", obs.ConnectProtoVer,
		"request_id", obs.RequestID,
		"connect_session", obs.ConnectSession,
		"body_bytes", obs.BodyBytes,
		"outcome", obs.Outcome,
	}
	if obs.FrameCount > 0 {
		attrs = append(attrs, "frame_count", obs.FrameCount, "frame_sizes", obs.FrameSizes)
	}
	if obs.ProtoWire != "" {
		attrs = append(attrs, "proto_wire", obs.ProtoWire)
	}
	if obs.BidiRequestID != "" {
		attrs = append(attrs, "bidi_request_id", obs.BidiRequestID)
	}
	if obs.BidiHasSeq {
		attrs = append(attrs, "bidi_seq", obs.BidiSeq)
	}
	if obs.BidiInspect != nil {
		attrs = append(attrs,
			"bidi_payload_kind", obs.BidiInspect.PayloadKind,
			"bidi_role_guess", obs.BidiInspect.RoleGuess,
			"bidi_inner_top", obs.BidiInspect.InnerTopField,
		)
	}
	if obs.RunSSEInspect != nil && obs.RunSSEInspect.RequestID != "" {
		attrs = append(attrs, "runsse_request_id", obs.RunSSEInspect.RequestID)
	}
	if obs.InflatedBytes > 0 {
		attrs = append(attrs, "inflated_bytes", obs.InflatedBytes)
	}
	if obs.InflateTruncated {
		attrs = append(attrs, "inflate_truncated", true)
	}
	if obs.DumpFile != "" {
		attrs = append(attrs, "dump_file", obs.DumpFile)
	}
	d.log().Info("mitm agent rpc debug", attrs...)

	if d.Metrics != nil {
		d.Metrics.IncAction("mitm", "debug_observe")
		d.Metrics.IncAction("mitm", "debug_"+kind)
	}
	return &obs
}

// ObserveResponse records a peek of an origin passthrough response (Phase 2).
// Safe for concurrent use. Does not block the client stream (caller tees asynchronously).
func (d *AgentRPCDebugger) ObserveResponse(r *http.Request, statusCode int, contentType string, peek []byte) *RPCObservation {
	if d == nil || !d.Enabled || r == nil {
		return nil
	}
	host := r.Host
	if host == "" && r.URL != nil {
		host = r.URL.Host
	}
	path := ""
	if r.URL != nil {
		path = r.URL.Path
	}
	kind := ClassifyDebugKind(path)
	reqID := firstNonEmpty(
		r.Header.Get("X-Request-Id"),
		r.Header.Get("x-request-id"),
	)
	obs := RPCObservation{
		Time:            time.Now().UTC(),
		ConnectSession:  ConnectSessionFrom(r.Context()),
		Host:            host,
		Path:            path,
		Method:          r.Method,
		Kind:            kind,
		ContentType:     contentType,
		ContentLength:   int64(len(peek)),
		RequestID:       reqID,
		BodyBytes:       len(peek),
		Outcome:         "response_peek",
		HeadersRedacted: map[string]string{"status": fmt.Sprintf("%d", statusCode)},
	}
	n := d.previewN()
	if len(peek) > 0 {
		prev := peek
		if len(prev) > n {
			prev = prev[:n]
		}
		obs.PreviewLen = len(prev)
		obs.PreviewHex = hex.EncodeToString(prev)
	}
	if cursorrpc.IsRunSSEPath(path) || ClassifyPath(path) == PathAgentRPC {
		insp := cursorrpc.InspectRunSSEResponseBody(peek, statusCode, contentType)
		if insp != nil {
			obs.RunSSEResponse = insp
			if insp.ProtoWire != "" {
				obs.ProtoWire = insp.ProtoWire
			}
			if insp.FrameCount > 0 {
				obs.FrameCount = insp.FrameCount
				obs.FrameSizes = insp.FrameSizes
				obs.FrameFlags = insp.FrameFlags
			}
		}
	}

	shouldDump := kind != "cursor_control" || d.DumpControl
	if shouldDump {
		if dumpPath, err := d.writeResponseDump(obs, peek); err == nil && dumpPath != "" {
			obs.DumpFile = dumpPath
		}
	}

	d.push(obs)
	d.bumpPath(kind+"_response", path)

	attrs := []any{
		"host", obs.Host,
		"path", obs.Path,
		"kind", obs.Kind,
		"outcome", obs.Outcome,
		"status", statusCode,
		"peek_bytes", len(peek),
		"request_id", obs.RequestID,
		"connect_session", obs.ConnectSession,
	}
	if obs.FrameCount > 0 {
		attrs = append(attrs, "frame_count", obs.FrameCount, "frame_sizes", obs.FrameSizes)
	}
	if obs.ProtoWire != "" {
		attrs = append(attrs, "proto_wire", obs.ProtoWire)
	}
	if rs := obs.RunSSEResponse; rs != nil && rs.PrintableHint != "" {
		attrs = append(attrs, "response_hint", rs.PrintableHint)
	}
	if obs.DumpFile != "" {
		attrs = append(attrs, "dump_file", obs.DumpFile)
	}
	d.log().Info("mitm agent rpc response debug", attrs...)
	if d.Metrics != nil {
		d.Metrics.IncAction("mitm", "debug_response_observe")
	}
	return &obs
}

func (d *AgentRPCDebugger) writeResponseDump(obs RPCObservation, peek []byte) (string, error) {
	dir := d.dumpDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	seq := d.seq.Add(1)
	safeHost := sanitizeFilePart(obs.Host)
	safePath := sanitizeFilePart(filepath.Base(obs.Path))
	if safePath == "" || safePath == "." {
		safePath = "rpc"
	}
	name := fmt.Sprintf("%s_%06d_%s_%s_RESP.txt",
		obs.Time.Format("20060102T150405.000"),
		seq, safeHost, safePath)
	full := filepath.Join(dir, name)

	var b strings.Builder
	fmt.Fprintf(&b, "time=%s\n", obs.Time.Format(time.RFC3339Nano))
	fmt.Fprintf(&b, "direction=response\n")
	fmt.Fprintf(&b, "connect_session=%s\n", obs.ConnectSession)
	fmt.Fprintf(&b, "host=%s\n", obs.Host)
	fmt.Fprintf(&b, "path=%s\n", obs.Path)
	fmt.Fprintf(&b, "kind=%s\n", obs.Kind)
	fmt.Fprintf(&b, "outcome=%s\n", obs.Outcome)
	fmt.Fprintf(&b, "request_id=%s\n", obs.RequestID)
	fmt.Fprintf(&b, "peek_bytes=%d\n", len(peek))
	if obs.ProtoWire != "" {
		fmt.Fprintf(&b, "proto_wire=%s\n", obs.ProtoWire)
	}
	if rs := obs.RunSSEResponse; rs != nil {
		fmt.Fprintf(&b, "runsse_response.status=%d\n", rs.StatusCode)
		fmt.Fprintf(&b, "runsse_response.frame_count=%d\n", rs.FrameCount)
		if len(rs.FrameSizes) > 0 {
			fmt.Fprintf(&b, "runsse_response.frame_sizes=%v\n", rs.FrameSizes)
			fmt.Fprintf(&b, "runsse_response.frame_flags=%v\n", rs.FrameFlags)
		}
		if rs.PrintableHint != "" {
			fmt.Fprintf(&b, "runsse_response.hint=%s\n", rs.PrintableHint)
		}
		if len(rs.Strings) > 0 {
			fmt.Fprintf(&b, "runsse_response.strings=%q\n", rs.Strings)
		}
	}
	n := d.previewN()
	prev := peek
	if len(prev) > n {
		prev = prev[:n]
	}
	fmt.Fprintf(&b, "preview_hex=%s\n", hex.EncodeToString(prev))
	if err := os.WriteFile(full, []byte(b.String()), 0o600); err != nil {
		return "", err
	}
	// Binary sibling for offline frame decode (Phase 2→4).
	binPath := strings.TrimSuffix(full, ".txt") + ".bin"
	_ = os.WriteFile(binPath, peek, 0o600)
	// Frame classification lines when deep inspect available.
	if rs := obs.RunSSEResponse; rs != nil && len(rs.Frames) > 0 {
		var fb strings.Builder
		fmt.Fprintf(&fb, "time=%s\nrequest_id=%s\nframes=%d\n",
			obs.Time.Format(time.RFC3339Nano), obs.RequestID, len(rs.Frames))
		for _, fr := range rs.Frames {
			fmt.Fprintf(&fb, "frame[%d] flags=%d wire=%d inflated=%d kind=%s hint=%q wire=%s\n",
				fr.Index, fr.Flags, fr.WireBytes, fr.Inflated, fr.Kind, fr.TextHint, fr.ProtoWire)
		}
		_ = os.WriteFile(strings.TrimSuffix(full, ".txt")+"_frames.txt", []byte(fb.String()), 0o600)
	}
	return full, nil
}

func (d *AgentRPCDebugger) bumpPath(kind, path string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.pathCounts == nil {
		d.pathCounts = make(map[string]int)
	}
	key := kind + "|" + path
	d.pathCounts[key]++
}

func (d *AgentRPCDebugger) push(obs RPCObservation) {
	d.mu.Lock()
	defer d.mu.Unlock()
	size := d.RingSize
	if size <= 0 {
		size = defaultDebugRingSize
	}
	if d.ring == nil || cap(d.ring) != size {
		d.ring = make([]RPCObservation, size)
		d.ringPos = 0
		d.ringLen = 0
	}
	d.ring[d.ringPos] = obs
	d.ringPos = (d.ringPos + 1) % size
	if d.ringLen < size {
		d.ringLen++
	}
}

// Recent returns the newest observations (newest first), up to limit (0 = all in ring).
func (d *AgentRPCDebugger) Recent(limit int) []RPCObservation {
	if d == nil {
		return nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.ringLen == 0 {
		return nil
	}
	size := len(d.ring)
	out := make([]RPCObservation, 0, d.ringLen)
	for i := 0; i < d.ringLen; i++ {
		idx := (d.ringPos - 1 - i + size*2) % size
		out = append(out, d.ring[idx])
	}
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

// PathCounts returns per-kind|path observation counts.
func (d *AgentRPCDebugger) PathCounts() map[string]int {
	if d == nil {
		return nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make(map[string]int, len(d.pathCounts))
	for k, v := range d.pathCounts {
		out[k] = v
	}
	return out
}

func (d *AgentRPCDebugger) writeDump(obs RPCObservation, body []byte) (string, error) {
	dir := d.dumpDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	seq := d.seq.Add(1)
	safeHost := sanitizeFilePart(obs.Host)
	safePath := sanitizeFilePart(filepath.Base(obs.Path))
	if safePath == "" || safePath == "." {
		safePath = "rpc"
	}
	name := fmt.Sprintf("%s_%06d_%s_%s.txt",
		obs.Time.Format("20060102T150405.000"),
		seq, safeHost, safePath)
	full := filepath.Join(dir, name)

	var b strings.Builder
	fmt.Fprintf(&b, "time=%s\n", obs.Time.Format(time.RFC3339Nano))
	fmt.Fprintf(&b, "connect_session=%s\n", obs.ConnectSession)
	fmt.Fprintf(&b, "host=%s\n", obs.Host)
	fmt.Fprintf(&b, "method=%s\n", obs.Method)
	fmt.Fprintf(&b, "path=%s\n", obs.Path)
	fmt.Fprintf(&b, "kind=%s\n", obs.Kind)
	fmt.Fprintf(&b, "outcome=%s\n", obs.Outcome)
	fmt.Fprintf(&b, "content_type=%s\n", obs.ContentType)
	fmt.Fprintf(&b, "content_encoding=%s\n", obs.ContentEncoding)
	fmt.Fprintf(&b, "content_length=%d\n", obs.ContentLength)
	fmt.Fprintf(&b, "connect_protocol_version=%s\n", obs.ConnectProtoVer)
	fmt.Fprintf(&b, "request_id=%s\n", obs.RequestID)
	fmt.Fprintf(&b, "session_id=%s\n", obs.SessionID)
	fmt.Fprintf(&b, "body_bytes=%d\n", obs.BodyBytes)
	fmt.Fprintf(&b, "frame_count=%d\n", obs.FrameCount)
	if len(obs.FrameSizes) > 0 {
		fmt.Fprintf(&b, "frame_sizes=%v\n", obs.FrameSizes)
		fmt.Fprintf(&b, "frame_flags=%v\n", obs.FrameFlags)
	}
	if obs.InflatedBytes > 0 {
		fmt.Fprintf(&b, "inflated_bytes=%d\n", obs.InflatedBytes)
	}
	if obs.InflateTruncated {
		fmt.Fprintf(&b, "inflate_truncated=true\n")
	}
	if obs.ProtoWire != "" {
		fmt.Fprintf(&b, "proto_wire=%s\n", obs.ProtoWire)
	}
	if obs.BidiRequestID != "" {
		fmt.Fprintf(&b, "bidi_request_id=%s\n", obs.BidiRequestID)
	}
	if obs.BidiHasSeq {
		fmt.Fprintf(&b, "bidi_seq=%d\n", obs.BidiSeq)
	}
	if obs.BidiHint != "" {
		fmt.Fprintf(&b, "bidi_hint=%s\n", obs.BidiHint)
	}
	if insp := obs.BidiInspect; insp != nil {
		fmt.Fprintf(&b, "bidi_inspect.payload_kind=%s\n", insp.PayloadKind)
		fmt.Fprintf(&b, "bidi_inspect.inner_top_field=%d\n", insp.InnerTopField)
		fmt.Fprintf(&b, "bidi_inspect.role_guess=%s\n", insp.RoleGuess)
		if insp.InnerNestedWire != "" {
			fmt.Fprintf(&b, "bidi_inspect.inner_nested_wire=%s\n", insp.InnerNestedWire)
		}
		if insp.NestedKindGuess != "" {
			fmt.Fprintf(&b, "bidi_inspect.nested_kind_guess=%s\n", insp.NestedKindGuess)
		}
		if len(insp.Strings) > 0 {
			fmt.Fprintf(&b, "bidi_inspect.strings=%q\n", insp.Strings)
		}
	}
	if rs := obs.RunSSEInspect; rs != nil {
		fmt.Fprintf(&b, "runsse_inspect.request_id=%s\n", rs.RequestID)
		fmt.Fprintf(&b, "runsse_inspect.frame_count=%d\n", rs.FrameCount)
		fmt.Fprintf(&b, "runsse_inspect.frame_size=%d\n", rs.FrameSize)
		fmt.Fprintf(&b, "runsse_inspect.frame_flags=%d\n", rs.FrameFlags)
		fmt.Fprintf(&b, "runsse_inspect.role_guess=%s\n", rs.RoleGuess)
	}
	b.WriteString("headers_redacted:\n")
	for k, v := range obs.HeadersRedacted {
		fmt.Fprintf(&b, "  %s: %s\n", k, v)
	}
	n := d.previewN()
	prev := body
	if len(prev) > n {
		prev = prev[:n]
	}
	fmt.Fprintf(&b, "preview_bytes=%d\n", len(prev))
	fmt.Fprintf(&b, "preview_hex=%s\n", hex.EncodeToString(prev))
	if len(body) > n {
		fmt.Fprintf(&b, "note=body truncated for dump; total_bytes=%d preview_cap=%d\n", len(body), n)
	}

	if err := os.WriteFile(full, []byte(b.String()), 0o600); err != nil {
		return "", err
	}
	return full, nil
}

// debugBodyForWire returns bytes suitable for protobuf wire walking.
// When Content-Encoding is gzip, attempts a bounded inflate (no secrets logged).
// truncated is true when the inflate hit maxGunzipForDebug.
func debugBodyForWire(body []byte, contentEncoding string) (out []byte, inflated int, truncated bool) {
	if len(body) == 0 {
		return body, 0, false
	}
	ce := strings.ToLower(strings.TrimSpace(contentEncoding))
	if ce != "gzip" && !(len(body) >= 2 && body[0] == 0x1f && body[1] == 0x8b) {
		return body, 0, false
	}
	gr, err := gzip.NewReader(bytes.NewReader(body))
	if err != nil {
		return body, 0, false
	}
	defer gr.Close()
	limited := io.LimitReader(gr, int64(maxGunzipForDebug)+1)
	plain, err := io.ReadAll(limited)
	if err != nil || len(plain) == 0 {
		return body, 0, false
	}
	if len(plain) > maxGunzipForDebug {
		return plain[:maxGunzipForDebug], maxGunzipForDebug, true
	}
	return plain, len(plain), false
}

// SummarizeProtoWire walks protobuf wire types without decoding string contents.
// Returns a compact tag summary like "1:ld(150),2:ld(38),3:varint" (safe for R&D dumps).
func SummarizeProtoWire(body []byte, maxFields int) string {
	if len(body) == 0 {
		return ""
	}
	if maxFields <= 0 {
		maxFields = 48
	}
	var parts []string
	off := 0
	for off < len(body) && len(parts) < maxFields {
		key, n := readProtoVarint(body[off:])
		if n <= 0 {
			break
		}
		off += n
		fieldNum := int(key >> 3)
		wireType := int(key & 7)
		if fieldNum <= 0 || fieldNum > 536870911 {
			break
		}
		switch wireType {
		case 0: // varint
			_, vn := readProtoVarint(body[off:])
			if vn <= 0 {
				return strings.Join(parts, ",")
			}
			off += vn
			parts = append(parts, fmt.Sprintf("%d:varint", fieldNum))
		case 1: // 64-bit
			if off+8 > len(body) {
				return strings.Join(parts, ",")
			}
			off += 8
			parts = append(parts, fmt.Sprintf("%d:i64", fieldNum))
		case 2: // length-delimited
			ln, vn := readProtoVarint(body[off:])
			if vn <= 0 || ln < 0 || off+vn+int(ln) > len(body) {
				return strings.Join(parts, ",")
			}
			off += vn + int(ln)
			parts = append(parts, fmt.Sprintf("%d:ld(%d)", fieldNum, ln))
		case 5: // 32-bit
			if off+4 > len(body) {
				return strings.Join(parts, ",")
			}
			off += 4
			parts = append(parts, fmt.Sprintf("%d:i32", fieldNum))
		default:
			// Start / end group (deprecated) or unknown — stop.
			return strings.Join(parts, ",")
		}
	}
	if len(parts) == 0 {
		return ""
	}
	if off < len(body) && len(parts) >= maxFields {
		parts = append(parts, "…")
	}
	return strings.Join(parts, ",")
}

func readProtoVarint(b []byte) (uint64, int) {
	var x uint64
	for i := 0; i < len(b) && i < 10; i++ {
		c := b[i]
		x |= uint64(c&0x7f) << (7 * i)
		if c < 0x80 {
			return x, i + 1
		}
	}
	return 0, 0
}

// ConnectFrame is a best-effort Connect envelope summary (no protobuf parse).
type ConnectFrame struct {
	Flags uint8
	Size  int
}

// ScanConnectFrames walks Connect-style envelopes without fully parsing payloads.
// Stops on truncated or non-envelope bodies (returns what was scanned).
func ScanConnectFrames(body []byte) []ConnectFrame {
	if len(body) < 5 {
		return nil
	}
	var out []ConnectFrame
	off := 0
	for off+5 <= len(body) {
		flags := body[off]
		// Reject reserved bits — likely raw protobuf, not framed.
		if flags&^0b00000011 != 0 {
			return out
		}
		n := int(body[off+1])<<24 | int(body[off+2])<<16 | int(body[off+3])<<8 | int(body[off+4])
		if n < 0 || off+5+n > len(body) {
			return out
		}
		out = append(out, ConnectFrame{Flags: flags, Size: n})
		off += 5 + n
		// Cap to avoid pathological loops on adversarial lengths.
		if len(out) >= 256 {
			break
		}
	}
	return out
}

// RedactHeaders copies headers with secrets stripped/redacted.
func RedactHeaders(h http.Header) map[string]string {
	out := make(map[string]string)
	if h == nil {
		return out
	}
	for k, vals := range h {
		lk := strings.ToLower(k)
		joined := strings.Join(vals, ", ")
		switch {
		case lk == "authorization", lk == "proxy-authorization":
			out[k] = redactAuthorization(joined)
		case strings.Contains(lk, "api-key"), strings.Contains(lk, "apikey"),
			lk == "cookie", lk == "set-cookie",
			strings.Contains(lk, "token"),
			lk == "x-cursor-checksum",
			lk == "x-api-key":
			out[k] = redactTokenPreview(joined)
		default:
			out[k] = joined
		}
	}
	return out
}

func redactAuthorization(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return ""
	}
	lower := strings.ToLower(v)
	if strings.HasPrefix(lower, "bearer ") {
		tok := strings.TrimSpace(v[7:])
		return "Bearer " + redactTokenPreview(tok)
	}
	return redactTokenPreview(v)
}

// redactTokenPreview keeps length + short prefix/suffix; never the full secret.
func redactTokenPreview(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return ""
	}
	if len(v) <= 8 {
		return fmt.Sprintf("[redacted len=%d]", len(v))
	}
	return fmt.Sprintf("%s…%s [len=%d]", v[:4], v[len(v)-2:], len(v))
}

func sanitizeFilePart(s string) string {
	s = strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			return r
		default:
			return '_'
		}
	}, s)
	if len(s) > 64 {
		s = s[:64]
	}
	return s
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
