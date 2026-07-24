package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

type datetimeTool struct{}

func (t *datetimeTool) Name() string        { return "datetime" }
func (t *datetimeTool) Description() string { return "Current UTC time RFC3339" }
func (t *datetimeTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{}}`)
}
func (t *datetimeTool) Call(context.Context, string, json.RawMessage) (Result, error) {
	return ok(t.Name(), time.Now().UTC().Format(time.RFC3339)), nil
}

type calculatorTool struct{}

func (t *calculatorTool) Name() string        { return "calculator" }
func (t *calculatorTool) Description() string { return "Evaluate simple a+b a-b a*b a/b expressions" }
func (t *calculatorTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"expr":{"type":"string"}}}`)
}
func (t *calculatorTool) Call(_ context.Context, input string, _ json.RawMessage) (Result, error) {
	expr := strings.ReplaceAll(strings.TrimSpace(input), " ", "")
	for _, op := range []string{"+", "-", "*", "/"} {
		if i := strings.Index(expr, op); i > 0 {
			a, err1 := strconv.ParseFloat(expr[:i], 64)
			b, err2 := strconv.ParseFloat(expr[i+1:], 64)
			if err1 != nil || err2 != nil {
				return fail(t.Name(), fmt.Errorf("parse"))
			}
			var v float64
			switch op {
			case "+":
				v = a + b
			case "-":
				v = a - b
			case "*":
				v = a * b
			case "/":
				if b == 0 {
					return fail(t.Name(), fmt.Errorf("div0"))
				}
				v = a / b
			}
			return ok(t.Name(), strconv.FormatFloat(v, 'f', -1, 64)), nil
		}
	}
	return fail(t.Name(), fmt.Errorf("unsupported expr"))
}
