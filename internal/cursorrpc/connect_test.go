package cursorrpc_test

import (
	"bytes"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/glider-ai/glider/internal/cursorrpc"
)

// TestReadFirstEnvelope_StopsAfterOneEnvelope is the direct regression test
// for the real, live-confirmed root cause behind a night of chased
// http2-stream-closed symptoms (2026-07-29): agent.v1.AgentService/Run is a
// genuine bidi-streaming RPC, and cursor-agent's real client keeps sending
// small periodic keepalive envelopes on its own request stream for up to
// ~30s before actually closing it. io.ReadAll(r.Body) blocks for that
// entire window; ReadFirstEnvelope must return as soon as the first
// envelope is available, leaving anything the client sends afterward
// unread.
func TestReadFirstEnvelope_StopsAfterOneEnvelope(t *testing.T) {
	first := []byte{0x00, 0x00, 0x00, 0x00, 0x03, 'f', 'o', 'o'} // flags=0, len=3, payload="foo"
	second := []byte{0x00, 0x00, 0x00, 0x00, 0x03, 'b', 'a', 'r'}
	src := io.MultiReader(bytes.NewReader(first), &blockingReader{t: t}, bytes.NewReader(second))

	got, err := cursorrpc.ReadFirstEnvelope(src)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !bytes.Equal(got, first) {
		t.Fatalf("got %x, want %x — must return exactly the first envelope, not read ahead into the second", got, first)
	}
}

// blockingReader errors instead of blocking forever if ReadFirstEnvelope
// tries to read past the first envelope — a live deadlock during a test
// run is a worse failure mode than a clear assertion failure.
type blockingReader struct {
	t     *testing.T
	asked bool
}

func (b *blockingReader) Read(p []byte) (int, error) {
	if !b.asked {
		b.asked = true
		b.t.Fatal("ReadFirstEnvelope read past the first envelope — it must stop exactly at header+payload")
	}
	return 0, io.EOF
}

func TestReadFirstEnvelope_ShortHeaderErrors(t *testing.T) {
	_, err := cursorrpc.ReadFirstEnvelope(strings.NewReader("ab"))
	if err == nil {
		t.Fatal("expected an error for a body shorter than the 5-byte envelope header")
	}
}

func TestReadFirstEnvelope_TruncatedPayloadErrors(t *testing.T) {
	// Header claims 10 bytes of payload but only 2 are actually present.
	body := []byte{0x00, 0x00, 0x00, 0x00, 0x0a, 'h', 'i'}
	_, err := cursorrpc.ReadFirstEnvelope(bytes.NewReader(body))
	if err == nil {
		t.Fatal("expected an error for a payload shorter than the header's declared length")
	}
}

// TestReadFirstEnvelope_FastEvenWithSlowFollowUp proves the whole point of
// this function under real timing conditions, not just byte-shape: it must
// return promptly even when more data would eventually follow on the same
// stream (matching cursor-agent's real ~5s-interval keepalive pattern) —
// io.ReadAll on the same source would hang until that follow-up arrived.
func TestReadFirstEnvelope_FastEvenWithSlowFollowUp(t *testing.T) {
	pr, pw := io.Pipe()
	go func() {
		_, _ = pw.Write([]byte{0x00, 0x00, 0x00, 0x00, 0x02, 'h', 'i'})
		time.Sleep(2 * time.Second) // simulates a client's later keepalive envelope
		_, _ = pw.Write([]byte{0x00, 0x00, 0x00, 0x00, 0x02, 'y', 'o'})
		pw.Close()
	}()

	done := make(chan struct{})
	var got []byte
	var err error
	go func() {
		got, err = cursorrpc.ReadFirstEnvelope(pr)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("ReadFirstEnvelope did not return promptly — it must not wait for data the client sends later on the same stream")
	}
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []byte{0x00, 0x00, 0x00, 0x00, 0x02, 'h', 'i'}
	if !bytes.Equal(got, want) {
		t.Fatalf("got %x, want %x", got, want)
	}
}
