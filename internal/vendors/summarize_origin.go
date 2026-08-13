package vendors

import (
	"context"
	"fmt"
	"strings"
)

// OriginSummarizer makes a summary. It runs an agent CLI that a person
// installed, with no console. This is the same mechanism that delegation
// already uses, and it is the same code.
//
// This is the objective of the full chain of preference. The user already pays
// for these CLIs. To spend the separate API credits of that user on the
// bookkeeping of Glider is the incorrect default. To run the CLI that the user
// already has costs that user nothing more.
//
// Know what this does NOT do: it does not use the credentials that Glider saw
// in intercepted traffic to make a request of its own.
//
// Glider has never made an upstream call with the authorization of a different
// party. passthroughHTTPS only sends a request that its owner already made. §8
// of the high-level design gives that exact pattern as the risk with the terms
// of service.
//
// To start the CLI as a subprocess reaches the same subscription, through the
// door that the vendor gives.
type OriginSummarizer struct {
	// Registry supplies the installed CLIs.
	Registry func() Registry
	// Vendor pins which CLI to use. Empty picks the first enabled one that
	// has a usable headless template.
	Vendor string
}

// Summarize runs the chosen CLI on the summary prompt and returns its answer.
func (o OriginSummarizer) Summarize(ctx context.Context, text string, maxChars int) (string, error) {
	if o.Registry == nil {
		return "", fmt.Errorf("vendors: origin summarizer has no registry")
	}
	reg := o.Registry()

	v, tmpl, err := o.pick(reg)
	if err != nil {
		return "", err
	}

	// No ContextPack: this is Glider's own bookkeeping, not a delegated user
	// task. Handing it session history would be circular — the thing being
	// summarized would be fed back in as context for summarizing it.
	res, err := RunWithOptions(ctx, v, text, RunOptions{Template: tmpl})
	if err != nil {
		return "", fmt.Errorf("vendors: origin summarizer (%s:%s): %w", v.Name, tmpl, err)
	}
	out := strings.TrimSpace(res.Text)
	if out == "" {
		return "", fmt.Errorf("vendors: origin summarizer (%s) produced no output", v.Name)
	}
	return truncateMiddleOut(out, maxChars), nil
}

// pick chooses a vendor and a HEADLESS template.
//
// Mode matters here more than anywhere else: an interactive template would
// open a console window on the user's desktop, unprompted, because a
// background bookkeeping task decided to tidy a file. agy's default template
// is interactive, so "first enabled vendor, default template" would do
// exactly that. Only headless templates are eligible.
func (o OriginSummarizer) pick(reg Registry) (Vendor, string, error) {
	consider := reg.Enabled()
	if o.Vendor != "" {
		v, ok := reg.Find(o.Vendor)
		if !ok || !v.Enabled {
			return Vendor{}, "", fmt.Errorf("vendors: summary origin_vendor %q is not registered or not enabled", o.Vendor)
		}
		consider = []Vendor{v}
	}
	for _, v := range consider {
		for _, name := range []string{"default", "headless"} {
			t, ok := v.ResolveTemplate(name)
			if !ok || t.Mode == "interactive" {
				continue
			}
			if templateNeedsSessionID(t.Args) {
				continue // a resume shape cannot run standalone
			}
			return v, name, nil
		}
	}
	return Vendor{}, "", fmt.Errorf("vendors: no enabled CLI has a headless template to summarize with")
}
