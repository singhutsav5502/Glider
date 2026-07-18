package mitm_test

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/glider-ai/glider/internal/mitm"
)

func TestRedactAuthorization(t *testing.T) {
	h := http.Header{}
	h.Set("Authorization", "Bearer supersecrettokenvalue99")
	h.Set("Content-Type", "application/connect+proto")
	h.Set("X-Cursor-Checksum", "abcdefghijklmnop")
	h.Set("X-Api-Key", "sk-live-abcdef")
	h.Set("Cookie", "session=verysecretcookie")

	out := mitm.RedactHeaders(h)
	auth := out["Authorization"]
	if strings.Contains(auth, "supersecrettokenvalue99") {
		t.Fatalf("authorization not redacted: %q", auth)
	}
	if !strings.Contains(auth, "Bearer ") || !strings.Contains(auth, "[len=") {
		t.Fatalf("expected bearer redaction with length, got %q", auth)
	}
	if out["Content-Type"] != "application/connect+proto" {
		t.Fatalf("content-type should pass through: %q", out["Content-Type"])
	}
	if strings.Contains(out["X-Cursor-Checksum"], "abcdefghijklmnop") {
		t.Fatalf("checksum should be redacted: %q", out["X-Cursor-Checksum"])
	}
	if strings.Contains(out["Cookie"], "verysecretcookie") {
		t.Fatalf("cookie should be redacted: %q", out["Cookie"])
	}
}

func TestScanConnectFrames(t *testing.T) {
	// Two frames: flags=0 size=3 "abc"; flags=2 (end) size=2 "{}"
	body := []byte{
		0x00, 0x00, 0x00, 0x00, 0x03, 'a', 'b', 'c',
		0x02, 0x00, 0x00, 0x00, 0x02, '{', '}',
	}
	frames := mitm.ScanConnectFrames(body)
	if len(frames) != 2 {
		t.Fatalf("frames=%d want 2", len(frames))
	}
	if frames[0].Size != 3 || frames[0].Flags != 0 {
		t.Fatalf("frame0=%+v", frames[0])
	}
	if frames[1].Size != 2 || frames[1].Flags != 2 {
		t.Fatalf("frame1=%+v", frames[1])
	}
}

func TestAgentRPCDebuggerGatedOff(t *testing.T) {
	d := &mitm.AgentRPCDebugger{Enabled: false, DumpDir: t.TempDir()}
	req := httptest.NewRequest(http.MethodPost, "https://api2.cursor.sh/aiserver.v1.BidiService/BidiAppend", nil)
	if obs := d.Observe(req, []byte{1, 2, 3}, "opaque"); obs != nil {
		t.Fatal("disabled debugger must not observe")
	}
	if len(d.Recent(10)) != 0 {
		t.Fatal("ring should be empty when disabled")
	}
}

func TestAgentRPCDebuggerDumpAndRing(t *testing.T) {
	dir := t.TempDir()
	d := &mitm.AgentRPCDebugger{
		Enabled:  true,
		DumpDir:  dir,
		RingSize: 8,
		PreviewN: 16,
	}
	body := []byte{
		0x00, 0x00, 0x00, 0x00, 0x04, 't', 'e', 's', 't',
	}
	req := httptest.NewRequest(http.MethodPost,
		"https://agent.api5.cursor.sh/aiserver.v1.BidiService/BidiAppend",
		nil)
	req.Header.Set("Content-Type", "application/connect+proto")
	req.Header.Set("Connect-Protocol-Version", "1")
	req.Header.Set("Authorization", "Bearer real-secret-token-do-not-leak")
	req.Header.Set("X-Request-Id", "req-123")
	req = req.WithContext(mitm.WithConnectSession(req.Context(), "cdeadbeef"))

	obs := d.Observe(req, body, "agent_rpc_opaque")
	if obs == nil {
		t.Fatal("expected observation")
	}
	if obs.Kind != "agent_rpc_opaque" {
		t.Fatalf("kind=%s", obs.Kind)
	}
	if obs.ConnectSession != "cdeadbeef" {
		t.Fatalf("session=%s", obs.ConnectSession)
	}
	if obs.FrameCount != 1 || obs.FrameSizes[0] != 4 {
		t.Fatalf("frames=%+v", obs)
	}
	if obs.DumpFile == "" {
		t.Fatal("expected dump file")
	}
	data, err := os.ReadFile(obs.DumpFile)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if strings.Contains(text, "real-secret-token-do-not-leak") {
		t.Fatal("dump leaked bearer token")
	}
	if !strings.Contains(text, "Bearer ") || !strings.Contains(text, "[len=") {
		t.Fatalf("expected redacted auth in dump:\n%s", text)
	}
	if !strings.Contains(text, "connect_session=cdeadbeef") {
		t.Fatalf("missing session in dump:\n%s", text)
	}

	recent := d.Recent(5)
	if len(recent) != 1 {
		t.Fatalf("recent=%d", len(recent))
	}
	counts := d.PathCounts()
	key := "agent_rpc_opaque|/aiserver.v1.BidiService/BidiAppend"
	if counts[key] != 1 {
		t.Fatalf("path counts=%v", counts)
	}

	// Ensure dump dir was created under our temp (not home).
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) == 0 {
		t.Fatalf("dump dir empty: %v", err)
	}
	if filepath.Dir(obs.DumpFile) != dir {
		t.Fatalf("dump not in temp dir: %s", obs.DumpFile)
	}
}

func TestClassifyDebugKind(t *testing.T) {
	if got := mitm.ClassifyDebugKind("/aiserver.v1.BidiService/BidiAppend"); got != "agent_rpc_opaque" {
		t.Fatal(got)
	}
	if got := mitm.ClassifyDebugKind("/aiserver.v1.AiService/StreamChat"); got != "agent_rpc_fulfillable" {
		t.Fatal(got)
	}
	if got := mitm.ClassifyDebugKind("/agent.v1.AgentService/RunSSE"); got != "agent_rpc_opaque" {
		t.Fatal(got)
	}
	if got := mitm.ClassifyDebugKind("/v1/chat/completions"); got != "openai_compat" {
		t.Fatal(got)
	}
	if got := mitm.ClassifyDebugKind("/aiserver.v1.DashboardService/X"); got != "cursor_control" {
		t.Fatal(got)
	}
}

func TestSummarizeProtoWire(t *testing.T) {
	// field1 ld("hi"), field2 varint 7
	body := []byte{0x0a, 0x02, 'h', 'i', 0x10, 0x07}
	got := mitm.SummarizeProtoWire(body, 8)
	if got != "1:ld(2),2:varint" {
		t.Fatalf("got %q", got)
	}
}

func TestAgentRPCDebuggerSkipsControlDumpsByDefault(t *testing.T) {
	dir := t.TempDir()
	d := &mitm.AgentRPCDebugger{Enabled: true, DumpDir: dir, RingSize: 8}
	req := httptest.NewRequest(http.MethodPost,
		"https://api2.cursor.sh/aiserver.v1.AnalyticsService/SubmitLogs",
		nil)
	req.Header.Set("Content-Type", "application/proto")
	obs := d.Observe(req, []byte{0x1f, 0x8b}, "skip_control")
	if obs == nil {
		t.Fatal("expected observation")
	}
	if obs.Kind != "cursor_control" {
		t.Fatalf("kind=%s", obs.Kind)
	}
	if obs.DumpFile != "" {
		t.Fatalf("control should not dump by default: %s", obs.DumpFile)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("dump dir should be empty, got %d", len(entries))
	}
	// Still in ring for /api/mitm/debug/recent
	if len(d.Recent(5)) != 1 {
		t.Fatal("ring should keep control observations")
	}

	d.DumpControl = true
	obs2 := d.Observe(req, []byte{0x0a, 0x01, 'x'}, "skip_control")
	if obs2.DumpFile == "" {
		t.Fatal("DumpControl=true should write dump")
	}
}

func TestInterceptorDebugObserveOpaque(t *testing.T) {
	dir := t.TempDir()
	dbg := &mitm.AgentRPCDebugger{Enabled: true, DumpDir: dir, RingSize: 4}
	inter := &mitm.Interceptor{
		Harness: stubHarness{},
		Debug:   dbg,
	}
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost,
		"https://api2.cursor.sh/aiserver.v1.ChatService/StreamUnifiedChatWithTools",
		strings.NewReader("\x00\x00\x00\x00\x02{}"))
	req.Header.Set("Content-Type", "application/connect+proto")
	handled, err := inter.TryHandle(rr, req)
	if err != nil {
		t.Fatal(err)
	}
	if handled {
		t.Fatal("must passthrough")
	}
	if len(dbg.Recent(10)) != 1 {
		t.Fatalf("want 1 observation, got %d", len(dbg.Recent(10)))
	}
	if dbg.Recent(1)[0].Outcome != "agent_rpc_opaque" {
		t.Fatalf("outcome=%s", dbg.Recent(1)[0].Outcome)
	}
}
