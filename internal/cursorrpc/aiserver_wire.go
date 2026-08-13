package cursorrpc

import (
	"fmt"

	"google.golang.org/protobuf/encoding/protowire"
)

// The part of Cursor's aiserver.v1 protocol that Glider reads and answers,
// decoded from the wire directly.
//
// Glider needs very little of that protocol: the turns of the conversation,
// the model the editor asked for, two context strings, and a request id. It
// answers with one field, the text of a StreamChatResponse. Everything here
// is those fields and nothing else.
//
// The package handles the neighbouring agent.v1 protocol the same way —
// refer to EncodeAgentTextDelta in runsse_codec.go — so this is the
// established shape here rather than a new one. It also keeps the build free
// of protoc: a contributor needs the Go toolchain and nothing more.
//
// The field numbers below are the wire contract of Cursor's own service.
// They are observed facts about a protocol, in the same way a port number
// or an HTTP header name is, and they are what any client must send to be
// understood. An unknown field is skipped, so a change on Cursor's side
// adds fields here without breaking the parse.

// GetChatRequest.
const (
	fChatCurrentFile     = 1
	fChatConversation    = 2
	fChatExplicitContext = 4
	fChatModelDetails    = 7
	fChatRequestID       = 9
)

// GetComposerChatRequest. Same shapes, different numbers.
const (
	fComposerConversation    = 1
	fComposerExplicitContext = 3
	fComposerModelDetails    = 5
)

// ConversationMessage, ModelDetails, ExplicitContext, CurrentFileInfo.
const (
	fConvText            = 1
	fConvType            = 2
	fModelDetailsName    = 1
	fExplicitContextText = 1
	fCurrentFileRelPath  = 1
	fCurrentFileContents = 2
)

// StreamChatResponse, the only message Glider writes in this family.
const fStreamChatText = 1

// ConversationMessage.type.
const (
	convTypeHuman uint64 = 1
	convTypeAI    uint64 = 2
)

type wireConversationMessage struct {
	Text string
	Type uint64
}

type wireCurrentFile struct {
	RelativeWorkspacePath string
	Contents              string
}

// wireChatRequest is the union of what GetChatRequest and
// GetComposerChatRequest carry that Glider uses. The composer request has no
// current file or request id, so those stay zero for it.
type wireChatRequest struct {
	Conversation    []wireConversationMessage
	ModelName       string
	ExplicitContext string
	CurrentFile     *wireCurrentFile
	RequestID       string
}

// eachField walks the fields of one protobuf message.
//
// It is strict about framing and lenient about content: a malformed tag or
// length is an error, and a field this code does not know is skipped. That
// combination is what lets decodeChatBody below tell "this is not a protobuf
// message at all" from "this is one, with fields I ignore".
func eachField(b []byte, fn func(num protowire.Number, bytesVal []byte, varintVal uint64) error) error {
	for len(b) > 0 {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			return fmt.Errorf("cursorrpc: bad tag: %w", protowire.ParseError(n))
		}
		b = b[n:]

		switch typ {
		case protowire.BytesType:
			v, n := protowire.ConsumeBytes(b)
			if n < 0 {
				return fmt.Errorf("cursorrpc: bad length-delimited field %d: %w", num, protowire.ParseError(n))
			}
			if err := fn(num, v, 0); err != nil {
				return err
			}
			b = b[n:]
		case protowire.VarintType:
			v, n := protowire.ConsumeVarint(b)
			if n < 0 {
				return fmt.Errorf("cursorrpc: bad varint field %d: %w", num, protowire.ParseError(n))
			}
			if err := fn(num, nil, v); err != nil {
				return err
			}
			b = b[n:]
		default:
			n := protowire.ConsumeFieldValue(num, typ, b)
			if n < 0 {
				return fmt.Errorf("cursorrpc: bad field %d: %w", num, protowire.ParseError(n))
			}
			b = b[n:]
		}
	}
	return nil
}

// wireStringField reads one string field out of a nested message and ignores
// the rest. ModelDetails, ExplicitContext and the two CurrentFileInfo strings
// are all this shape.
func wireStringField(b []byte, want protowire.Number) (string, error) {
	var out string
	err := eachField(b, func(num protowire.Number, v []byte, _ uint64) error {
		if num == want && out == "" {
			out = string(v)
		}
		return nil
	})
	return out, err
}

func parseConversationMessage(b []byte) (wireConversationMessage, error) {
	var m wireConversationMessage
	err := eachField(b, func(num protowire.Number, v []byte, varint uint64) error {
		switch num {
		case fConvText:
			if m.Text == "" {
				m.Text = string(v)
			}
		case fConvType:
			m.Type = varint
		}
		return nil
	})
	return m, err
}

func parseCurrentFile(b []byte) (*wireCurrentFile, error) {
	cf := &wireCurrentFile{}
	err := eachField(b, func(num protowire.Number, v []byte, _ uint64) error {
		switch num {
		case fCurrentFileRelPath:
			if cf.RelativeWorkspacePath == "" {
				cf.RelativeWorkspacePath = string(v)
			}
		case fCurrentFileContents:
			if cf.Contents == "" {
				cf.Contents = string(v)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return cf, nil
}

// parseGetChatRequest decodes the StreamChat family request.
func parseGetChatRequest(b []byte) (*wireChatRequest, error) {
	req := &wireChatRequest{}
	err := eachField(b, func(num protowire.Number, v []byte, _ uint64) error {
		switch num {
		case fChatConversation:
			m, err := parseConversationMessage(v)
			if err != nil {
				return err
			}
			req.Conversation = append(req.Conversation, m)
		case fChatModelDetails:
			name, err := wireStringField(v, fModelDetailsName)
			if err != nil {
				return err
			}
			if req.ModelName == "" {
				req.ModelName = name
			}
		case fChatExplicitContext:
			ctx, err := wireStringField(v, fExplicitContextText)
			if err != nil {
				return err
			}
			if req.ExplicitContext == "" {
				req.ExplicitContext = ctx
			}
		case fChatCurrentFile:
			cf, err := parseCurrentFile(v)
			if err != nil {
				return err
			}
			if req.CurrentFile == nil {
				req.CurrentFile = cf
			}
		case fChatRequestID:
			if req.RequestID == "" {
				req.RequestID = string(v)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return req, nil
}

// parseGetComposerChatRequest decodes the StreamComposer request.
func parseGetComposerChatRequest(b []byte) (*wireChatRequest, error) {
	req := &wireChatRequest{}
	err := eachField(b, func(num protowire.Number, v []byte, _ uint64) error {
		switch num {
		case fComposerConversation:
			m, err := parseConversationMessage(v)
			if err != nil {
				return err
			}
			req.Conversation = append(req.Conversation, m)
		case fComposerModelDetails:
			name, err := wireStringField(v, fModelDetailsName)
			if err != nil {
				return err
			}
			if req.ModelName == "" {
				req.ModelName = name
			}
		case fComposerExplicitContext:
			ctx, err := wireStringField(v, fExplicitContextText)
			if err != nil {
				return err
			}
			if req.ExplicitContext == "" {
				req.ExplicitContext = ctx
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return req, nil
}

// EncodeStreamChatText builds a StreamChatResponse carrying one text chunk.
func EncodeStreamChatText(text string) []byte {
	return protoBytesField(fStreamChatText, []byte(text))
}

// decodeChatBody unwraps Connect framing when present and parses the result.
//
// The fallback to the raw body is kept from the version that used generated
// types: UnwrapProtoPayload uses a heuristic, and a body that only looks
// framed would otherwise be truncated before the parse. If the unwrapped
// payload does not parse and it differs from the body, the body is tried.
func decodeChatBody(body []byte, parse func([]byte) (*wireChatRequest, error)) (*wireChatRequest, error) {
	payload, err := UnwrapProtoPayload(body)
	if err != nil {
		return nil, err
	}
	req, perr := parse(payload)
	if perr == nil {
		return req, nil
	}
	if len(payload) != len(body) {
		if req2, err2 := parse(body); err2 == nil {
			return req2, nil
		}
	}
	return nil, perr
}
