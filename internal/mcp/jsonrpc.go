package mcp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

const (
	jsonRPCVersion   = "2.0"
	protocolVersion  = "2024-11-05" // widely supported; servers negotiate
	clientName       = "glider"
	clientVersion    = "0.1.0"
)

// jsonRPCRequest is a JSON-RPC 2.0 request.
type jsonRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      any             `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// jsonRPCNotification is a JSON-RPC 2.0 notification (no id).
type jsonRPCNotification struct {
	JSONRPC string          `json:"jsonrpc"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// jsonRPCResponse is a JSON-RPC 2.0 response.
type jsonRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      any             `json:"id,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *jsonRPCError   `json:"error,omitempty"`
}

type jsonRPCError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *jsonRPCError) Error() string {
	if e == nil {
		return ""
	}
	return fmt.Sprintf("mcp rpc %d: %s", e.Code, e.Message)
}

func encodeRequest(id any, method string, params any) ([]byte, error) {
	var raw json.RawMessage
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			return nil, err
		}
		raw = b
	}
	return json.Marshal(jsonRPCRequest{
		JSONRPC: jsonRPCVersion,
		ID:      id,
		Method:  method,
		Params:  raw,
	})
}

func encodeNotification(method string, params any) ([]byte, error) {
	var raw json.RawMessage
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			return nil, err
		}
		raw = b
	}
	return json.Marshal(jsonRPCNotification{
		JSONRPC: jsonRPCVersion,
		Method:  method,
		Params:  raw,
	})
}

// readJSONLine reads one newline-delimited JSON object from r.
func readJSONLine(r io.Reader, buf *bytes.Buffer) ([]byte, error) {
	tmp := make([]byte, 1)
	for {
		n, err := r.Read(tmp)
		if n > 0 {
			if tmp[0] == '\n' {
				line := bytes.TrimSpace(buf.Bytes())
				buf.Reset()
				if len(line) == 0 {
					continue
				}
				return append([]byte(nil), line...), nil
			}
			buf.WriteByte(tmp[0])
			if buf.Len() > 16<<20 {
				return nil, fmt.Errorf("mcp: message too large")
			}
			continue
		}
		if err != nil {
			if err == io.EOF && buf.Len() > 0 {
				line := bytes.TrimSpace(buf.Bytes())
				buf.Reset()
				if len(line) == 0 {
					return nil, io.EOF
				}
				return append([]byte(nil), line...), nil
			}
			return nil, err
		}
	}
}

func parseContentText(result json.RawMessage) (string, bool, error) {
	if len(result) == 0 {
		return "", false, nil
	}
	var envelope struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	if err := json.Unmarshal(result, &envelope); err != nil {
		// Fallback: treat whole result as text.
		return string(result), false, nil
	}
	var b strings.Builder
	for i, c := range envelope.Content {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(c.Text)
	}
	if b.Len() == 0 {
		return string(result), envelope.IsError, nil
	}
	return b.String(), envelope.IsError, nil
}

func defaultInitializeParams() map[string]any {
	return map[string]any{
		"protocolVersion": protocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo": map[string]any{
			"name":    clientName,
			"version": clientVersion,
		},
	}
}
