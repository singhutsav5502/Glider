# Graphify vs Glider contextgraph

> Research note **2026-07-19**. Sources cited below. Informs overnight context improvements in `internal/contextgraph`.

---

## What Graphify is

[Graphify](https://github.com/Graphify-Labs/graphify) (Graphify-Labs; also [graphify.com](https://graphify.com/) / [graphifylabs.ai](https://graphifylabs.ai/)) turns a codebase into a **queryable knowledge graph** for AI coding assistants (Cursor, Claude Code, Codex, Gemini CLI, etc.).

Key properties (from project README and Augment/DEV writeups):

| Idea | Graphify approach |
|------|-------------------|
| Not vector RAG | Persistent graph traversal, not embedding search |
| Local code extract | tree-sitter AST; deterministic; no LLM for code maps |
| Edge provenance | Edges tagged **EXTRACTED** (explicit in source) vs **INFERRED** |
| Artifacts | `graph.json`, `graph.html`, `GRAPH_REPORT.md` |
| Queries | `graphify query`, `path`, `explain` against the graph |
| Communities | Clustering / "god nodes" for architecture overview |

Citations:

- https://github.com/Graphify-Labs/graphify
- https://graphify.com/
- https://www.augmentcode.com/learn/graphify-v099-codebase-knowledge-graph
- https://dev.to/vikrantnegi/how-graphify-stopped-my-team-from-burning-through-cursors-context-window-2d32

---

## What Glider contextgraph is today

`internal/contextgraph` is an **append-only event log + ephemeral turn index** for sticky routing, debug, and hoop/swarm observability (`RouteDecided`, `LoopTick`, `SwarmFanOut`, `EpisodeMerged`, …). It is **runtime orchestration context**, not a codebase AST knowledge graph.

| Concern | Glider | Graphify |
|---------|--------|----------|
| Primary object | Turn / request events | Code entities + relations |
| Persistence | JSONL under `~/.glider/context` | `graph.json` in repo |
| Query | Recent events / turn views (overnight: `Query`, `PathSummary`) | First-class query/path/explain |
| Provenance | Attrs on events (overnight: EXTRACTED/INFERRED on facts) | Built into every edge |
| Consumers | Gateway sticky + hoop SM relevancy + tools `context_query` | IDE assistants |

---

## What we adopt (without copying)

1. **Query over re-read** — `Store.Query` / `context_query` tool so hoop + swarm share one lookup surface.
2. **Provenance tags** — `EXTRACTED` vs `INFERRED` on `RecordFact` / event attrs.
3. **Path summary** — lightweight `PathSummary(turn, from, to)` for decision narratives (not AST paths).
4. **RelevancyScore(turn)** — feeds AI-first state machine guards from shared turn activity.

## What we defer

- tree-sitter codebase indexing (out of scope for orchestrator MVP)
- Leiden communities / god-node reports
- git-merge drivers for graph.json
- Full Graphify `explain` UX

Glider stays a **gateway + hoop/swarm orchestrator**; Graphify remains inspirational for making context **structured, queryable, and provenance-aware** rather than a siloed chat buffer.
