package router

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/glider-ai/glider/internal/backend"
	"github.com/glider-ai/glider/internal/config"
)

// Built-in MVP patterns when routing.task_classifier.*_patterns are empty.
var (
	defaultMustCloudPatterns = []string{
		`(?i)\b(architect(ure|ing)?|redesign|re-?architect)\b`,
		`(?i)\b(migrat(e|ion)|from scratch|greenfield)\b`,
		`(?i)\b(security (audit|review)|threat model)\b`,
		`(?i)\b(multi-?file|across (the )?(codebase|repo|repository|project)|entire (codebase|repo))\b`,
		`(?i)\b(plan (then|and) (implement|code|edit)|explore (then|and) (edit|fix|implement))\b`,
		`(?i)\b(large refactor|major refactor|rewrite (the )?(module|package|system))\b`,
	}
	defaultSmallLocalPatterns = []string{
		`(?i)\b(rename|find and replace|change (the )?name of)\b`,
		`(?i)\b(explain|what does (this|the)|summarize|summary of)\b`,
		`(?i)\b(format|prettier|fix lint|gofmt|add (a )?docstring|add comments?)\b`,
		`(?i)\b(how do i|what is|simple (question|qa)|quick question)\b`,
	}
)

const (
	defaultToolsPriority      = 85
	defaultMustCloudPriority  = 80
	defaultSmallLocalPriority = 70
)

// TaskClassRule matches prompt heuristics or tool presence (see planning/routing_and_context.md).
type TaskClassRule struct {
	name         string
	priority     int
	kind         taskClassKind
	patterns     []*regexp.Regexp
	action       config.ActionConfig
	toolFollowup config.ToolFollowupConfig
}

type taskClassKind int

const (
	taskClassToolsCloud taskClassKind = iota
	taskClassMustCloud
	taskClassSmallLocal
)

// NewTaskClassifierRules builds the injected rules for routing.task_classifier.
// Order of evaluation is by Priority() (higher first): tools → must_cloud → small_local.
// When toolFollowup allows all tools locally, the tools→cloud rule does not match
// so Path A can fulfill tool-capable requests on local backends.
func NewTaskClassifierRules(tc config.TaskClassifierConfig, toolFollowup config.ToolFollowupConfig) ([]Rule, error) {
	if !tc.Enabled {
		return nil, nil
	}
	localModel := strings.TrimSpace(tc.LocalModel)
	if localModel == "" {
		localModel = "qwen2.5-coder:14b"
	}
	cloudBackend := strings.TrimSpace(tc.CloudBackend)
	if cloudBackend == "" {
		cloudBackend = "openai"
	}
	cloudModel := strings.TrimSpace(tc.CloudModel)
	if cloudModel == "" {
		cloudModel = "gpt-4o"
	}
	toolsPri := tc.ToolsPriority
	if toolsPri == 0 {
		toolsPri = defaultToolsPriority
	}
	mustPri := tc.MustCloudPriority
	if mustPri == 0 {
		mustPri = defaultMustCloudPriority
	}
	smallPri := tc.SmallLocalPriority
	if smallPri == 0 {
		smallPri = defaultSmallLocalPriority
	}

	var out []Rule
	if tc.ToolsForceCloudOrDefault() {
		out = append(out, &TaskClassRule{
			name:         "Task Classifier Tools→Cloud",
			priority:     toolsPri,
			kind:         taskClassToolsCloud,
			toolFollowup: toolFollowup,
			action: config.ActionConfig{
				Target:  "cloud",
				Backend: cloudBackend,
				Model:   cloudModel,
			},
		})
	}

	mustPatterns := tc.MustCloudPatterns
	if len(mustPatterns) == 0 {
		mustPatterns = defaultMustCloudPatterns
	}
	mustRes, err := compilePatterns(mustPatterns)
	if err != nil {
		return nil, fmt.Errorf("task_classifier must_cloud_patterns: %w", err)
	}
	out = append(out, &TaskClassRule{
		name:     "Task Classifier Must-Cloud",
		priority: mustPri,
		kind:     taskClassMustCloud,
		patterns: mustRes,
		action: config.ActionConfig{
			Target:  "cloud",
			Backend: cloudBackend,
			Model:   cloudModel,
		},
	})

	smallPatterns := tc.SmallLocalPatterns
	if len(smallPatterns) == 0 {
		smallPatterns = defaultSmallLocalPatterns
	}
	smallRes, err := compilePatterns(smallPatterns)
	if err != nil {
		return nil, fmt.Errorf("task_classifier small_local_patterns: %w", err)
	}
	out = append(out, &TaskClassRule{
		name:     "Task Classifier Small-Local",
		priority: smallPri,
		kind:     taskClassSmallLocal,
		patterns: smallRes,
		action: config.ActionConfig{
			Target: "local",
			Model:  localModel,
		},
	})
	return out, nil
}

func compilePatterns(patterns []string) ([]*regexp.Regexp, error) {
	out := make([]*regexp.Regexp, 0, len(patterns))
	for i, p := range patterns {
		re, err := regexp.Compile(p)
		if err != nil {
			return nil, fmt.Errorf("pattern[%d]: %w", i, err)
		}
		out = append(out, re)
	}
	return out, nil
}

func (r *TaskClassRule) Name() string  { return r.name }
func (r *TaskClassRule) Priority() int { return r.priority }

func (r *TaskClassRule) Evaluate(_ context.Context, req *backend.CompletionRequest) (*RuleResult, error) {
	switch r.kind {
	case taskClassToolsCloud:
		if req != nil && req.HasTools() {
			// Path A: allowlisted-only tool sets may stay local (tool_followup).
			if ToolsAllLocalAllowed(r.toolFollowup, req.ToolNames()) {
				return &RuleResult{Matched: false}, nil
			}
			return &RuleResult{
				Matched: true,
				Action:  withRole(withReason(actionToDecision(r.name, r.action), "tools_present"), "exec"),
			}, nil
		}
	case taskClassMustCloud, taskClassSmallLocal:
		content := allMessageContent(req)
		for _, re := range r.patterns {
			if re.MatchString(content) {
				reason := "must_cloud"
				role := "plan"
				if r.kind == taskClassSmallLocal {
					reason = "small_offload"
					role = InferTaskRole(content)
					if role == "" {
						role = "exec"
					}
				}
				d := withReason(actionToDecision(r.name, r.action), reason)
				if d != nil {
					d.Role = role
				}
				return &RuleResult{
					Matched: true,
					Action:  d,
				}, nil
			}
		}
	}
	return &RuleResult{Matched: false}, nil
}

// InferTaskRole returns plan | research | exec from prompt heuristics (empty if unclear).
func InferTaskRole(content string) string {
	lower := strings.ToLower(content)
	switch {
	case strings.Contains(lower, "architect"), strings.Contains(lower, "redesign"),
		strings.Contains(lower, "plan then"), strings.Contains(lower, "design the"):
		return "plan"
	case strings.Contains(lower, "research"), strings.Contains(lower, "survey"),
		strings.Contains(lower, "compare options"), strings.Contains(lower, "investigate"):
		return "research"
	case strings.Contains(lower, "rename"), strings.Contains(lower, "fix"),
		strings.Contains(lower, "implement"), strings.Contains(lower, "edit"),
		strings.Contains(lower, "format"), strings.Contains(lower, "explain"):
		return "exec"
	default:
		return ""
	}
}

func withReason(d *backend.RoutingDecision, reason string) *backend.RoutingDecision {
	if d != nil {
		d.Reason = reason
	}
	return d
}

func withRole(d *backend.RoutingDecision, role string) *backend.RoutingDecision {
	if d != nil {
		d.Role = role
	}
	return d
}

func allMessageContent(req *backend.CompletionRequest) string {
	if req == nil || len(req.Messages) == 0 {
		return ""
	}
	var b strings.Builder
	for i, m := range req.Messages {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(m.Content)
	}
	return b.String()
}
