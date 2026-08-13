package mitm

import (
	"strings"
)

// PathKind classifies a decrypted HTTPS request path for MITM handling.
type PathKind int

const (
	// PathOther is unrecognized traffic — forward to origin, no harness.
	PathOther PathKind = iota
	// PathOpenAICompat is OpenAI-compatible chat/Responses JSON — harness eligible.
	PathOpenAICompat
	// PathAgentRPC is Cursor Agent Connect/gRPC (BidiAppend, AiService StreamChat, …).
	// Fulfillable StreamChat* paths are decoded by internal/cursorrpc; others stay opaque.
	PathAgentRPC
	// PathCursorControl is plugins, analytics, dashboard, composer lists, etc.
	PathCursorControl
)

func (k PathKind) String() string {
	switch k {
	case PathOpenAICompat:
		return "openai_compat"
	case PathAgentRPC:
		return "agent_rpc"
	case PathCursorControl:
		return "cursor_control"
	default:
		return "other"
	}
}

// IsLLMPath reports whether path is eligible for OpenAI/Responses harness interception.
func IsLLMPath(path string) bool {
	return ClassifyPath(path) == PathOpenAICompat
}

// ClassifyPath decides harness vs opaque skip for a request URL path.
//
// Harness: openai_compat JSON and fulfillable agent_rpc (AiService StreamChat*).
// Opaque agent paths (BidiAppend, etc.) still classify as agent_rpc but passthrough
// after best-effort decode (see cursorrpc.DecodeChatRequest).
func ClassifyPath(path string) PathKind {
	if path == "" {
		return PathOther
	}
	// Normalize: Connect paths are usually absolute; strip query if present.
	if i := strings.IndexByte(path, '?'); i >= 0 {
		path = path[:i]
	}

	if isOpenAICompatPath(path) {
		return PathOpenAICompat
	}
	if isAgentRPCPath(path) {
		return PathAgentRPC
	}
	if isCursorControlPath(path) {
		return PathCursorControl
	}
	return PathOther
}

func isOpenAICompatPath(path string) bool {
	// Exact and suffix forms cover api2.cursor.sh and any allowlisted host that still
	// speaks OpenAI-compatible HTTP (legacy Ask / BYOK-shaped traffic through MITM).
	switch {
	case path == "/v1/chat/completions",
		path == "/chat/completions",
		strings.HasSuffix(path, "/chat/completions"):
		return true
	case path == "/v1/responses",
		path == "/responses",
		strings.HasSuffix(path, "/responses"):
		// Avoid matching Connect RPCs whose method name ends in "Responses".
		if strings.Contains(path, "aiserver.") {
			return false
		}
		return true
	default:
		return false
	}
}

// Agent chat plane observed on api2.cursor.sh (Connect/gRPC over HTTP/1.1 or h2).
// Bodies are protobuf / connect framing — not OpenAI JSON.
//
// Cursor 3.x Agent also uses /agent.v1.AgentService/RunSSE (not under aiserver.v1).
func isAgentRPCPath(path string) bool {
	lower := strings.ToLower(path)

	// AiService: chat/composer only — Report* is telemetry (falls through to control).
	if strings.Contains(lower, "/aiserver.v1.aiservice/") {
		if strings.Contains(lower, "report") {
			return false
		}
		return true
	}

	agentMarkers := []string{
		"/aiserver.v1.bidiservice/",
		"/aiserver.v1.chatservice/",
		"/aiserver.v1.agentservice/",
		"/aiserver.v1.conversationservice/",
		"/agent.v1.agentservice/",
		"bidiappend",
		"bidipoll",
		"streamunifiedchat",
		"streamchat",
		"getchat",
		"runsse",
		"runagent",
		"agentrun",
	}
	for _, m := range agentMarkers {
		if strings.Contains(lower, m) {
			return true
		}
	}
	return false
}

// Non-chat Cursor control / telemetry / plugin RPCs — skip quietly.
func isCursorControlPath(path string) bool {
	lower := strings.ToLower(path)
	controlMarkers := []string{
		"/aiserver.v1.dashboardservice/",
		"/aiserver.v1.analyticsservice/",
		"/aiserver.v1.backgroundcomposerservice/",
		"/aiserver.v1.repositoryservice/",
		"/aiserver.v1.authservice/",
		"/aiserver.v1.metricsservice/",
		"/aiserver.v1.telemetryservice/",
		"/aiserver.v1.cppservice/",
		"/aiserver.v1.fileservice/",
		"/aiserver.v1.cmdkservice/",
		"geteffectiveuserplugins",
		"listbackgroundcomposers",
		"report",
		"heartbeat",
	}
	for _, m := range controlMarkers {
		if strings.Contains(lower, m) {
			return true
		}
	}
	// Any remaining aiserver.* RPC that is not agent_rpc → control-ish.
	if strings.Contains(lower, "/aiserver.") {
		return true
	}
	return false
}
