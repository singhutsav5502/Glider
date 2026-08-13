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

// PendingResume holds what the ask-and-answer loop of Path A must remember.
// It keeps that data between the moment when a run gets a refusal and the
// moment when the person answers. Refer to
// planning/permission_relay_design.md §2.3 and §5 item 4.
//
// This state stays in memory on purpose. Code needs it only for the seconds
// or minutes between the message about the refusal and the next message from
// the person. To keep it through a restart of Glider in the middle of a
// conversation is not worth the additional complexity.
type PendingResume struct {
	Vendor    Vendor
	Prompt    string
	SessionID string
	Denials   []Denial
	Cwd       string // resolved once at ask-time, reused unchanged for the resume — see workspace.go
	CreatedAt time.Time
}

// PendingResumeTTL bounds how long a token stays answerable — a var, not a
// const, so tests can shrink it. An old, forgotten denial should not be
// resumable an hour later against a delegate session that may no longer
// even exist on the vendor's side.
var PendingResumeTTL = 30 * time.Minute

type resumeStore struct {
	mu      sync.Mutex
	pending map[string]PendingResume
}

var defaultResumeStore = &resumeStore{pending: map[string]PendingResume{}}

// RegisterPendingResume keeps the state of a run that got a refusal. It
// returns a short token to correlate that state.
//
// FormatDenialSummary puts the token in the text that asks the person. The
// person sends it back as "<token> /vendor:allow" or "<token> /vendor:deny".
// This uses the trailing ":<template>" syntax that ParseDelegateCommand
// already reads, and it does not add a second parser. "allow" and "deny" are
// markers for the flow of control. The caller processes them before the code
// arrives at Vendor.ResolveTemplate, and they are not true CommandTemplates.
//
// This function also removes each entry that is expired, when it can. The
// only other position that removes a token is TakePendingResume, and that
// function operates only for a token that a person answers. A person
// frequently does not answer a refusal: they see the question and decide to
// do nothing. Such an entry would stay in this map for the full life of the
// process. The security and reliability audit on 2026-07-28 found this
// growth with no limit, on a service that must operate for days or weeks.
//
// The removal occurs here, and not in a separate goroutine in the background.
// Thus this code stays complete in one position, and the growth of the map
// has the limit "how frequently does a NEW refusal occur". That is already
// the natural rate of growth of the map.
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

// PendingResumeCount returns the count of the pending resumes that the store
// holds now. It gives a test visibility into the size of defaultResumeStore,
// which is not exported.
//
// A test needs this to confirm that the code truly removes the expired
// entries. A test of the behaviour of TakePendingResume for one token cannot
// show this, because Take deletes an expired token that it examines. That
// result is the same with the removal and with no removal.
func PendingResumeCount() int {
	defaultResumeStore.mu.Lock()
	defer defaultResumeStore.mu.Unlock()
	return len(defaultResumeStore.pending)
}

// TakePendingResume finds the state of a token and removes it. It operates
// one time only. Therefore an answer that is old, or that a person sends
// again, cannot resume the same refusal a second time.
//
// It returns ok=false for a token that it does not know, and for a token that
// is expired. It deletes an expired entry in both conditions, and it does not
// let the entry stay.
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

// ResolveDelegate does the full control flow of one delegate turn. Each
// caller that faces HTTP uses it: internal/mitm/delegate_handler.go and
// internal/api/anthropic_messages.go. Therefore the allow and deny logic
// exists in exactly one position.
//
// The templateName values "allow" and "deny" are markers for the flow of
// control, and they are not true CommandTemplates. Refer to the comment on
// RegisterPendingResume. For a usual run, vendor is the vendor that
// ParseDelegateCommand found in the registry. For allow and for deny, this
// code ignores that value. It uses the vendor in the pending resume. The
// token alone identifies the run that the person answers.
//
// originPID identifies the true CLI process that sent this request. It is 0
// when the code cannot find it: on a platform that is not Windows today, or
// after a failed lookup. Refer to workspace.go for the cause: the Messages
// API and each other wire protocol that Glider intercepts carry no path in
// the file system. Thus the code cannot find {{cwd}} from the body of the
// request.
//
// The code can know no directory for originPID. That occurs when there is no
// entry for that PID, and no configured default. It then asks the person one
// time, in plain reply text. It does not use the server directory of Glider
// with no message. A live test on 2026-07-26 confirmed that this default is
// incorrect, and not only imprecise.
//
// A PID of 0 does not cause the question. That condition keeps the older
// behaviour, which uses os.Getwd(), as the last result. There is no method to
// correlate a later "/workspace" reply with an origin that has no identity.
func ResolveDelegate(ctx context.Context, vendor Vendor, templateName, prompt string, originPID uint32) string {
	return ResolveDelegateWithContext(ctx, vendor, templateName, prompt, originPID, ContextPack{})
}

// ResolveDelegateWithContext does what ResolveDelegate does, and it also
// gives the session context to the delegated CLI. Glider writes that context
// in the context file of that vendor for the time of the run. Refer to
// ContextPack.
//
// ResolveDelegate is the thin cover over this function that gives no context.
// This is the same pattern as Run over RunWithOptions. Therefore each caller
// and each test that exists continues with no change. Only the two entry
// points that face HTTP must give a pack, and only those two have the
// conversation.
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
				return askForWorkspace(vendor.Name)
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
			// Prefer the clean answer when the vendor produced one. A refusal does
			// not always stop a run: cursor-agent is refused, recovers, and still
			// emits a result event. Refer to cleanReplyOrEmpty for the live case
			// that corrected this. Raw text stays the fallback, with no
			// "couldn't parse" note in front of the refusal message.
			token := RegisterPendingResume(vendor, prompt, out.SessionID, cwd, out.Denials)
			text := out.Text
			if clean := cleanReplyOrEmpty(vendor.Name, out.Text); clean != "" {
				text = clean
			}
			return FormatDenialSummary(vendor.Name, token, out.Denials, text)
		}
		return renderDelegateReply(vendor.Name, out.Text) + FormatEditSummary(vendor.Name, out.EditViews)
	}
}

// resolveInteractive gives the work to LaunchInteractiveFunc, and not to
// RunWithOptions. There is no stdout to capture, because this is a true
// interactive session and not a session with no console. Therefore this is
// different from the usual condition above. There is no RunResult. There is
// no detection of a refusal. And there is nothing to send back, except a
// short message that says that Glider opened the window.
//
// This is NOT Path B, and that is on purpose. Refer to
// planning/permission_relay_design.md §3, which no person has built. There is
// no relay through a pty. There is no correlation back into this chat. And
// Glider cannot see or change anything in that window after this point.
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

// resolveAllow gives the permission that the run with the refusal needs. The
// permission has a small scope and it is different for each vendor. The
// registered VendorAdapter gives it with GrantResumePermission: that function
// does nothing for claude and cursor-agent, and it changes settings.json for
// agy. Refer to agy_grant.go. Then this function runs the "resume" template
// of the vendor again.
//
// The code that removes the permission always operates, after a success and
// after a failure. Therefore each change stays inside this one call.
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
		// The permission can now continue after this one call. This is a true fact,
		// and the person must know it. Do not hide it, because the resume itself
		// operated in both conditions.
		revertNote = fmt.Sprintf("\n\n(warning: could not revert the temporary permission grant for %s: %s — it may still be active)", pr.Vendor.Name, revertErr.Error())
	}

	if runErr != nil {
		return fmt.Sprintf("Resume of %s failed: %s%s", pr.Vendor.Name, runErr.Error(), revertNote)
	}
	if len(out.Denials) > 0 {
		// Same as the first-run refusal path above: a resume that hits a second
		// refusal can still carry a readable answer.
		newToken := RegisterPendingResume(pr.Vendor, pr.Prompt, out.SessionID, pr.Cwd, out.Denials)
		text := out.Text
		if clean := cleanReplyOrEmpty(pr.Vendor.Name, out.Text); clean != "" {
			text = clean
		}
		return FormatDenialSummary(pr.Vendor.Name, newToken, out.Denials, text) + revertNote
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

// askForWorkspace is the reply Glider sends when it does not know which
// directory this session works in.
//
// It asks EVERY new session, and a configured default does not answer for a
// session that no person has answered for. Refer to the comment on
// WorkspaceStore.Lookup for the cause: a global default silently sent the
// first handoff of a second project to the directory of the first project.
//
// A default is still of use, and this reply offers it. A person then accepts
// it with one copy, and does not have to type a path.
func askForWorkspace(vendorName string) string {
	msg := fmt.Sprintf("I do not know which directory to run %s in for this session yet. "+
		"Reply with \"<path> /workspace\" to set it one time, then send your request again. "+
		"For the directory that you are in now, reply \". /workspace\".", vendorName)
	if def := defaultWorkspaceStore.Default(); def != "" {
		msg += fmt.Sprintf(" Your default is %s — to use it here, reply \"%s /workspace\". "+
			"Glider asks each session, because it cannot see which project a new CLI is in.", def, def)
	}
	return msg
}
