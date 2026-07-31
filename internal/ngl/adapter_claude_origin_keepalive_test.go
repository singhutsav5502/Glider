package ngl

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestWriteClaudeSSE_SendsPeriodicPingsWhileWaiting mirrors
// cursorrpc's own TestWriteDelegateReplyWithKeepAlive_SendsPeriodicHeartbeatsWhileWaiting
// — see writeClaudeSSE's doc comment for the real incident (2026-07-31)
// this closes: a real Claude Code session, delegating to a real
// cursor-agent target, hit its own client-side idle-stream timeout during
// the wait because no keep-alive was ever sent here.
func TestWriteClaudeSSE_SendsPeriodicPingsWhileWaiting(t *testing.T) {
	orig := claudeDelegateKeepAliveInterval
	claudeDelegateKeepAliveInterval = 25 * time.Millisecond
	defer func() { claudeDelegateKeepAliveInterval = orig }()

	replyText := make(chan string)
	rr := httptest.NewRecorder()

	go func() {
		time.Sleep(120 * time.Millisecond)
		replyText <- "done"
		close(replyText)
	}()

	if err := writeClaudeSSE(rr, "claude-3.5-sonnet", "", replyText); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	body := rr.Body.String()
	pingCount := strings.Count(body, "event: ping")
	// ~120ms of waiting at a 25ms interval should fire at least a few —
	// a generous floor so this isn't flaky on a loaded box, while still
	// failing if the ticker never fires at all (the pre-fix behavior).
	if pingCount < 2 {
		t.Fatalf("got %d ping events over a 120ms wait at a 25ms interval, want >= 2 — keep-alive ticker did not fire while waiting", pingCount)
	}
	if !strings.Contains(body, `"text":"done"`) {
		t.Fatalf("final resolved text never made it onto the wire: %s", body)
	}
}

// TestWriteClaudeSSE_HeaderArrivesBeforeReplyResolves confirms the header
// is written immediately, not held back until replyText resolves — the
// same "must not block before writing anything" guarantee
// cursorrpc.WriteDelegateReplyWithKeepAlive already has a test for.
func TestWriteClaudeSSE_HeaderArrivesBeforeReplyResolves(t *testing.T) {
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
		done <- writeClaudeSSE(rr, "claude-3.5-sonnet", "Delegated to cursor-agent:\n\n", replyText)
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
		t.Fatal("header never appeared on the wire — writeClaudeSSE must not block on replyText before writing header")
	}
	if err := <-done; err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}
