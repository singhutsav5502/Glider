// Package safego launches long-running background goroutines with panic
// recovery — Go's default behavior for an unrecovered panic in ANY
// goroutine (not just the one that started it) is to crash the entire
// process immediately, with no chance for other goroutines to shut down
// cleanly.
//
// net/http's own Server already recovers panics inside individual request
// handlers (a well-known, documented stdlib behavior) — a bug in a single
// gateway or dashboard request only fails that one request. The real
// exposure found in the 2026-07-28 production-readiness audit was
// everywhere else: zero `recover()` calls existed anywhere in this
// codebase, so a panic in any background loop (VRAM polling, backend
// health checks, the transparent redirector's packet workers, an MCP
// session's read loop) would silently kill the whole background service —
// gateway, dashboard, and interception all going down together, most
// plausibly unattended, for a service explicitly meant to run for
// days/weeks.
package safego

import (
	"log/slog"
	"runtime/debug"
)

// Go runs fn in a new goroutine, recovering any panic instead of letting
// it crash the process — logs the panic value and a stack trace via log
// (falls back to slog.Default() if log is nil, so this works from call
// sites that don't have a logger handy), then lets the goroutine exit
// normally. name identifies which background loop panicked, since a bare
// stack trace alone doesn't say "this was pollVRAM" vs "this was the MCP
// read loop."
//
// Deliberately does NOT restart fn automatically — a loop that panics
// once is quite likely to panic again on the same trigger, and a
// crash-loop-restart inside a single process is worse than "this one
// background feature stopped, the rest of Glider kept running": it can
// spin a hot CPU loop or spam logs, forever, invisibly. A human should see
// the log line and fix the actual bug.
func Go(name string, log *slog.Logger, fn func()) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				l := log
				if l == nil {
					l = slog.Default()
				}
				l.Error("recovered panic in background goroutine",
					"name", name, "panic", r, "stack", string(debug.Stack()))
			}
		}()
		fn()
	}()
}
