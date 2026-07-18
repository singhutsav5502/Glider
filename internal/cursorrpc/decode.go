package cursorrpc

import (
	"fmt"
	"strings"

	aiserverv1 "github.com/everestmz/cursor-rpc/cursor/gen/aiserver/v1"
	"github.com/glider-ai/glider/internal/backend"
)

// ChatKind identifies which aiserver chat RPC we decoded.
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

// Fulfillable reports whether we can encode a Connect response Cursor accepts.
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
		var msg aiserverv1.GetChatRequest
		if err := UnmarshalProto(body, &msg); err != nil {
			return nil, fmt.Errorf("decode GetChatRequest: %w", err)
		}
		return fromGetChat(path, ChatStreamChat, &msg), nil

	case strings.Contains(lower, "/aiserver.v1.aiservice/streamcomposer"):
		// Exclude StreamComposerContext which has a different request type.
		if strings.Contains(lower, "streamcomposercontext") {
			return nil, nil
		}
		var msg aiserverv1.GetComposerChatRequest
		if err := UnmarshalProto(body, &msg); err != nil {
			return nil, fmt.Errorf("decode GetComposerChatRequest: %w", err)
		}
		return fromComposer(path, &msg), nil

	default:
		// BidiService, ChatService (newer), toolformer, etc. — not in this schema pass.
		return nil, nil
	}
}

// IsFulfillableAgentPath reports paths we can decode + locally fulfill end-to-end.
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

func fromGetChat(path string, kind ChatKind, msg *aiserverv1.GetChatRequest) *DecodedChat {
	model := modelName(msg.GetModelDetails())
	req := &backend.CompletionRequest{
		Model:    model,
		Messages: conversationToMessages(msg.GetConversation()),
		Stream:   true, // Cursor chat RPCs are server-streaming
	}
	if msg.GetExplicitContext() != nil && msg.GetExplicitContext().GetContext() != "" {
		req.Messages = append([]backend.Message{{
			Role:    "system",
			Content: msg.GetExplicitContext().GetContext(),
		}}, req.Messages...)
	}
	if msg.GetCurrentFile() != nil {
		cf := msg.GetCurrentFile()
		if cf.GetContents() != "" || cf.GetRelativeWorkspacePath() != "" {
			snippet := cf.GetContents()
			if len(snippet) > 4000 {
				snippet = snippet[:4000] + "\n…[truncated]"
			}
			req.Messages = append([]backend.Message{{
				Role: "system",
				Content: fmt.Sprintf("Current file: %s\n%s",
					cf.GetRelativeWorkspacePath(), snippet),
			}}, req.Messages...)
		}
	}
	if msg.GetRequestId() != "" {
		req.Metadata.RequestID = msg.GetRequestId()
	}
	return &DecodedChat{Kind: kind, Request: req, RawModel: model, RPCPath: path}
}

func fromComposer(path string, msg *aiserverv1.GetComposerChatRequest) *DecodedChat {
	model := modelName(msg.GetModelDetails())
	req := &backend.CompletionRequest{
		Model:    model,
		Messages: conversationToMessages(msg.GetConversation()),
		Stream:   true,
	}
	if msg.GetExplicitContext() != nil && msg.GetExplicitContext().GetContext() != "" {
		req.Messages = append([]backend.Message{{
			Role:    "system",
			Content: msg.GetExplicitContext().GetContext(),
		}}, req.Messages...)
	}
	return &DecodedChat{Kind: ChatStreamComposer, Request: req, RawModel: model, RPCPath: path}
}

func modelName(md *aiserverv1.ModelDetails) string {
	if md == nil {
		return ""
	}
	if n := md.GetModelName(); n != "" {
		return n
	}
	return ""
}

func conversationToMessages(conv []*aiserverv1.ConversationMessage) []backend.Message {
	out := make([]backend.Message, 0, len(conv))
	for _, m := range conv {
		if m == nil {
			continue
		}
		role := "user"
		switch m.GetType() {
		case aiserverv1.ConversationMessage_MESSAGE_TYPE_AI:
			role = "assistant"
		case aiserverv1.ConversationMessage_MESSAGE_TYPE_HUMAN:
			role = "user"
		default:
			if m.GetText() == "" {
				continue
			}
		}
		text := m.GetText()
		if text == "" {
			continue
		}
		out = append(out, backend.Message{Role: role, Content: text})
	}
	return out
}

func stripQuery(path string) string {
	if i := strings.IndexByte(path, '?'); i >= 0 {
		return path[:i]
	}
	return path
}
