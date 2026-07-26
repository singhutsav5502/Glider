package mitm

import (
	"encoding/binary"
	"fmt"
)

// peekClientHelloSNI extracts the SNI hostname from a raw TLS ClientHello
// record, without consuming it — the caller is expected to have obtained
// buf via bufio.Reader.Peek, so the same reader can still be handed to
// mitmSession/blindTunnel afterward with nothing consumed (identical to how
// handleCONNECT hands its br through unmodified after reading the CONNECT
// line off the wire, not the TLS bytes). Needed only by the transparent
// ingress: the CONNECT path gets its hostname for free from the request
// line and never needs this.
func peekClientHelloSNI(buf []byte) (string, error) {
	if len(buf) < 5 || buf[0] != 0x16 {
		return "", fmt.Errorf("mitm: not a TLS handshake record")
	}
	recLen := int(binary.BigEndian.Uint16(buf[3:5]))
	if len(buf) < 5+recLen {
		return "", fmt.Errorf("mitm: incomplete TLS record (have %d, want %d)", len(buf), 5+recLen)
	}
	hs := buf[5 : 5+recLen]

	if len(hs) < 4 || hs[0] != 0x01 {
		return "", fmt.Errorf("mitm: not a ClientHello handshake message")
	}
	hsLen := int(hs[1])<<16 | int(hs[2])<<8 | int(hs[3])
	body := hs[4:]
	if len(body) < hsLen {
		return "", fmt.Errorf("mitm: truncated ClientHello body")
	}
	body = body[:hsLen]

	// client_version(2) + random(32)
	if len(body) < 34 {
		return "", fmt.Errorf("mitm: ClientHello too short")
	}
	pos := 34

	// session_id
	if pos >= len(body) {
		return "", fmt.Errorf("mitm: ClientHello truncated at session_id")
	}
	sidLen := int(body[pos])
	pos += 1 + sidLen
	if pos > len(body) {
		return "", fmt.Errorf("mitm: ClientHello truncated after session_id")
	}

	// cipher_suites
	if pos+2 > len(body) {
		return "", fmt.Errorf("mitm: ClientHello truncated at cipher_suites")
	}
	csLen := int(binary.BigEndian.Uint16(body[pos : pos+2]))
	pos += 2 + csLen
	if pos > len(body) {
		return "", fmt.Errorf("mitm: ClientHello truncated after cipher_suites")
	}

	// compression_methods
	if pos+1 > len(body) {
		return "", fmt.Errorf("mitm: ClientHello truncated at compression_methods")
	}
	cmLen := int(body[pos])
	pos += 1 + cmLen
	if pos > len(body) {
		return "", fmt.Errorf("mitm: ClientHello truncated after compression_methods")
	}

	// extensions (may be absent entirely on a minimal ClientHello)
	if pos+2 > len(body) {
		return "", fmt.Errorf("mitm: no SNI extension present")
	}
	extTotalLen := int(binary.BigEndian.Uint16(body[pos : pos+2]))
	pos += 2
	end := pos + extTotalLen
	if end > len(body) {
		end = len(body)
	}

	for pos+4 <= end {
		extType := binary.BigEndian.Uint16(body[pos : pos+2])
		extLen := int(binary.BigEndian.Uint16(body[pos+2 : pos+4]))
		pos += 4
		if pos+extLen > len(body) {
			break
		}
		extData := body[pos : pos+extLen]
		pos += extLen

		const sniExtensionType = 0x0000
		if extType != sniExtensionType {
			continue
		}
		if len(extData) < 2 {
			continue
		}
		listLen := int(binary.BigEndian.Uint16(extData[0:2]))
		rest := extData[2:]
		if len(rest) > listLen {
			rest = rest[:listLen]
		}
		for len(rest) >= 3 {
			nameType := rest[0]
			nameLen := int(binary.BigEndian.Uint16(rest[1:3]))
			rest = rest[3:]
			if len(rest) < nameLen {
				break
			}
			name := rest[:nameLen]
			const hostNameType = 0x00
			if nameType == hostNameType {
				return string(name), nil
			}
			rest = rest[nameLen:]
		}
	}
	return "", fmt.Errorf("mitm: no SNI extension present")
}
