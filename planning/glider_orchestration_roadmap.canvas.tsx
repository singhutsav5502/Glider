import {
  BarChart,
  Button,
  Callout,
  Card,
  CardBody,
  CardHeader,
  Code,
  CollapsibleSection,
  Divider,
  Grid,
  H1,
  H2,
  H3,
  Link,
  Pill,
  Row,
  Spacer,
  Stack,
  Stat,
  Table,
  Text,
  TodoListCard,
  UsageBar,
  useCanvasState,
  useHostTheme,
} from "cursor/canvas";

type SectionId =
  | "status"
  | "backlog"
  | "context"
  | "hotswap"
  | "concurrency"
  | "loops"
  | "slate"
  | "milestones";

const SECTIONS: { id: SectionId; label: string }[] = [
  { id: "status", label: "Status" },
  { id: "backlog", label: "Backlog" },
  { id: "context", label: "Context" },
  { id: "hotswap", label: "Hot-swap" },
  { id: "concurrency", label: "Concurrency" },
  { id: "loops", label: "Loops" },
  { id: "slate", label: "Slate map" },
  { id: "milestones", label: "Milestones" },
];

/** Honest maturity % from Glider code + planning docs (2026-07-18). Not aspirational. */
const CAPABILITY_STATUS = [
  {
    area: "Routing (explicit Ã¢â€ â€™ classifier Ã¢â€ â€™ Starlark Ã¢â€ â€™ ceiling)",
    pct: 80,
    truth: "Hard-force /cloud shipped; classifier MVP regex",
  },
  {
    area: "Path A gateway (cus- + OpenAI/Anthropic normalize)",
    pct: 90,
    truth: "Primary Agent+tools path; stream tool_calls bridge shipped",
  },
  {
    area: "Path B MITM Agent (BidiAppend Ã¢â€ â€™ RunSSE)",
    pct: 35,
    truth: "Text fulfill experimental; child/tool RunSSE Ã¢â€ â€™ origin",
  },
  {
    area: "Local tools (Path A Tools on request)",
    pct: 75,
    truth: "Attach + SSE tool_calls; no Glider-side tool runners yet",
  },
  {
    area: "Analytics LOCAL/CLOUD/CANNED %",
    pct: 95,
    truth: "Overview tile+bar; distribution + optional context_routes",
  },
  {
    area: "Orchestration 1:1 (queue, fallback, breaker, budget)",
    pct: 75,
    truth: "Production-usable single Execute path",
  },
  {
    area: "Swarms / multi-agent",
    pct: 40,
    truth: "internal/swarm FanOut+Merge+Loop+HotSwap; FanOutExecutor cancel-aware",
  },
  {
    area: "Context management (session / episodes / swarm memory)",
    pct: 35,
    truth: "contextgraph event log + turn sticky; Episode store still stub",
  },
  {
    area: "Hot-swap modules (config Watch/Swap)",
    pct: 65,
    truth: "swarm.Registry + fan_out Apply; backends/MITM/ports still restart",
  },
  {
    area: "Concurrency (fan-out, hub races, backpressure)",
    pct: 70,
    truth: "swarm.FanOut cancel + Group; orchestration.concurrency channel sizes",
  },
  {
    area: "Loop engineering (eval / babysit / recurring)",
    pct: 0,
    truth: "Documented in impl plan Ã‚Â§9.2 only",
  },
];

const BACKLOG = [
  {
    pri: "P0",
    item: "/cloud hard-force (TipTap + priority inversion)",
    status: "Done",
    depth: "MatchExplicitCommand mid-text; Engine.explicitHardOverride; Path B ArmOrigin",
    tone: "success" as const,
  },
  {
    pri: "P0",
    item: "Analytics LOCAL/CLOUD/CANNED % (origin_passthrough visible)",
    status: "Done",
    depth: "Overview LOCAL/CLOUD/CANNED + bar; GET /api/metrics distribution; optional context_routes",
    tone: "success" as const,
  },
  {
    pri: "P0",
    item: "Path A stream tool_calls Ã¢â€ â€™ Cursor SSE bridge",
    status: "Open",
    depth: "Without this, tools_force_cloud stays default; Agent+tools cannot stay local",
    tone: "danger" as const,
  },
  {
    pri: "P1",
    item: "Role-aware classifier (plan / research / exec)",
    status: "Open",
    depth: "Extend task_class.go; dashboard reason chips; metrics class rates",
    tone: "warning" as const,
  },
  {
    pri: "P1",
    item: "Episode record on local fulfill",
    status: "Open",
    depth: "1-line summary + artifacts into ~/.glider/history; Overview field",
    tone: "warning" as const,
  },
  {
    pri: "P1",
    item: "Feature-flagged FanOutExecutor (gateway, 2 workers)",`n    status: "Done",
    depth: "StrategyFanOut exists; needs VRAM BatchReserve + SSE merge",
    tone: "info" as const,
  },
  {
    pri: "P2",
    item: "Session memory + turn budgets",
    status: "Design",
    depth: "Per-session token/cost caps; checkpoint after N turns for loops",
    tone: "neutral" as const,
  },
  {
    pri: "P2",
    item: "Path B child RunSSE / tool frames",
    status: "Blocked",
    depth: "Prefer Path A first; MITM tool-loop is XL and protocol-fragile",
    tone: "danger" as const,
  },
  {
    pri: "P2",
    item: "Recurring eval loops (lint/test reflect)",
    status: "Design",
    depth: "Local re-prompt until green; Cursor sees final stream only",
    tone: "neutral" as const,
  },
  {
    pri: "P3",
    item: "Thread weaving + planner decomposition",
    status: "Aspirational",
    depth: "Slate-like orchestrator thread; Glider stays proxy Ã¢â‚¬â€ JSON/Starlark graphs",
    tone: "neutral" as const,
  },
  {
    pri: "P3",
    item: "Provider hot-swap without process restart",
    status: "Open",
    depth: "Today backends/MITM/ports require restart; Swap covers routing surface only",
    tone: "info" as const,
  },
];

const CONTEXT_LAYERS = [
  ["Layer", "Purpose", "Glider today", "Target"],
  [
    "Turn budget",
    "Cap tokens/cost per Cursor turn",
    "Rate/budget on orchestrator; max_local_context_tokens ceiling",
    "Per-session soft+hard budgets with dashboard gauges",
  ],
  [
    "Session memory",
    "Carry decisions across turns",
    "JSONL under ~/.glider/history (process-run sessions)",
    "Typed SessionState: last route, episodes[], overrides",
  ],
  [
    "Episode (swarm)",
    "Compressed worker return Ã¢â‚¬â€ not full transcript",
    "Not implemented",
    "Episode{Summary, Artifacts, Tokens, Model} Ã¢â€ â€™ orchestrator",
  ],
  [
    "Shared swarm state",
    "Avoid brittle message-pass between workers",
    "N/A (no swarm)",
    "Hub-owned scratchpad keyed by corr_id / swarm_id",
  ],
  [
    "Loop checkpoint",
    "Resume after /loop tick or CI babysit wake",
    "Not implemented",
    "Checkpoint blob: goal, last eval, next wake reason",
  ],
  [
    "Dumb-zone guard",
    "Keep working memory small (Slate lesson)",
    "Token ceiling as safety net only",
    "Episode-first; compaction last resort, never primary",
  ],
];

const HOT_SWAP_MODULES = [
  ["Module", "Swap today?", "Concurrency note", "Restart needed?"],
  [
    "Router rules / aliases / threshold / log level",
    "Yes Ã¢â‚¬â€ Provider.Swap + Watch",
    "atomic.Value store; subscribers rebuild Engine",
    "No",
  ],
  [
    "GPU assignments (vram.gpu_assignments)",
    "Yes Ã¢â‚¬â€ same Swap path",
    "Read by GET /api/vram after Swap",
    "No",
  ],
  [
    "Task classifier config block",
    "Partial Ã¢â‚¬â€ YAML reload",
    "Must not race mid-Route; snapshot at decision start",
    "No (if wired)",
  ],
  [
    "Backend clients (Ollama/vLLM/OpenAI)",
    "No live registry swap",
    "In-flight Complete must finish on old client",
    "Yes",
  ],
  [
    "MITM CA / hosts / ports",
    "No",
    "CONNECT listeners are process-bound",
    "Yes",
  ],
  [
    "Executor strategy (single Ã¢â€ â€™ fan_out)",
    "Config flag only (stub)",
    "Fan-out needs BatchReserve before spawn",
    "No once built",
  ],
  [
    "Agent / skill packages (future)",
    "Not started",
    "Load as versioned modules; pin active set per swarm",
    "Hot ideal",
  ],
];

const CONCURRENCY_ROWS = [
  ["Concern", "Current behavior", "Risk if ignored", "Design target"],
  [
    "Queue + cancellation",
    "Priority queue; ctx cancel respected (tests)",
    "Orphaned backend streams",
    "Keep; propagate cancel into fan-out children",
  ],
  [
    "Fallback / breaker",
    "FallbackChain + live reprobe",
    "Thundering herd on dead Ollama",
    "Per-backend breaker shared across swarm workers",
  ],
  [
    "/cloud vs local race",
    "Hard-force + Path B ArmOrigin before DecideLocal",
    "Overlap: canned/local + origin double-answer",
    "Single Arm* wins; RunSSE refuses local if /cloud",
  ],
  [
    "RunSSE hub (Bidi Ã¢â€ â€ RunSSE)",
    "pending/waiting maps; 800ms wait; 30s TTL GC",
    "Stale offer / wrong corr_id fulfill",
    "Strict UUID keying; metric on expire + miss",
  ],
  [
    "Fan-out backpressure",
    "Unbuilt",
    "VRAM OOM; SSE merge chaos",
    "BatchReserve all-or-nothing; bounded worker pool",
  ],
  [
    "Hot-swap mid-flight",
    "Swap notifies; in-flight keep old route snap",
    "Half-applied rules mid-request",
    "Immutable decision snapshot at Route()",
  ],
];

const SLATE_MAP = [
  ["Slate pattern", "Meaning", "Glider mapping", "Status"],
  [
    "Orchestrator thread",
    "Programs in action space; not all tactics",
    "Future local planner or Starlark+LLM Ã¢â€ â€™ SubTasks",
    "0%",
  ],
  [
    "Worker threads",
    "One bounded action then pause",
    "Short-lived Complete jobs on Ollama pool",
    "Stub",
  ],
  [
    "Episodes",
    "Compressed step history Ã¢â€ â€™ orchestrator",
    "New Episode type into history/metrics",
    "Design",
  ],
  [
    "Thread weaving",
    "Parallel workers; shared context by design",
    "FanOutExecutor + hub scratchpad + merge SSE",
    "0%",
  ],
  [
    "Model routing by role",
    "PlanÃ¢â€ â€™frontier; execÃ¢â€ â€™fast coder",
    "Aliases + role-tagged classifier",
    "Partial (aliases yes)",
  ],
  [
    "Implicit planning",
    "Research then present plan; no rigid modes",
    "Adaptive decompose; avoid fixed plannerÃ¢â€ â€™coder pipeline",
    "Design",
  ],
  [
    "Dumb-zone avoidance",
    "Small working memory via episodes",
    "Token ceiling = safety net, not primary signal",
    "Ceiling only",
  ],
  [
    "Skills packages",
    "Reusable domain instructions",
    "Optional later; Glider is harness not agent UX",
    "Out of scope now",
  ],
];

const MILESTONE_48H = [
  { id: "m0", content: "P0 done: /cloud hard-force verified on TipTap Agent turns", status: "completed" as const },
  { id: "m1", content: "Path A tool_calls stream bridge (M2 remainder) Ã¢â‚¬â€ unblock local tools", status: "in_progress" as const },
  { id: "m2", content: "Role tags on classifier + dashboard reason chips", status: "pending" as const },
  { id: "m3", content: "Episode stub written on local fulfill Ã¢â€ â€™ history API", status: "pending" as const },
  { id: "m4", content: "Flag FanOutExecutor: 2 local models, gateway-only, e2e green", status: "pending" as const },
];

const MILESTONE_2W = [
  { id: "w1", content: "SessionState + turn budgets on Overview", status: "pending" as const },
  { id: "w2", content: "Eval loop MVP: lint/test reflect before Cursor sees stream", status: "pending" as const },
  { id: "w3", content: "Babysit-style CI loop adapter (wake on check fail)", status: "pending" as const },
  { id: "w4", content: "Provider registry hot-reload (no port/MITM yet)", status: "pending" as const },
  { id: "w5", content: "Path B tools only if Path A bridge proven Ã¢â‚¬â€ else stay origin", status: "pending" as const },
  { id: "w6", content: "go test -race sign-off where CGO toolchain available", status: "pending" as const },
];

function pctTone(pct: number): "success" | "warning" | "deleted" | "info" {
  if (pct >= 75) return "success";
  if (pct >= 40) return "warning";
  if (pct > 0) return "deleted";
  return "info";
}

function SectionNav({
  active,
  onSelect,
}: {
  active: SectionId;
  onSelect: (id: SectionId) => void;
}) {
  return (
    <Row gap={6} wrap>
      {SECTIONS.map((s) => (
        <span key={s.id}>
          <Button
            variant={active === s.id ? "primary" : "secondary"}
            onClick={() => onSelect(s.id)}
          >
            {s.label}
          </Button>
        </span>
      ))}
    </Row>
  );
}

function StatusSection() {
  const theme = useHostTheme();
  const avg =
    Math.round(
      CAPABILITY_STATUS.reduce((a, c) => a + c.pct, 0) / CAPABILITY_STATUS.length,
    );

  return (
    <Stack gap={16}>
      <Callout tone="warning" title="Honest snapshot Ã¢â‚¬â€ 2026-07-18">
        Do not treat green core (Phases 1Ã¢â‚¬â€œ4) as swarm-ready.{" "}
        <Code>/cloud</Code> P0 is done. Path B text fulfill is experimental. Swarms are
        stubs (~5%). Source: STATUS.md + planning docs, verified against code surfaces.
      </Callout>

      <Grid columns={4} gap={12}>
        <Stat value="80%" label="Routing" tone="success" />
        <Stat value="35%" label="Path B Agent" tone="warning" />
        <Stat value="5%" label="Swarms" tone="danger" />
        <Stat value={`${avg}%`} label="Capability average" tone="info" />
      </Grid>

      <Card>
        <CardHeader trailing={<Pill tone="neutral" size="sm">code truth</Pill>}>
          Capability maturity
        </CardHeader>
        <CardBody>
          <Stack gap={12}>
            {CAPABILITY_STATUS.map((c) => (
              <div key={c.area}>
                <Stack gap={4}>
                <Row justify="space-between" align="center">
                  <Text weight="medium">{c.area}</Text>
                  <Pill tone={pctTone(c.pct)} size="sm">
                    {c.pct}%
                  </Pill>
                </Row>
                <UsageBar
                  total={100}
                  segments={[
                    {
                      id: c.area,
                      value: c.pct,
                      color: c.pct >= 75 ? "green" : c.pct >= 40 ? "orange" : c.pct > 0 ? "red" : "gray",
                    },
                  ]}
                  topRightLabel={`${c.pct} / 100`}
                />
                <Text size="small" tone="secondary">
                  {c.truth}
                </Text>
                </Stack>
              </div>
            ))}
          </Stack>
        </CardBody>
      </Card>

      <H3>Relative effort vs maturity</H3>
      <Text size="small" tone="secondary">
        Chart: maturity % by area (same source as table). Higher bar = more shipped.
      </Text>
      <BarChart
        categories={CAPABILITY_STATUS.map((c) =>
          c.area.length > 28 ? c.area.slice(0, 26) + "Ã¢â‚¬Â¦" : c.area,
        )}
        series={[
          {
            name: "Maturity %",
            data: CAPABILITY_STATUS.map((c) => c.pct),
            tone: "info",
          },
        ]}
        height={220}
        valueSuffix="%"
        yMax={100}
      />
      <Text size="small" tone="tertiary">
        Source: Glider repo STATUS.md + planning/*.md Ã‚Â· as of 2026-07-18 Ã‚Â· not a forecast
      </Text>

      <div
        style={{
          padding: 12,
          background: theme.fill.tertiary,
          borderRadius: 8,
        }}
      >
        <Text size="small" tone="secondary">
          Companion docs:{" "}
          <Code>planning/swarm_orchestration.md</Code>,{" "}
          <Code>planning/context_management.md</Code>,{" "}
          <Code>planning/context_and_swarm_architecture.md</Code>,{" "}
          <Code>planning/smart_routing_and_local_tools.md</Code>
        </Text>
      </div>
    </Stack>
  );
}

function BacklogSection() {
  return (
    <Stack gap={16}>
      <Text tone="secondary">
        Ordered by user-visible reliability first. Do not start Path B multi-agent or full
        thread-weaving until Path A tool bridge is green.
      </Text>
      <Table
        headers={["Pri", "Item", "Status", "In-depth notes"]}
        columnAlign={["left", "left", "left", "left"]}
        rowTone={BACKLOG.map((b) => b.tone)}
        striped
        stickyHeader
        rows={BACKLOG.map((b) => [b.pri, b.item, b.status, b.depth])}
      />
      <CollapsibleSection title="Code anchors" defaultOpen={false}>
        <Table
          framed={false}
          headers={["Piece", "Path"]}
          rows={[
            ["Hard-force explicit", "internal/router/explicit.go, router.go"],
            ["Task classifier", "internal/router/task_class.go"],
            ["Pipeline / CompleteLocal", "internal/orchestrator/pipeline.go"],
            ["Config Watch/Swap", "internal/config/watcher.go"],
            ["Path B hub", "internal/mitm/agent_fulfill_hub.go"],
            ["Strategy enum", "internal/backend/interfaces.go"],
            ["VRAM BatchReserve", "internal/vram/manager.go"],
          ]}
        />
      </CollapsibleSection>
    </Stack>
  );
}

function ContextSection() {
  return (
    <Stack gap={16}>
      <Callout tone="info" title="For-loop engineering needs checkpoints">
        Cursor-style <Code>/loop</Code> and babysit CI are recurring wakes. Glider must
        persist goal + last eval + next wake reason Ã¢â‚¬â€ not re-inflate full transcripts each
        tick. Episodes are the swarm analogue of loop checkpoints.
      </Callout>
      <Table
        headers={CONTEXT_LAYERS[0]}
        rows={CONTEXT_LAYERS.slice(1)}
        striped
        stickyHeader
      />
      <Grid columns={2} gap={12}>
        <Card>
          <CardHeader>Session memory (design)</CardHeader>
          <CardBody>
            <Stack gap={8}>
              <Text size="small">
                Key by Cursor request UUID / composer id when available; fall back to Glider
                session id from history writer.
              </Text>
              <Text size="small" tone="secondary">
                Fields: active overrides, last RoutingDecision, episode[] ring buffer (N=32),
                spend so far, loop checkpoint pointer.
              </Text>
            </Stack>
          </CardBody>
        </Card>
        <Card>
          <CardHeader>Swarm shared state (design)</CardHeader>
          <CardBody>
            <Stack gap={8}>
              <Text size="small">
                Hub owns scratchpad; workers never peer-message. Returns are Episode only Ã¢â‚¬â€
                Slate thread-weaving pattern adapted to Go channel fan-in.
              </Text>
              <Text size="small" tone="secondary">
                Cancel parent ctx Ã¢â€ â€™ cancel all workers; merge only successful episodes;
                partial failure Ã¢â€ â€™ origin or degraded single-model fallback.
              </Text>
            </Stack>
          </CardBody>
        </Card>
      </Grid>
    </Stack>
  );
}

function HotSwapSection() {
  return (
    <Stack gap={16}>
      <Text tone="secondary">
        Inspiration: Slate treats skills/models as swappable roles. Glider already has{" "}
        <Code>Provider.Watch</Code> / <Code>Swap</Code> for the routing surface Ã¢â‚¬â€ extend that
        pattern to executors and (carefully) backends.
      </Text>
      <Table
        headers={HOT_SWAP_MODULES[0]}
        rows={HOT_SWAP_MODULES.slice(1)}
        striped
        stickyHeader
      />
      <H3>Module graph (target)</H3>
      <Text size="small" tone="secondary">
        Providers, routers, and agents as independently versioned modules behind interfaces.
        Live Swap must never mutate an in-flight RoutingDecision.
      </Text>
      <Card>
        <CardBody>
          <Stack gap={6}>
            <Text weight="semibold">
              ConfigProvider Ã¢â€ â€™ RouterEngine Ã¢â€ â€™ Executor Ã¢â€ â€™ Backend
            </Text>
            <Text size="small" tone="secondary">
              Today: first three partially hot; Backend registry is startup-pinned. Swarm adds
              SwarmExecutor beside FallbackChain without rewriting PipelineCompleter.
            </Text>
          </Stack>
        </CardBody>
      </Card>
    </Stack>
  );
}

function ConcurrencySection() {
  return (
    <Stack gap={16}>
      <Callout tone="danger" title="Known race class: /cloud vs local">
        Fixed for TipTap mid-string <Code>/cloud</Code>. Path B must ArmOrigin before any
        local fulfill. Regression = double answers (canned/Ollama + origin).
      </Callout>
      <Table
        headers={CONCURRENCY_ROWS[0]}
        rows={CONCURRENCY_ROWS.slice(1)}
        striped
        stickyHeader
      />
      <Grid columns={3} gap={12}>
        <Card>
          <CardHeader>Fan-out</CardHeader>
          <CardBody>
            <Text size="small">
              Spawn N Completes under one parent ctx; BatchReserve first; merge via aggregator
              or first-valid. Gateway Mode A only until Path B hub is multi-offer aware.
            </Text>
          </CardBody>
        </Card>
        <Card>
          <CardHeader>Backpressure</CardHeader>
          <CardBody>
            <Text size="small">
              Existing priority queue is the choke point. Swarm workers must acquire queue slots
              or a dedicated swarm semaphore Ã¢â‚¬â€ never unbounded goroutines per Cursor turn.
            </Text>
          </CardBody>
        </Card>
        <Card>
          <CardHeader>Cancellation</CardHeader>
          <CardBody>
            <Text size="small">
              Client disconnect / Cursor abort Ã¢â€ â€™ cancel ctx. Hub waiters should select on ctx
              as well as offer channel (today: time-bounded Wait).
            </Text>
          </CardBody>
        </Card>
      </Grid>
    </Stack>
  );
}

function LoopsSection() {
  return (
    <Stack gap={16}>
      <Text tone="secondary">
        Map Cursor product loops onto Glider harness capabilities Ã¢â‚¬â€ Glider is not a coding
        agent UI; it can still host the eval/reflect cycle under the proxy.
      </Text>
      <Table
        headers={["Loop type", "Cursor analogue", "Glider hook", "Status"]}
        rows={[
          [
            "Recurring eval",
            "/loop interval prompt",
            "Checkpoint + re-Complete with last episode",
            "0%",
          ],
          [
            "Babysit CI",
            "babysit skill / PR checks",
            "Wake on CI fail Ã¢â€ â€™ local fix loop Ã¢â€ â€™ push policy external",
            "0%",
          ],
          [
            "Lint/test reflect",
            "impl plan Ã‚Â§9.2",
            "Background runner; only final SSE to Cursor",
            "Design",
          ],
          [
            "Agent swarm wave",
            "Slate parallel workers",
            "FanOut + Episode weave",
            "Stub",
          ],
        ]}
        striped
      />
      <Card>
        <CardHeader>Loop checkpoint schema (proposed)</CardHeader>
        <CardBody>
          <Stack gap={6}>
            <Text size="small">
              <Code>
                {`{ goal, last_episode_id, eval_status, wake_reason, next_delay_s, spend_tokens }`}
              </Code>
            </Text>
            <Text size="small" tone="secondary">
              Persist beside session history. On wake: load checkpoint Ã¢â€ â€™ route Ã¢â€ â€™ execute Ã¢â€ â€™
              write new episode Ã¢â€ â€™ update checkpoint. Never replay full chat into local context
              if episode summary exists.
            </Text>
          </Stack>
        </CardBody>
      </Card>
    </Stack>
  );
}

function SlateSection() {
  return (
    <Stack gap={16}>
      <Row gap={8} align="center" wrap>
        <Text tone="secondary">Research:</Text>
        <Link href="https://randomlabs.ai/">randomlabs.ai</Link>
        <Link href="https://docs.randomlabs.ai/en/getting-started/introduction">
          Slate docs
        </Link>
        <Link href="https://randomlabs.ai/blog/slate">blog/slate</Link>
      </Row>
      <Callout tone="neutral" title="What not to copy">
        Full TypeScript DSL orchestration runtime, swarm-native CLI UX, or claiming hive-mind
        before Path A tools and reliable explicit overrides are solid. Glider stays a Go
        proxy/harness.
      </Callout>
      <Table headers={SLATE_MAP[0]} rows={SLATE_MAP.slice(1)} striped stickyHeader />
      <H3>Quick wins (safe, high leverage)</H3>
      <Stack gap={6}>
        <Text size="small">1. Role-tagged routing into existing registry aliases</Text>
        <Text size="small">2. Episode stub after every local fulfill (metrics + history)</Text>
        <Text size="small">3. Bounded fan-out prototype behind feature flag</Text>
        <Text size="small">4. Keep explicit overrides absolute (Slate expressivity still needs /cloud)</Text>
      </Stack>
    </Stack>
  );
}

function MilestonesSection() {
  return (
    <Stack gap={16}>
      <Grid columns={2} gap={16}>
        <Stack gap={8}>
          <H3>ASAP Ã¢â‚¬â€ 48 hours</H3>
          <Text size="small" tone="secondary">
            Reliability first, then swarm foundation. No Path B multi-agent in this window.
          </Text>
          <TodoListCard todos={MILESTONE_48H} defaultExpanded />
        </Stack>
        <Stack gap={8}>
          <H3>Next Ã¢â‚¬â€ 2 weeks</H3>
          <Text size="small" tone="secondary">
            Context + loops + cautious hot-swap depth. Path B tools gated on Path A proof.
          </Text>
          <TodoListCard todos={MILESTONE_2W} defaultExpanded />
        </Stack>
      </Grid>
      <Divider />
      <Text size="small" tone="tertiary">
        Acceptance for 48h FanOut: config <Code>strategy: fan_out</Code> e2e green on gateway
        only; MITM Path B unchanged. Acceptance for 2w eval loop: failing unit test causes
        Ã¢â€°Â¥1 silent local retry before Cursor UI shows final text.
      </Text>
    </Stack>
  );
}

export default function GliderOrchestrationRoadmap() {
  const [section, setSection] = useCanvasState<SectionId>(
    "glider-orch-section",
    "status",
  );

  return (
    <Stack gap={20} style={{ padding: 16 }}>
      <Stack gap={8}>
        <Row align="center" gap={10} wrap>
          <H1>Glider orchestration roadmap</H1>
          <Pill tone="warning" size="sm">
            analytical Ã‚Â· honest
          </Pill>
        </Row>
        <Text tone="secondary">
          Pending depth on Path B, routing, tools, swarms, context management, and
          Slate-inspired hot-swap concurrency Ã¢â‚¬â€ grounded in repo code, not wishful %.
        </Text>
      </Stack>

      <SectionNav active={section} onSelect={setSection} />
      <Divider />

      {section === "status" && <StatusSection />}
      {section === "backlog" && <BacklogSection />}
      {section === "context" && <ContextSection />}
      {section === "hotswap" && <HotSwapSection />}
      {section === "concurrency" && <ConcurrencySection />}
      {section === "loops" && <LoopsSection />}
      {section === "slate" && <SlateSection />}
      {section === "milestones" && <MilestonesSection />}

      <Spacer />
      <Text size="small" tone="quaternary">
        Glider @ D:\___repos\Glider Ã‚Â· canvas mirrors planning/context_and_swarm_architecture.md
      </Text>
    </Stack>
  );
}
