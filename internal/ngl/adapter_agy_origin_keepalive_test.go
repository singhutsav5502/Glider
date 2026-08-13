package ngl

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestAgyWriteReply_SendsPeriodicEmptyEventsWhileWaiting mirrors
// cursorrpc's TestWriteDelegateReplyWithKeepAlive_SendsPeriodicHeartbeatsWhileWaiting
// — see WriteReply's doc comment for the real incident (2026-07-31) this
// closes.
func TestAgyWriteReply_SendsPeriodicEmptyEventsWhileWaiting(t *testing.T) {
	orig := agyDelegateKeepAliveInterval
	agyDelegateKeepAliveInterval = 25 * time.Millisecond
	defer func() { agyDelegateKeepAliveInterval = orig }()

	replyText := make(chan string)
	rr := httptest.NewRecorder()

	go func() {
		time.Sleep(120 * time.Millisecond)
		replyText <- "done"
		close(replyText)
	}()

	if err := (agyOriginAdapter{}).WriteReply(rr, "gemini-3.6-flash-high", true, "", replyText); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	body := rr.Body.String()
	// header was "" (suppressed), so every "data: " occurrence here is
	// either a keep-alive empty-text event or the final resolved one.
	dataEvents := strings.Count(body, "data: ")
	if dataEvents < 3 {
		t.Fatalf("got %d data events over a 120ms wait at a 25ms interval, want >= 3 — keep-alive ticker did not fire while waiting: %s", dataEvents, body)
	}
	if !strings.Contains(body, `"text":"done"`) {
		t.Fatalf("final resolved text never made it onto the wire: %s", body)
	}
}

// TestAgyWriteReply_HeaderArrivesBeforeReplyResolves confirms the header
// is written immediately, not held back until replyText resolves.
func TestAgyWriteReply_HeaderArrivesBeforeReplyResolves(t *testing.T) {
	replyText := make(chan string)
	headerWritten := make(chan struct{})
	rr := httptest.NewRecorder()

	go func() {
		<-headerWritten
		replyText <- "the real answer"
		close(replyText)
	}()

	done := make(chan error, 1)
	go func() {
		done <- (agyOriginAdapter{}).WriteReply(rr, "gemini-3.6-flash-high", true, "Delegated to cursor-agent:\n\n", replyText)
	}()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(rr.Body.String(), "Delegated to cursor-agent") {
			close(headerWritten)
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	if !strings.Contains(rr.Body.String(), "Delegated to cursor-agent") {
		t.Fatal("header never appeared on the wire — WriteReply must not block on replyText before writing header")
	}
	if err := <-done; err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}
