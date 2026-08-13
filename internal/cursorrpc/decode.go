package cursorrpc

import (
	"fmt"
	"strings"

	"github.com/glider-ai/glider/internal/backend"
)

// ChatKind identifies which aiserver chat RPC this code decoded.
type ChatKind int

const (
	ChatUnknown        ChatKind = iota
	ChatStreamChat              // GetChatRequest → StreamChatResponse
	ChatStreamComposer          // GetComposerChatRequest → StreamChatResponse
)

func (k ChatKind) String() string {
	switch k {
	case ChatStreamChat:
		return "stream_chat"
	case ChatStreamComposer:
		return "stream_composer"
	default:
		return "unknown"
	}
}

// DecodedChat is a harness-ready view of a Cursor Agent/chat Connect request.
type DecodedChat struct {
	Kind     ChatKind
	Request  *backend.CompletionRequest
	RawModel string
	RPCPath  string
}

// Fulfillable reports whether this package can make a Connect response that
// Cursor accepts.
func (d *DecodedChat) Fulfillable() bool {
	return d != nil && (d.Kind == ChatStreamChat || d.Kind == ChatStreamComposer)
}

// DecodeChatRequest tries to parse a known AiService chat/composer body.
// Returns (nil, nil) when the path is agent_rpc but not a decodeable chat shape
// (e.g. BidiAppend) — caller should origin-passthrough.
func DecodeChatRequest(path string, body []byte) (*DecodedChat, error) {
	path = stripQuery(path)
	lower := strings.ToLower(path)

	switch {
	case strings.Contains(lower, "/aiserver.v1.aiservice/streamchat"),
		strings.Contains(lower, "/aiserver.v1.aiservice/streamchatweb"),
		strings.Contains(lower, "/aiserver.v1.aiservice/streamchattryreallyhard"),
		strings.Contains(lower, "/aiserver.v1.aiservice/streamnotepadchat"):
		msg, err := decodeChatBody(body, parseGetChatRequest)
		if err != nil {
			return nil, fmt.Errorf("decode GetChatRequest: %w", err)
		}
		return fromGetChat(path, ChatStreamChat, msg), nil

	case strings.Contains(lower, "/aiserver.v1.aiservice/streamcomposer"):
		// Exclude StreamComposerContext which has a different request type.
		if strings.Contains(lower, "streamcomposercontext") {
			return nil, nil
		}
		msg, err := decodeChatBody(body, parseGetComposerChatRequest)
		if err != nil {
			return nil, fmt.Errorf("decode GetComposerChatRequest: %w", err)
		}
		return fromComposer(path, msg), nil

	default:
		// BidiService, ChatService (newer), toolformer, etc. — not in this schema pass.
		return nil, nil
	}
}

// IsFulfillableAgentPath reports each path that this package can decode, and
// then fulfill with a local model from one end to the other.
func IsFulfillableAgentPath(path string) bool {
	lower := strings.ToLower(stripQuery(path))
	markers := []string{
		"/aiserver.v1.aiservice/streamchat",
		"/aiserver.v1.aiservice/streamchatweb",
		"/aiserver.v1.aiservice/streamchattryreallyhard",
		"/aiserver.v1.aiservice/streamnotepadchat",
		"/aiserver.v1.aiservice/streamcomposer",
	}
	for _, m := range markers {
		if strings.Contains(lower, m) {
			if m == "/aiserver.v1.aiservice/streamcomposer" && strings.Contains(lower, "streamcomposercontext") {
				return false
			}
			return true
		}
	}
	return false
}

func fromGetChat(path string, kind ChatKind, msg *wireChatRequest) *DecodedChat {
	req := &backend.CompletionRequest{
		Model:    msg.ModelName,
		Messages: conversationToMessages(msg.Conversation),
		Stream:   true, // Cursor chat RPCs are server-streaming
	}
	if msg.ExplicitContext != "" {
		req.Messages = append([]backend.Message{{
			Role:    "system",
			Content: msg.ExplicitContext,
		}}, req.Messages...)
	}
	if cf := msg.CurrentFile; cf != nil {
		if cf.Contents != "" || cf.RelativeWorkspacePath != "" {
			snippet := cf.Contents
			if len(snippet) > 4000 {
				snippet = snippet[:4000] + "\n…[truncated]"
			}
			req.Messages = append([]backend.Message{{
				Role: "system",
				Content: fmt.Sprintf("Current file: %s\n%s",
					cf.RelativeWorkspacePath, snippet),
			}}, req.Messages...)
		}
	}
	if msg.RequestID != "" {
		req.Metadata.RequestID = msg.RequestID
	}
	return &DecodedChat{Kind: kind, Request: req, RawModel: msg.ModelName, RPCPath: path}
}

func fromComposer(path string, msg *wireChatRequest) *DecodedChat {
	req := &backend.CompletionRequest{
		Model:    msg.ModelName,
		Messages: conversationToMessages(msg.Conversation),
		Stream:   true,
	}
	if msg.ExplicitContext != "" {
		req.Messages = append([]backend.Message{{
			Role:    "system",
			Content: msg.ExplicitContext,
		}}, req.Messages...)
	}
	return &DecodedChat{Kind: ChatStreamComposer, Request: req, RawModel: msg.ModelName, RPCPath: path}
}

func conversationToMessages(conv []wireConversationMessage) []backend.Message {
	out := make([]backend.Message, 0, len(conv))
	for _, m := range conv {
		if m.Text == "" {
			continue
		}
		role := "user"
		if m.Type == convTypeAI {
			role = "assistant"
		}
		out = append(out, backend.Message{Role: role, Content: m.Text})
	}
	return out
}

func stripQuery(path string) string {
	if i := strings.IndexByte(path, '?'); i >= 0 {
		return path[:i]
	}
	return path
}
