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
// (FormatDenialSummary) and round-tripped back via "/vendor:allow <token>"
// or "/vendor:deny <token>", reusing ParseDelegateCommand's existing
// ":<template>" flag syntax rather than inventing a second parser: "allow"
// and "deny" are handled as control-flow markers by the caller before ever
// reaching Vendor.ResolveTemplate, not real CommandTemplates.
func RegisterPendingResume(v Vendor, prompt, sessionID, cwd string, denials []Denial) string {
	token := newToken()
	defaultResumeStore.mu.Lock()
	defer defaultResumeStore.mu.Unlock()
	defaultResumeStore.pending[token] = PendingResume{
		Vendor: v, Prompt: prompt, SessionID: sessionID, Denials: denials, Cwd: cwd, CreatedAt: time.Now(),
	}
	return token
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
	switch templateName {
	case "allow":
		return resolveAllow(ctx, strings.TrimSpace(prompt))
	case "deny":
		return resolveDeny(strings.TrimSpace(prompt))
	default:
		opts := RunOptions{Template: templateName}
		if originPID != 0 {
			dir, ok := defaultWorkspaceStore.Lookup(originPID)
			if !ok {
				return fmt.Sprintf("I don't know which directory to run %s in for this session yet. "+
					"Reply with \"/workspace <path>\" (e.g. \"/workspace .\" for the current directory) to set it once, "+
					"then resend your request. You can also set a default workspace from the dashboard's Vendors page.", vendor.Name)
			}
			opts.Cwd = dir
		}

		out, runErr := RunWithOptions(ctx, vendor, prompt, opts)
		if runErr != nil {
			return fmt.Sprintf("Delegation to %s failed: %s", vendor.Name, runErr.Error())
		}
		if len(out.Denials) > 0 {
			token := RegisterPendingResume(vendor, prompt, out.SessionID, opts.Cwd, out.Denials)
			return FormatDenialSummary(vendor.Name, token, out.Denials, out.Text)
		}
		return out.Text + FormatEditSummary(vendor.Name, out.EditViews)
	}
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

	out, runErr := RunWithOptions(ctx, pr.Vendor, resumePrompt, RunOptions{Template: "resume", Resume: pr.SessionID, Cwd: pr.Cwd})
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
	return out.Text + FormatEditSummary(pr.Vendor.Name, out.EditViews) + revertNote
}

func resolveDeny(token string) string {
	pr, found := TakePendingResume(token)
	if !found {
		return fmt.Sprintf("No pending permission request found for token %q (it may already be answered, denied, or expired).", token)
	}
	return fmt.Sprintf("Denied — %s's request was not resumed.", pr.Vendor.Name)
}
