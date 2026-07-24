package loop

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// SCORE must be an explicit token on its own line (not a mid-sentence float) so chatty
// prose cannot pass eval. Accepts plain, markdown-bold/italic, and ATX header wrappers:
//
//	SCORE: 0.8
//	**SCORE: 0.8**
//	*SCORE: 0.8*
//	## SCORE: 0.8
var scoreLine = regexp.MustCompile(`(?mi)^\s*(?:#{1,6}\s+)?(?:\*{1,3}|_{1,3})?\s*SCORE:\s*([0-9]*\.?[0-9]+)\s*(?:\*{1,3}|_{1,3})?\s*$`)

func parseEvalScore(text string) float64 {
	v, _ := parseEvalScoreOK(text)
	return v
}

// parseEvalScoreOK requires an explicit SCORE token (plain / markdown-wrapped line) or a
// structured {"score":…,"reason":…} object — chatty apologies without SCORE are not a pass.
func parseEvalScoreOK(text string) (float64, bool) {
	if v, ok := parseCriticJSONScore(text); ok {
		return clampScore(v), true
	}
	m := scoreLine.FindStringSubmatch(text)
	if len(m) < 2 {
		return 0, false
	}
	v, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return 0, false
	}
	return clampScore(v), true
}

func clampScore(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

// normalizeCriticOutput maps structured critic JSON onto SCORE:/REASON: lines so the
// rest of the eval path (logging, digest) stays line-oriented.
func normalizeCriticOutput(text string) string {
	v, reason, ok := parseCriticJSON(text)
	if !ok {
		return text
	}
	if strings.TrimSpace(reason) == "" {
		reason = "ok"
	}
	return fmt.Sprintf("SCORE: %g\nREASON: %s", clampScore(v), singleLine(reason))
}

// parseCriticJSONScore extracts an explicit "score" field from critic JSON output.
func parseCriticJSONScore(text string) (float64, bool) {
	v, _, ok := parseCriticJSON(text)
	return v, ok
}

func parseCriticJSON(text string) (score float64, reason string, ok bool) {
	raw := extractJSONObject(text)
	if raw == "" {
		return 0, "", false
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(raw), &obj); err != nil {
		return 0, "", false
	}
	scoreVal, hasScore := obj["score"]
	if !hasScore {
		return 0, "", false
	}
	v, okNum := jsonNumberAsFloat(scoreVal)
	if !okNum {
		return 0, "", false
	}
	if r, _ := obj["reason"].(string); r != "" {
		reason = r
	}
	return v, reason, true
}

func extractJSONObject(text string) string {
	text = strings.TrimSpace(text)
	// Strip common markdown fences around a JSON blob.
	if strings.HasPrefix(text, "```") {
		body := text
		if i := strings.Index(body, "\n"); i >= 0 {
			body = body[i+1:]
		}
		body = strings.TrimSpace(body)
		if j := strings.LastIndex(body, "```"); j >= 0 {
			body = strings.TrimSpace(body[:j])
		}
		text = body
	}
	start := strings.Index(text, "{")
	end := strings.LastIndex(text, "}")
	if start < 0 || end <= start {
		return ""
	}
	return text[start : end+1]
}

func jsonNumberAsFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(n), 64)
		return f, err == nil
	default:
		return 0, false
	}
}
