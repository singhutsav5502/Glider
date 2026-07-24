package swarm

import "testing"

func TestResolveInferenceRoute(t *testing.T) {
	cases := []struct {
		route, want string
		prefer      bool
	}{
		{"local", "local", false},
		{"cloud", "cloud", false},
		{"auto", "auto", true},
		{"", "local", true},
		{"", "auto", false},
		{" LOCAL ", "local", false},
	}
	for _, c := range cases {
		got := ResolveInferenceRoute(c.route, c.prefer)
		if got != c.want {
			t.Fatalf("ResolveInferenceRoute(%q,%v)=%q want %q", c.route, c.prefer, got, c.want)
		}
	}
}

func TestApplyInferenceRoute(t *testing.T) {
	if got := ApplyInferenceRoute("hello", "local"); got != "/local hello" {
		t.Fatalf("local: %q", got)
	}
	if got := ApplyInferenceRoute("hello", "cloud"); got != "/cloud hello" {
		t.Fatalf("cloud: %q", got)
	}
	if got := ApplyInferenceRoute("hello", "auto"); got != "hello" {
		t.Fatalf("auto: %q", got)
	}
	if got := ApplyInferenceRoute("/local already", "cloud"); got != "/local already" {
		t.Fatalf("keep existing prefix: %q", got)
	}
}
