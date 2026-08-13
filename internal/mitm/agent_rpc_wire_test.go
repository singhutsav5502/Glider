package mitm_test

// Protobuf fixtures for Cursor's aiserver.v1 chat request, written on the
// wire rather than produced from a generated type.
//
// The interceptor under test decodes these through internal/cursorrpc, which
// also reads the wire by field number. Building the fixture the same way here
// keeps the two independent: if a field number is wrong in either place, the
// test fails instead of both agreeing on the same mistake.

func pbVarint(v uint64) []byte {
	var b []byte
	for v >= 0x80 {
		b = append(b, byte(v)|0x80)
		v >>= 7
	}
	return append(b, byte(v))
}

func pbBytesField(field int, payload []byte) []byte {
	out := pbVarint(uint64(field)<<3 | 2)
	out = append(out, pbVarint(uint64(len(payload)))...)
	return append(out, payload...)
}

func pbStringField(field int, s string) []byte { return pbBytesField(field, []byte(s)) }

func pbVarintField(field int, v uint64) []byte {
	out := pbVarint(uint64(field)<<3 | 0)
	return append(out, pbVarint(v)...)
}

// chatRequestWire builds GetChatRequest{ model_details{model_name}, conversation }.
//
// GetChatRequest: conversation = 2, model_details = 7.
// ModelDetails: model_name = 1.
// ConversationMessage: text = 1, type = 2 (1 is HUMAN).
func chatRequestWire(model, text string) []byte {
	msg := pbStringField(1, text)
	msg = append(msg, pbVarintField(2, 1)...)

	out := pbBytesField(2, msg)
	out = append(out, pbBytesField(7, pbStringField(1, model))...)
	return out
}
