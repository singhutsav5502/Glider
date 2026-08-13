// Package safego starts a goroutine with a long life in the background, and it
// recovers a panic.
//
// The default behaviour of Go for a panic with no recovery, in ANY goroutine and
// not only in the first one, is to stop the full process immediately. No other
// goroutine gets an opportunity to stop cleanly.
//
// The Server of net/http already recovers a panic inside one request handler.
// That behaviour is well known and documented. Therefore a defect in one request
// of the gateway or of the dashboard fails that one request only.
//
// The true exposure that the audit of production readiness found on 2026-07-28
// was in each other position: this repository had zero calls to `recover()`.
// Therefore a panic in any loop in the background would stop the full service
// with no message. Those loops are the poll of the VRAM, the tests of the health
// of a backend, the packet workers of the transparent redirector, and the read
// loop of an MCP session.
//
// The gateway, the dashboard and the interception would all stop together. That
// would most probably occur with no person present, for a service that must
// operate for days or weeks.
package safego

import (
	"log/slog"
	"runtime/debug"
)

// Go runs fn in a new goroutine, recovering any panic instead of letting
// it crash the process — logs the panic value and a stack trace via log
// (falls back to slog.Default() if log is nil, so this works from call
// sites that do not have a logger handy), then lets the goroutine exit
// normally. name identifies which background loop panicked, since a bare
// stack trace alone does not say "this was pollVRAM" vs "this was the MCP
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
