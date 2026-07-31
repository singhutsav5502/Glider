package vendors

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"time"
)

// PendingResume is what Path A's ask/answer loop needs to remember between
// the moment a run is denied and the moment the human answers — see
// planning/permission_relay_design.md §2.3 and §5 item 4. In-memory,
// deliberately: this state is only ever needed for the few seconds-to-
// minutes between a denial being surfaced and the human's next message;
// surviving a Glider restart mid-conversation isn't worth the extra
// complexity this correlation actually needs.
type PendingResume struct {
	Vendor    Vendor
	Prompt    string
	SessionID string
	Denials   []Denial
	Cwd       string // resolved once at ask-time, reused unchanged for the resume — see workspace.go
	CreatedAt time.Time
}

// PendingResumeTTL bounds how long a token stays answerable — a var, not a
// const, so tests can shrink it. An old, forgotten denial shouldn't be
// resumable an hour later against a delegate session that may no longer
// even exist on the vendor's side.
var PendingResumeTTL = 30 * time.Minute

type resumeStore struct {
	mu      sync.Mutex
	pending map[string]PendingResume
}

var defaultResumeStore = &resumeStore{pending: map[string]PendingResume{}}

// RegisterPendingResume stores a denied run's state and returns a short
// correlation token for it — embedded in the "ask the human" text
// (FormatDenialSummary) and round-tripped back via "<token> /vendor:allow"
// or "<token> /vendor:deny", reusing ParseDelegateCommand's existing
// trailing ":<template>" flag syntax rather than inventing a second
// parser: "allow" and "deny" are handled as control-flow markers by the
// caller before ever reaching Vendor.ResolveTemplate, not real
// CommandTemplates.
//
// Also opportunistically evicts any already-expired entries — the only
// other place a token is ever removed is TakePendingResume, which only
// runs for a token someone actually replies to. A denial the human never
// answers (common in practice: they see the prompt, decide not to bother)
// would otherwise sit in this map forever on a long-running process —
// found in the 2026-07-28 security/reliability audit, unbounded growth on
// a service meant to run for days/weeks. Sweeping here (not a separate
// background goroutine) keeps this self-contained and bounds growth by
// "how often does a NEW denial happen," which is already the map's own
// natural growth rate.
func RegisterPendingResume(v Vendor, prompt, sessionID, cwd string, denials []Denial) string {
	token := newToken()
	defaultResumeStore.mu.Lock()
	defer defaultResumeStore.mu.Unlock()
	cutoff := time.Now().Add(-PendingResumeTTL)
	for k, pr := range defaultResumeStore.pending {
		if pr.CreatedAt.Before(cutoff) {
			delete(defaultResumeStore.pending, k)
		}
	}
	defaultResumeStore.pending[token] = PendingResume{
		Vendor: v, Prompt: prompt, SessionID: sessionID, Denials: denials, Cwd: cwd, CreatedAt: time.Now(),
	}
	return token
}

// PendingResumeCount returns the number of currently-stored pending
// resumes — test-only visibility into defaultResumeStore's size (which is
// otherwise unexported), so a test can confirm expired entries actually
// get swept rather than only checking TakePendingResume's per-token
// behavior (which would look identical whether or not the sweep exists,
// since Take already deletes-on-expiry for whichever single token it's
// asked about).
func PendingResumeCount() int {
	defaultResumeStore.mu.Lock()
	defer defaultResumeStore.mu.Unlock()
	return len(defaultResumeStore.pending)
}

// TakePendingResume looks up and removes a token's state — one-shot, so a
// stale or replayed answer can't double-resume the same denial. ok=false
// for an unknown or expired token; an expired entry is deleted either way,
// not left to leak.
func TakePendingResume(token string) (PendingResume, bool) {
	defaultResumeStore.mu.Lock()
	defer defaultResumeStore.mu.Unlock()
	pr, found := defaultResumeStore.pending[token]
	if !found {
		return PendingResume{}, false
	}
	delete(defaultResumeStore.pending, token)
	if time.Since(pr.CreatedAt) > PendingResumeTTL {
		return PendingResume{}, false
	}
	return pr, true
}

func newToken() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// ResolveDelegate executes one delegate turn's full control flow — shared
// by every HTTP-facing caller (internal/mitm/delegate_handler.go,
// internal/api/anthropic_messages.go) so the allow/deny logic exists in
// exactly one place. templateName "allow"/"deny" are control-flow markers,
// not real CommandTemplates (see RegisterPendingResume's doc comment) —
// vendor is the registry-resolved vendor from ParseDelegateCommand for a
// normal run, but is ignored for allow/deny in favor of the vendor stored
// in the pending resume itself (the token alone identifies which run is
// being answered).
//
// originPID identifies the real CLI process that sent this request (0 if
// unresolvable — non-Windows today, or a lookup failure) — see
// workspace.go for why this exists: neither the Messages API nor any
// other wire protocol Glider intercepts carries a filesystem path, so
// {{cwd}} can't be resolved from the request body at all. When
// originPID's directory isn't known yet (no per-PID entry, no configured
// default), this asks the human once via plain reply text rather than
// silently falling back to Glider's own server directory — confirmed live
// 2026-07-26 that fallback is actively wrong, not just imprecise. An
// unresolvable PID (0) skips the ask entirely and keeps the old
// os.Getwd()-based behavior as a last-resort degradation, since there's no
// way to correlate a later "/workspace" reply back to an unidentified
// origin anyway.
func ResolveDelegate(ctx context.Context, vendor Vendor, templateName, prompt string, originPID uint32) string {
	return ResolveDelegateWithContext(ctx, vendor, templateName, prompt, originPID, ContextPack{})
}

// ResolveDelegateWithContext is ResolveDelegate plus the session context to
// hand the delegated CLI, written into that vendor's own context file for
// the duration of the run (see ContextPack). ResolveDelegate is the thin
// no-context wrapper over this, mirroring how Run wraps RunWithOptions —
// so every existing caller and test keeps working unchanged, and only the
// two HTTP-facing entry points (which actually have the conversation in
// hand) need to pass a pack.
func ResolveDelegateWithContext(ctx context.Context, vendor Vendor, templateName, prompt string, originPID uint32, pack ContextPack) string {
	switch templateName {
	case "allow":
		return resolveAllow(ctx, strings.TrimSpace(prompt))
	case "deny":
		return resolveDeny(strings.TrimSpace(prompt))
	default:
		tmpl, ok := vendor.ResolveTemplate(templateName)
		if !ok {
			return fmt.Sprintf("%s has no %q command template.", vendor.Name, templateName)
		}

		var cwd string
		if originPID != 0 {
			dir, found := defaultWorkspaceStore.Lookup(originPID)
			if !found {
				return fmt.Sprintf("I don't know which directory to run %s in for this session yet. "+
					"Reply with \"<path> /workspace\" (e.g. \". /workspace\" for the current directory) to set it once, "+
					"then resend your request. You can also set a default workspace from the dashboard's Vendors page.", vendor.Name)
			}
			cwd = dir
		}

		if tmpl.Mode == "interactive" {
			return resolveInteractive(vendor, tmpl, prompt, cwd)
		}

		pack.Task = prompt
		pack.Workspace = cwd
		out, runErr := RunWithOptions(ctx, vendor, prompt, RunOptions{Template: templateName, Cwd: cwd, ContextPack: pack})
		if runErr != nil {
			return fmt.Sprintf("Delegation to %s failed: %s", vendor.Name, runErr.Error())
		}
		if len(out.Denials) > 0 {
			// Raw text, not renderDelegateReply: a denied run stopped
			// before reaching a terminal result event by definition, so
			// there is no clean final answer to extract — trying anyway
			// would just append a confusing "couldn't parse" note right
			// before the actual permission-denial message.
			token := RegisterPendingResume(vendor, prompt, out.SessionID, cwd, out.Denials)
			return FormatDenialSummary(vendor.Name, token, out.Denials, out.Text)
		}
		return renderDelegateReply(vendor.Name, out.Text) + FormatEditSummary(vendor.Name, out.EditViews)
	}
}

// resolveInteractive hands off to LaunchInteractiveFunc instead of
// RunWithOptions — there is no stdout to capture (a genuinely interactive
// session, not a headless one), so unlike the default case above there is
// no RunResult, no denial detection, and nothing to relay back except a
// short confirmation that the window was opened. This is deliberately NOT
// Path B (planning/permission_relay_design.md §3, still unbuilt): no pty
// relay, no correlation back into this chat, no way for Glider to see or
// act on anything that happens in that window from here on.
func resolveInteractive(vendor Vendor, tmpl CommandTemplate, prompt, cwd string) string {
	args := substituteTemplateArgs(tmpl.Args, prompt, "", cwd, "", "")
	if err := LaunchInteractiveFunc(vendor, cwd, args...); err != nil {
		return fmt.Sprintf("Could not open %s interactively: %s", vendor.Name, err.Error())
	}
	where := cwd
	if where == "" {
		where = "its default directory"
	}
	return fmt.Sprintf("Opened %s in a new interactive window (in %s) with your task queued as its first message. "+
		"Continue there directly — this chat won't see anything from that session.", vendor.Name, where)
}

// resolveAllow grants whatever scoped, vendor-specific permission the
// denied run needs (via the registered VendorAdapter's
// GrantResumePermission — a no-op for claude/cursor-agent, a real
// settings.json side effect for agy, see agy_grant.go) and re-invokes the
// vendor's "resume" template. The grant's revert always runs, success or
// failure, so any side effect stays scoped to exactly this one call.
func resolveAllow(ctx context.Context, token string) string {
	pr, found := TakePendingResume(token)
	if !found {
		return fmt.Sprintf("No pending permission request found for token %q (it may already be answered, denied, or expired).", token)
	}

	adapter := adapterFor(pr.Vendor.Name)
	revert, err := adapter.GrantResumePermission(pr.Vendor, pr.Cwd, pr.Denials)
	if err != nil {
		return fmt.Sprintf("Could not grant resume permission for %s: %s", pr.Vendor.Name, err.Error())
	}
	resumePrompt := adapter.WrapResumePrompt(pr.Prompt)
	extraArgs := adapter.ExtraResumeArgs(pr.Denials)

	out, runErr := RunWithOptions(ctx, pr.Vendor, resumePrompt, RunOptions{Template: "resume", Resume: pr.SessionID, Cwd: pr.Cwd, ExtraArgs: extraArgs})
	revertErr := revert()

	var revertNote string
	if revertErr != nil {
		// The granted permission may now linger past this one call — a
		// real fact worth telling the human, not a detail to swallow just
		// because the resume itself already ran either way.
		revertNote = fmt.Sprintf("\n\n(warning: could not revert the temporary permission grant for %s: %s — it may still be active)", pr.Vendor.Name, revertErr.Error())
	}

	if runErr != nil {
		return fmt.Sprintf("Resume of %s failed: %s%s", pr.Vendor.Name, runErr.Error(), revertNote)
	}
	if len(out.Denials) > 0 {
		newToken := RegisterPendingResume(pr.Vendor, pr.Prompt, out.SessionID, pr.Cwd, out.Denials)
		return FormatDenialSummary(pr.Vendor.Name, newToken, out.Denials, out.Text) + revertNote
	}
	return renderDelegateReply(pr.Vendor.Name, out.Text) + FormatEditSummary(pr.Vendor.Name, out.EditViews) + revertNote
}

func resolveDeny(token string) string {
	pr, found := TakePendingResume(token)
	if !found {
		return fmt.Sprintf("No pending permission request found for token %q (it may already be answered, denied, or expired).", token)
	}
	return fmt.Sprintf("Denied — %s's request was not resumed.", pr.Vendor.Name)
}
