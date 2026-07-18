package contextgraph_test

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/glider-ai/glider/internal/contextgraph"
)

func TestAppendAndTurnLookup(t *testing.T) {
	s := contextgraph.New("")
	s.Append(contextgraph.Event{
		Kind:      contextgraph.EventTurnOpened,
		TurnID:    "root-1",
		RequestID: "root-1",
		Actor:     "cloud",
		Attrs:     map[string]string{"route": "cloud", "source": "explicit_cloud"},
	})
	s.Append(contextgraph.Event{
		Kind:      contextgraph.EventStickyBound,
		TurnID:    "root-1",
		RequestID: "sum-2",
		Actor:     "mitm",
		Attrs:     map[string]string{"edge": "sticky_inherits"},
	})
	s.Append(contextgraph.Event{
		Kind:      contextgraph.EventSummaryRequested,
		TurnID:    "root-1",
		RequestID: "sum-2",
		Actor:     "cloud",
	})

	view, ok := s.Turn("root-1")
	if !ok {
		t.Fatal("expected turn root-1")
	}
	if view.Route != "cloud" {
		t.Fatalf("route=%q", view.Route)
	}
	if len(view.Events) < 3 {
		t.Fatalf("events=%d", len(view.Events))
	}
	if view.Stats == nil || view.Stats.EventCount < 3 {
		t.Fatalf("stats=%+v", view.Stats)
	}
	if s.TurnIDForRequest("sum-2") != "root-1" {
		t.Fatalf("child not bound: %q", s.TurnIDForRequest("sum-2"))
	}
	child, ok := s.Turn("sum-2")
	if !ok || child.ID != "root-1" {
		t.Fatalf("lookup by child request failed: ok=%v id=%q", ok, child.ID)
	}
	if !s.CloudTurnLive("sum-2") {
		t.Fatal("expected cloud turn live via child id")
	}
}

func TestRecentTurnsOrder(t *testing.T) {
	s := contextgraph.New("")
	s.Append(contextgraph.Event{Kind: contextgraph.EventTurnOpened, TurnID: "a", RequestID: "a", Attrs: map[string]string{"route": "local"}})
	time.Sleep(2 * time.Millisecond)
	s.Append(contextgraph.Event{Kind: contextgraph.EventTurnOpened, TurnID: "b", RequestID: "b", Attrs: map[string]string{"route": "cloud"}})
	recent := s.RecentTurns(10)
	if len(recent) < 2 {
		t.Fatalf("recent=%d", len(recent))
	}
	if recent[0].ID != "b" {
		t.Fatalf("want newest first, got %q", recent[0].ID)
	}
}

func TestConcurrentAppends(t *testing.T) {
	dir := t.TempDir()
	s := contextgraph.New(dir)
	const n = 200
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		i := i
		go func() {
			defer wg.Done()
			id := "req-" + string(rune('a'+(i%26))) + "-" + itoa(i)
			s.Append(contextgraph.Event{
				Kind:      contextgraph.EventBidiSeen,
				TurnID:    "turn-concurrent",
				RequestID: id,
				Actor:     "mitm",
				Attrs:     map[string]string{"i": itoa(i)},
			})
		}()
	}
	wg.Wait()
	view, ok := s.Turn("turn-concurrent")
	if !ok {
		t.Fatal("missing turn")
	}
	if len(view.Events) != n {
		t.Fatalf("events=%d want %d", len(view.Events), n)
	}
	st := s.Stats()
	if st.Events != n {
		t.Fatalf("stats.events=%d", st.Events)
	}
	// JSONL warm store written
	matches, _ := filepath.Glob(filepath.Join(dir, "events-*.jsonl"))
	if len(matches) == 0 {
		t.Fatal("expected jsonl file")
	}
	raw, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatal(err)
	}
	lines := 0
	for _, b := range raw {
		if b == '\n' {
			lines++
		}
	}
	if lines < n {
		t.Fatalf("jsonl lines=%d want >= %d", lines, n)
	}
}

func TestLiveCloudFamilyPastTTLViaOpenRun(t *testing.T) {
	s := contextgraph.New("")
	s.Grace = 5 * time.Millisecond
	s.Append(contextgraph.Event{
		Kind:      contextgraph.EventTurnOpened,
		TurnID:    "cloud-root",
		RequestID: "cloud-root",
		Attrs:     map[string]string{"route": "cloud", "source": "explicit_cloud"},
	})
	s.Append(contextgraph.Event{
		Kind:      contextgraph.EventRunSSEOpen,
		TurnID:    "cloud-root",
		RequestID: "cloud-root",
		Actor:     "mitm",
	})
	time.Sleep(10 * time.Millisecond) // past grace, but open run keeps it live
	if !s.CloudTurnLive("cloud-root") {
		t.Fatal("open RunSSE must keep cloud live past grace")
	}
	tid, src, root, ok := s.LiveCloudFamily()
	if !ok || tid != "cloud-root" || root != "cloud-root" || src != "explicit_cloud" {
		t.Fatalf("LiveCloudFamily=%q %q %q ok=%v", tid, src, root, ok)
	}
	// Summary child with new UUID resolves via newest live cloud family.
	tid2, _, root2, ok := s.ResolveCloudSticky("summary-new-uuid", "")
	if !ok || tid2 != "cloud-root" || root2 != "cloud-root" {
		t.Fatalf("ResolveCloudSticky=%q root=%q ok=%v", tid2, root2, ok)
	}
	s.Append(contextgraph.Event{
		Kind:      contextgraph.EventRunSSEClose,
		TurnID:    "cloud-root",
		RequestID: "cloud-root",
		Actor:     "mitm",
	})
	// Still within grace after close.
	if !s.CloudTurnLive("cloud-root") {
		t.Fatal("expected grace after RunSSEClose")
	}
}

func TestSessionIndex(t *testing.T) {
	s := contextgraph.New("")
	s.Append(contextgraph.Event{
		Kind:           contextgraph.EventTurnOpened,
		TurnID:         "t1",
		RequestID:      "r1",
		ConnectSession: "cs:abc",
		Attrs:          map[string]string{"route": "cloud"},
	})
	if s.TurnIDForSession("cs:abc") != "t1" {
		t.Fatalf("session map=%q", s.TurnIDForSession("cs:abc"))
	}
	view, ok := s.Turn("cs:abc")
	if !ok || view.ID != "t1" {
		t.Fatalf("lookup by session failed: ok=%v id=%q", ok, view.ID)
	}
}

func TestAllEventKindsRoundTrip(t *testing.T) {
	s := contextgraph.New("")
	kinds := []contextgraph.EventKind{
		contextgraph.EventRouteDecided,
		contextgraph.EventStickyBound,
		contextgraph.EventFulfilledLocal,
		contextgraph.EventOriginPassthrough,
		contextgraph.EventToolStarted,
		contextgraph.EventToolFinished,
		contextgraph.EventSummaryRequested,
		contextgraph.EventSubagentSpawned,
		contextgraph.EventRunSSEOpen,
		contextgraph.EventRunSSEClose,
		contextgraph.EventBidiSeen,
		contextgraph.EventError,
		contextgraph.EventTurnOpened,
	}
	for _, k := range kinds {
		s.Append(contextgraph.Event{Kind: k, TurnID: "all", RequestID: "all", Actor: "mitm"})
	}
	view, ok := s.Turn("all")
	if !ok || len(view.Events) != len(kinds) {
		t.Fatalf("ok=%v events=%d", ok, len(view.Events))
	}
	st := s.Stats()
	if st.ByKind["BidiSeen"] < 1 || st.ByKind["Error"] < 1 {
		t.Fatalf("by_kind=%v", st.ByKind)
	}
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b [16]byte
	n := len(b)
	for i > 0 {
		n--
		b[n] = byte('0' + i%10)
		i /= 10
	}
	return string(b[n:])
}
