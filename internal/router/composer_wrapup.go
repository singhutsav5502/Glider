package router

import (
	"context"
	"regexp"
	"strings"

	"github.com/glider-ai/glider/internal/backend"
	"github.com/glider-ai/glider/internal/config"
)

// ComposerWrapupRuleName is the default dashboard/config name for the wrap-up → origin rule.
const ComposerWrapupRuleName = "composer_wrapup_origin"

// Trigger type string for glider.yaml routing.rules[].trigger.type.
const TriggerComposerWrapup = "composer_wrapup"

// composerWrapupRE matches Cursor chrome wrap-ups / final summaries / title packs.
// Underscore forms are listed explicitly (\bsummary\b does not match inside
// user_visible_high_level_summary).
var composerWrapupRE = regexp.MustCompile(`(?i)(` +
	`user_visible_high_level_summary|high_level_summary|` +
	`composer[_ ]?summary|progress_reporting|` +
	`completed_subtitle|final_summary|executive_summary|` +
	`\b(?:` +
	`summariz(?:e|ing|ation)|reply\s+summary|final\s+summary|brief\s+summary|` +
	`one[\s-]?sentence|one[\s-]?line\s+summary|short\s+(?:title|summary|description)|` +
	`generate\s+(?:a\s+)?title|title\s+(?:for|of|generation)|` +
	`conversation\s+title|thread\s+title|chat\s+title|` +
	`name\s+this\s+(?:chat|composer|conversation)|` +
	`wrap[\s-]?up|what\s+was\s+(?:done|changed)|concise\s+summary|recap` +
	`)\b` +
	`)`)

// ComposerWrapupOriginRule forces cloud/origin for composer wrap-up chrome and for
// non-fresh TipTap follow-ons while a StickyCloud / last-cloud family is live.
// Priority should sit below explicit /cloud|/local (99/100) and above task_classifier
// / Small Context Local so wrap-ups never local-fulfill after /cloud.
type ComposerWrapupOriginRule struct {
	name     string
	priority int
	action   config.ActionConfig
}

// NewComposerWrapupOriginRule builds the first-class wrap-up → origin rule.
func NewComposerWrapupOriginRule(cfg config.RuleConfig) *ComposerWrapupOriginRule {
	name := strings.TrimSpace(cfg.Name)
	if name == "" {
		name = ComposerWrapupRuleName
	}
	action := cfg.Action
	if strings.TrimSpace(action.Target) == "" {
		action.Target = "cloud"
	}
	return &ComposerWrapupOriginRule{
		name:     name,
		priority: cfg.Priority,
		action:   action,
	}
}

func (r *ComposerWrapupOriginRule) Name() string  { return r.name }
func (r *ComposerWrapupOriginRule) Priority() int { return r.priority }

func (r *ComposerWrapupOriginRule) Evaluate(_ context.Context, req *backend.CompletionRequest) (*RuleResult, error) {
	if req == nil {
		return &RuleResult{Matched: false}, nil
	}
	// Explicit slash on current turn wins elsewhere; never steal /local.
	content := joinMessageContent(req)
	if HasLocalOverride(content) {
		return &RuleResult{Matched: false}, nil
	}
	scan := content
	if s := strings.TrimSpace(req.Metadata.WrapupScan); s != "" {
		scan = scan + "\x00" + s
	}
	wrapup := MatchComposerWrapup(content, []byte(scan))
	fresh := IsFreshUserTipTapTurn(content, req.Metadata.ExtractSource, []byte(scan))

	stickyCloud := req.Metadata.StickyCloudLive
	lastCloud := req.Metadata.LastRouteCloud
	if stickyCloud && !fresh {
		return &RuleResult{Matched: true, Action: actionToDecision(r.name, r.action)}, nil
	}
	if lastCloud && !fresh && (wrapup || isChromeOnlyCrumb(content, req.Metadata.ExtractSource)) {
		return &RuleResult{Matched: true, Action: actionToDecision(r.name, r.action)}, nil
	}
	if wrapup {
		return &RuleResult{Matched: true, Action: actionToDecision(r.name, r.action)}, nil
	}
	return &RuleResult{Matched: false}, nil
}

// MatchComposerWrapup reports wrap-up / final-summary / title chrome in text or scan bytes.
func MatchComposerWrapup(userText string, scan []byte) bool {
	if composerWrapupRE.MatchString(userText) {
		return true
	}
	if len(scan) == 0 {
		return false
	}
	body := scan
	if len(body) > 512<<10 {
		body = body[:512<<10]
	}
	return composerWrapupRE.Match(body)
}

// IsFreshUserTipTapTurn mirrors MITM HasFreshUserTipTapTurn for the routing engine:
// printable_hint / section_fallback / chrome packs are NOT fresh user turns.
func IsFreshUserTipTapTurn(userText, extractSource string, scan []byte) bool {
	if MatchComposerWrapup(userText, scan) {
		return false
	}
	t := strings.TrimSpace(userText)
	if t == "" || len(t) < 16 {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(extractSource)) {
	case "printable_hint", "section_fallback":
		return false
	case "tiptap_text":
		return true
	}
	if len(scan) == 0 {
		return true
	}
	return bytesContainsTipTap(scan)
}

func isChromeOnlyCrumb(userText, extractSource string) bool {
	t := strings.TrimSpace(userText)
	if t == "" || len(t) < 16 {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(extractSource)) {
	case "printable_hint", "section_fallback", "":
		return true
	}
	return false
}

func bytesContainsTipTap(body []byte) bool {
	if len(body) == 0 {
		return false
	}
	scan := body
	if len(scan) > 4<<20 {
		scan = scan[:4<<20]
	}
	s := string(scan)
	return strings.Contains(s, `"type":"doc"`) ||
		strings.Contains(s, `"type": "doc"`) ||
		strings.Contains(s, `"type":"text"`) ||
		strings.Contains(s, `"type": "text"`)
}

func joinMessageContent(req *backend.CompletionRequest) string {
	if req == nil || len(req.Messages) == 0 {
		return ""
	}
	var b strings.Builder
	for i, m := range req.Messages {
		if i > 0 {
			b.WriteByte(0)
		}
		b.WriteString(m.Content)
	}
	return b.String()
}
