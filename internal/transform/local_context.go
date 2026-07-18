package transform

import (
	"strings"
	"unicode/utf8"

	"github.com/glider-ai/glider/internal/backend"
	"github.com/glider-ai/glider/internal/config"
	"github.com/glider-ai/glider/internal/contextkit"
)

const defaultLocalSystemMaxChars = 4000

// BoundLocalContext shrinks a CompletionRequest for local backends so Path A
// mega-history (and legacy StreamChat) does not drown small models.
//
// Policy (local_context=latest_turn):
//  1. Keep leading system message(s), truncated to LocalSystemMaxChars total.
//  2. Keep the latest real user turn.
//  3. If that turn is mid tool-loop (assistant tool_calls / role=tool after it),
//     keep from that user through the end so tool round-trips stay intact.
//
// Path B ExtractBidiCompletionRequest already builds a single user message
// (TipTap LatestUserTurnText) — this is effectively a no-op there.
// Cloud / origin passthrough must NOT call this; sticky deny-local is unchanged.
func BoundLocalContext(req *backend.CompletionRequest, cfg config.TransformConfig) *backend.CompletionRequest {
	if req == nil {
		return nil
	}
	mode := strings.ToLower(strings.TrimSpace(cfg.LocalContext))
	if mode == "" || mode == "full" {
		return cloneRequest(req)
	}
	if mode != "latest_turn" {
		return cloneRequest(req)
	}
	if len(req.Messages) == 0 {
		return cloneRequest(req)
	}

	maxSys := cfg.LocalSystemMaxChars
	if maxSys <= 0 {
		maxSys = defaultLocalSystemMaxChars
	}

	out := cloneRequest(req)
	msgs := out.Messages

	systems, restStart := takeLeadingSystems(msgs, maxSys)
	rest := msgs[restStart:]
	if len(rest) == 0 {
		out.Messages = systems
		return out
	}

	userIdx := lastRealUserIndex(rest)
	if userIdx < 0 {
		// No user turn — keep systems + last message only.
		kept := append([]backend.Message{}, systems...)
		kept = append(kept, rest[len(rest)-1])
		out.Messages = kept
		return out
	}

	turn := rest[userIdx:]
	kept := append([]backend.Message{}, systems...)
	kept = append(kept, turn...)
	out.Messages = kept
	return out
}

// InjectEpisodeContext prepends a short system preamble from compressed episodes
// (not TipTap history). No-op when count is 0 or episodes empty.
func InjectEpisodeContext(req *backend.CompletionRequest, eps []contextkit.Episode, cfg config.TransformConfig) *backend.CompletionRequest {
	if req == nil || len(eps) == 0 || cfg.LocalEpisodeCount <= 0 {
		return req
	}
	n := cfg.LocalEpisodeCount
	if n > len(eps) {
		n = len(eps)
	}
	selected := eps
	if len(eps) > n {
		selected = eps[len(eps)-n:]
	}
	preamble := contextkit.FormatEpisodePreamble(selected, cfg.LocalEpisodeMaxChars)
	if preamble == "" {
		return req
	}
	out := cloneRequest(req)
	out.Messages = append([]backend.Message{{Role: "system", Content: preamble}}, out.Messages...)
	return out
}

func takeLeadingSystems(msgs []backend.Message, maxChars int) (systems []backend.Message, restStart int) {
	used := 0
	for i, m := range msgs {
		if !strings.EqualFold(m.Role, "system") {
			return systems, i
		}
		content := m.Content
		if used >= maxChars {
			break
		}
		remain := maxChars - used
		if utf8.RuneCountInString(content) > remain {
			content = truncateRunes(content, remain)
		}
		cp := m
		cp.Content = content
		systems = append(systems, cp)
		used += utf8.RuneCountInString(content)
		restStart = i + 1
	}
	return systems, restStart
}

// lastRealUserIndex finds the latest user message that starts a turn
// (not a misplaced tool payload).
func lastRealUserIndex(msgs []backend.Message) int {
	for i := len(msgs) - 1; i >= 0; i-- {
		m := msgs[i]
		if !strings.EqualFold(m.Role, "user") {
			continue
		}
		if strings.TrimSpace(m.ToolCallID) != "" {
			continue
		}
		return i
	}
	return -1
}

func truncateRunes(s string, max int) string {
	if max <= 0 {
		return ""
	}
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	n := 0
	for i := range s {
		if n == max {
			return s[:i]
		}
		n++
	}
	return s
}
