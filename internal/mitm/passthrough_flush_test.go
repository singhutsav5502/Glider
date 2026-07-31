package mitm

import (
	"bytes"
	"crypto/tls"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"
)

// flushRecorder is an http.ResponseWriter that only makes bytes "visible"
// on Flush — modeling exactly what net/http itself does (buffer writes,
// release on flush/fill/return) so a test can distinguish "the relay
// wrote bytes" from "the client could actually see them yet."
type flushRecorder struct {
	mu      sync.Mutex
	pending bytes.Buffer
	visible bytes.Buffer
	flushes int
	hdr     http.Header
	code    int
}

func newFlushRecorder() *flushRecorder {
	return &flushRecorder{hdr: make(http.Header)}
}

func (f *flushRecorder) Header() http.Header { return f.hdr }

func (f *flushRecorder) WriteHeader(code int) { f.code = code }

func (f *flushRecorder) Write(p []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.pending.Write(p)
}

func (f *flushRecorder) Flush() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.visible.Write(f.pending.Bytes())
	f.pending.Reset()
	f.flushes++
}

func (f *flushRecorder) visibleBytes() []byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]byte(nil), f.visible.Bytes()...)
}

// TestPassthroughHTTPS_FlushesSmallStreamingFramesBeforeResponseEnds is
// the direct regression test for the root cause found 2026-07-31, after a
// long investigation into cursor-agent hanging for 160-420s behind
// Glider's transparent MITM while the identical request took ~19s
// directly.
//
// net/http buffers response writes internally (2048 bytes for HTTP/1.1,
// 4096 for HTTP/2) and only releases them when that buffer fills, the
// handler returns, or something calls Flush. passthroughHTTPS relayed the
// response with a bare io.Copy and called Flush nowhere — so a streaming
// RPC response, which by design is a trickle of small frames, piled up in
// the buffer instead of reaching the client. Live data was bimodal and
// conclusive: every hung AgentService/Run stream plateaued at 116-215
// bytes (under both thresholds, so the client saw nothing at all and
// reconnected on its own ~30s timer, repeatedly), while every stream that
// actually delivered had crossed 14KB and therefore auto-flushed purely
// as a side effect of exceeding the buffer.
//
// This test fails (times out waiting for visible bytes) against the
// pre-fix bare io.Copy, and passes with flushAfterWrite in place.
func TestPassthroughHTTPS_FlushesSmallStreamingFramesBeforeResponseEnds(t *testing.T) {
	// A frame far smaller than either net/http buffer threshold — the
	// exact shape (a 5-byte Connect envelope header + tiny payload) that
	// a real heartbeat-driven streaming RPC emits.
	smallFrame := []byte{0x00, 0x00, 0x00, 0x00, 0x04, 0xde, 0xad, 0xbe, 0xef}

	release := make(chan struct{})
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Error("upstream test server ResponseWriter is not an http.Flusher")
			return
		}
		w.Header().Set("Content-Type", "application/connect+proto")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(smallFrame)
		flusher.Flush()
		// Hold the response open — a real streaming RPC stays open long
		// after its first frames. The relay must not wait for this.
		<-release
	}))
	defer upstream.Close()
	defer close(release)

	upstreamURL, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatalf("parse upstream URL: %v", err)
	}

	p := &Proxy{
		Log:             slog.New(slog.NewTextHandler(io.Discard, nil)),
		DialTimeout:     5 * time.Second,
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // httptest self-signed cert
	}

	req := httptest.NewRequest(http.MethodPost, "https://"+upstreamURL.Host+"/agent.v1.AgentService/Run", nil)
	rec := newFlushRecorder()

	relayDone := make(chan error, 1)
	go func() {
		relayDone <- p.passthroughHTTPS(rec, req, upstreamURL.Hostname(), upstreamURL.Host)
	}()

	// The whole point: these bytes must become visible to the client
	// while the response is still open, not only once it finally ends.
	deadline := time.After(10 * time.Second)
	for {
		if bytes.Contains(rec.visibleBytes(), smallFrame) {
			return // fixed behavior — frame reached the client promptly
		}
		select {
		case err := <-relayDone:
			t.Fatalf("relay returned (err=%v) without ever making the small frame visible to the client — "+
				"a streaming response's early frames must be flushed, not buffered until the stream ends", err)
		case <-deadline:
			t.Fatalf("small streaming frame never became visible to the client within 10s "+
				"(flushes=%d, visible=%d bytes) — this is the buffered-relay bug: net/http holds writes "+
				"until its 2048/4096-byte buffer fills, so a trickle of small frames never reaches the client",
				rec.flushes, len(rec.visibleBytes()))
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// TestFlushAfterWrite_FlushesEveryWrite pins flushAfterWrite's own
// contract directly: one Flush per non-empty Write, so no byte ever waits
// on a buffer it can't be sure will fill.
func TestFlushAfterWrite_FlushesEveryWrite(t *testing.T) {
	rec := newFlushRecorder()
	w := flushAfterWrite{w: rec, flusher: rec}

	for i := 0; i < 3; i++ {
		if _, err := w.Write([]byte{0xaa}); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}
	if rec.flushes != 3 {
		t.Fatalf("got %d flushes across 3 writes, want 3", rec.flushes)
	}
	if got := rec.visibleBytes(); len(got) != 3 {
		t.Fatalf("got %d visible bytes, want 3", len(got))
	}
}
