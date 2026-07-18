# Glider: TDD Test Plan & Phase Success Criteria

> Every phase is defined by the tests that must pass before it is complete.
> Development follows **Red → Green → Refactor**: write the test first, watch it fail, implement until it passes, then clean up.
>
> **Status:** Phases 1–5 core tests are implemented in-repo. **Phase 6** (MITM, Responses, aliases, shared `PipelineCompleter` / `CompleteLocal`) is covered by `internal/mitm`, `internal/orchestrator/pipeline_test.go`, `internal/api/responses_test.go`, and `e2e/mitm_test.go`. Dashboard/observability extensions (`/api/vram`, `/api/sessions`, history store, config validate, GPU assignments) live under `internal/dashboard` and `internal/metrics` tests.

---

## Development Workflow

```
1. Start phase → Write ALL test stubs for the phase (they all fail — RED)
2. Implement feature → Run tests → Target test turns GREEN
3. Refactor → All tests still GREEN
4. Repeat until all phase tests pass
5. Phase complete ✓ → Move to next phase
```

**Test file convention:** `*_test.go` adjacent to the implementation file.
**Test command:** `go test ./...` must pass with 0 failures before a phase is signed off.
**Coverage target:** ≥80% line coverage on all non-trivial packages.

---

## Phase 1 — Foundation & Proxy

> **Phase is DONE when:** Cursor can point to `localhost:8080`, send a chat completion request, and receive a streamed response from Ollama, vLLM, or a cloud backend — with all tests below passing.

---

### 1.1 API Gateway (`internal/api/`)

#### `T1.1.1` — Parse valid OpenAI chat completion request
| | |
|---|---|
| **Type** | Unit |
| **Given** | A valid JSON body: `{model: "gpt-4o", messages: [{role: "user", content: "hello"}], stream: true}` |
| **When** | `POST /v1/chat/completions` is received |
| **Then** | Request is parsed into `CompletionRequest` struct without error. `Model`, `Messages`, and `Stream` fields are correctly populated. |
| **Success** | No parse error. All fields match input. |

#### `T1.1.2` — Reject malformed request
| | |
|---|---|
| **Type** | Unit |
| **Given** | An invalid JSON body (missing `messages` field) |
| **When** | `POST /v1/chat/completions` is received |
| **Then** | Returns HTTP 400 with an OpenAI-format error response: `{error: {message: "...", type: "invalid_request_error"}}` |
| **Success** | Status 400. Error body matches OpenAI error schema. |

#### `T1.1.3` — Stream SSE response correctly
| | |
|---|---|
| **Type** | Unit |
| **Given** | A mock backend that emits 3 `CompletionChunk`s: `"Hello"`, `" world"`, `"!"` then signals done |
| **When** | Proxy streams the response back |
| **Then** | HTTP response has `Content-Type: text/event-stream`. Body contains 3 `data: {...}` lines followed by `data: [DONE]`. Each chunk has valid OpenAI `chat.completion.chunk` format with incrementing content. |
| **Success** | All 3 chunks received in order. Final `[DONE]` sentinel present. Valid JSON in every chunk. |

#### `T1.1.4` — Non-streaming response
| | |
|---|---|
| **Type** | Unit |
| **Given** | A request with `stream: false` |
| **When** | Backend returns a complete response |
| **Then** | Returns a single JSON response in OpenAI `chat.completion` format (not chunked SSE). |
| **Success** | Single JSON object returned. `choices[0].message.content` contains full response. |

#### `T1.1.5` — List models endpoint
| | |
|---|---|
| **Type** | Unit |
| **Given** | Backend registry has 3 models registered |
| **When** | `GET /v1/models` is received |
| **Then** | Returns OpenAI-format model list: `{data: [{id: "model-name", object: "model"}, ...]}` |
| **Success** | All 3 models listed. Response matches OpenAI schema. |

#### `T1.1.6` — Request ID propagation
| | |
|---|---|
| **Type** | Unit |
| **Given** | Any valid request |
| **When** | Request is processed |
| **Then** | A unique `X-Request-ID` header is added to the response. The same ID appears in all SSE chunks. |
| **Success** | Header present. ID is unique across multiple requests. |

---

### 1.2 Backend Interface Compliance (`internal/backend/`)

#### `T1.2.1` — Ollama backend implements InferenceBackend
| | |
|---|---|
| **Type** | Unit (compile-time) |
| **Given** | `ollama.Backend` struct |
| **When** | Assigned to a variable of type `InferenceBackend` |
| **Then** | Compiles without error |
| **Success** | `var _ InferenceBackend = (*ollama.Backend)(nil)` compiles. |

#### `T1.2.2` — vLLM backend implements InferenceBackend + LoRAManager
| | |
|---|---|
| **Type** | Unit (compile-time) |
| **Given** | `vllm.Backend` struct |
| **When** | Assigned to variables of type `InferenceBackend` and `LoRAManager` |
| **Then** | Both compile without error |
| **Success** | Both interface assertions compile. |

#### `T1.2.3` — Cloud backends implement InferenceBackend
| | |
|---|---|
| **Type** | Unit (compile-time) |
| **Given** | `cloud.OpenAIBackend` and `cloud.AnthropicBackend` structs |
| **When** | Assigned to `InferenceBackend` variables |
| **Then** | Compiles without error |
| **Success** | Both interface assertions compile. |

---

### 1.3 Ollama Backend (`internal/backend/ollama/`)

#### `T1.3.1` — Complete: stream response from Ollama
| | |
|---|---|
| **Type** | Integration (mock HTTP server) |
| **Given** | A mock Ollama server that returns SSE chunks for `/v1/chat/completions` |
| **When** | `ollama.Backend.Complete(ctx, req)` is called |
| **Then** | Returns a channel that emits `CompletionChunk`s matching the mock server's output. Channel closes after final chunk. |
| **Success** | All chunks received. Channel closed. No errors. |

#### `T1.3.2` — Complete: handle Ollama error
| | |
|---|---|
| **Type** | Integration (mock HTTP server) |
| **Given** | A mock Ollama server that returns HTTP 500 |
| **When** | `Complete()` is called |
| **Then** | Returns an error, not a channel. Error contains the status code and Ollama's error message. |
| **Success** | Error returned. Error message is descriptive. |

#### `T1.3.3` — Complete: handle connection refused
| | |
|---|---|
| **Type** | Unit |
| **Given** | Ollama URL points to a port with nothing listening |
| **When** | `Complete()` is called |
| **Then** | Returns an error within 5 seconds (not hanging forever). |
| **Success** | Error returned. No hang. Timeout respected. |

#### `T1.3.4` — LoadModel: preload model into Ollama
| | |
|---|---|
| **Type** | Integration (mock HTTP server) |
| **Given** | Mock Ollama server accepts `POST /api/generate` with empty prompt |
| **When** | `LoadModel(ctx, "llama3:8b", opts)` is called with `KeepAlive: 30m` |
| **Then** | Sends correct JSON to Ollama: `{model: "llama3:8b", keep_alive: "30m"}` |
| **Success** | Request body matches expected. No error returned. |

#### `T1.3.5` — UnloadModel: send keep_alive=0
| | |
|---|---|
| **Type** | Integration (mock HTTP server) |
| **Given** | Mock Ollama server |
| **When** | `UnloadModel(ctx, "llama3:8b")` is called |
| **Then** | Sends `{model: "llama3:8b", keep_alive: 0}` to Ollama |
| **Success** | Request body contains `keep_alive: 0`. |

#### `T1.3.6` — ListLoaded: parse /api/ps response
| | |
|---|---|
| **Type** | Integration (mock HTTP server) |
| **Given** | Mock Ollama returns `{models: [{name: "llama3:8b", size_vram: 5000000000}]}` from `/api/ps` |
| **When** | `ListLoaded(ctx)` is called |
| **Then** | Returns `[]LoadedModel` with one entry. `Name` = "llama3:8b", `SizeVRAM` = 5000000000. |
| **Success** | Correct model count and metadata. |

#### `T1.3.7` — HealthCheck: ping Ollama
| | |
|---|---|
| **Type** | Integration (mock HTTP server) |
| **Given** | Mock Ollama server responds 200 to `GET /` |
| **When** | `Ping(ctx)` is called |
| **Then** | Returns nil error. `IsHealthy()` returns true. |
| **Success** | No error. Healthy = true. |

#### `T1.3.8` — HealthCheck: Ollama down
| | |
|---|---|
| **Type** | Unit |
| **Given** | Ollama URL points to nothing |
| **When** | `Ping(ctx)` is called |
| **Then** | Returns error. `IsHealthy()` returns false. |
| **Success** | Error returned. Healthy = false. |

---

### 1.4 vLLM Backend (`internal/backend/vllm/`)

#### `T1.4.1` — Complete: stream response from vLLM
| | |
|---|---|
| **Type** | Integration (mock HTTP server) |
| **Given** | A mock vLLM server that returns OpenAI-format SSE chunks |
| **When** | `vllm.Backend.Complete(ctx, req)` is called |
| **Then** | Returns a channel that emits matching `CompletionChunk`s. |
| **Success** | All chunks received correctly. |

#### `T1.4.2` — LoadAdapter: load LoRA adapter
| | |
|---|---|
| **Type** | Integration (mock HTTP server) |
| **Given** | Mock vLLM server accepts `POST /v1/load_lora_adapter` |
| **When** | `LoadAdapter(ctx, "refactor-lora", "./adapters/refactor/")` is called |
| **Then** | Sends correct JSON to vLLM with adapter name and path. |
| **Success** | Request body matches. No error. |

#### `T1.4.3` — UnloadAdapter: unload LoRA adapter
| | |
|---|---|
| **Type** | Integration (mock HTTP server) |
| **Given** | Mock vLLM server accepts `POST /v1/unload_lora_adapter` |
| **When** | `UnloadAdapter(ctx, "refactor-lora")` is called |
| **Then** | Sends correct unload request. |
| **Success** | No error. |

#### `T1.4.4` — Complete with adapter: sets model field for LoRA routing
| | |
|---|---|
| **Type** | Integration (mock HTTP server) |
| **Given** | Request has `Metadata.Adapter = "refactor-lora"` |
| **When** | `Complete()` is called |
| **Then** | The request forwarded to vLLM includes the adapter in the model field or appropriate header. |
| **Success** | vLLM receives adapter routing information. |

---

### 1.5 Cloud Backends (`internal/backend/cloud/`)

#### `T1.5.1` — OpenAI: forward request and stream response
| | |
|---|---|
| **Type** | Integration (mock HTTP server) |
| **Given** | Mock OpenAI server returns SSE chunks |
| **When** | `openai.Backend.Complete(ctx, req)` is called |
| **Then** | Request is forwarded with `Authorization: Bearer <api_key>` header. SSE chunks are returned on channel. |
| **Success** | Auth header present. All chunks received. |

#### `T1.5.2` — Anthropic: forward request with correct headers
| | |
|---|---|
| **Type** | Integration (mock HTTP server) |
| **Given** | Mock Anthropic server |
| **When** | `anthropic.Backend.Complete(ctx, req)` is called |
| **Then** | Request includes `x-api-key` and `anthropic-version` headers. Request body is translated from OpenAI format to Anthropic format. Response is translated back. |
| **Success** | Headers correct. Format translation works both ways. |

#### `T1.5.3` — Cloud backend: handle API error (429 rate limit)
| | |
|---|---|
| **Type** | Integration (mock HTTP server) |
| **Given** | Mock server returns HTTP 429 with `Retry-After: 30` header |
| **When** | `Complete()` is called |
| **Then** | Returns a typed error that includes the status code and retry-after duration. |
| **Success** | Error is identifiable as rate-limit. Retry-after value is extracted. |

---

### 1.6 Backend Registry (`internal/backend/`)

#### `T1.6.1` — Register and retrieve backend
| | |
|---|---|
| **Type** | Unit |
| **Given** | A registry with an Ollama backend registered as "ollama" |
| **When** | `registry.Get("ollama")` is called |
| **Then** | Returns the Ollama backend instance. |
| **Success** | Correct instance returned. No error. |

#### `T1.6.2` — Get unknown backend returns error
| | |
|---|---|
| **Type** | Unit |
| **Given** | Registry has only "ollama" registered |
| **When** | `registry.Get("nonexistent")` is called |
| **Then** | Returns nil and an error. |
| **Success** | Error indicates backend not found. |

#### `T1.6.3` — List all backends
| | |
|---|---|
| **Type** | Unit |
| **Given** | Registry has "ollama", "vllm", "openai" registered |
| **When** | `registry.List()` is called |
| **Then** | Returns all 3 backends. |
| **Success** | Count = 3. All names present. |

#### `T1.6.4` — Register duplicate name returns error
| | |
|---|---|
| **Type** | Unit |
| **Given** | "ollama" is already registered |
| **When** | Another backend is registered with name "ollama" |
| **Then** | Returns an error. Original backend is not overwritten. |
| **Success** | Error returned. `Get("ollama")` returns original. |

---

### 1.7 End-to-End Passthrough (`e2e/`)

#### `T1.7.1` — Full round-trip: Proxy → Ollama → Proxy
| | |
|---|---|
| **Type** | E2E (mock Ollama server) |
| **Given** | Glider proxy running on `:8080`. Mock Ollama on `:11434` returning 3 SSE chunks. |
| **When** | HTTP client sends `POST localhost:8080/v1/chat/completions` with `stream: true` |
| **Then** | Client receives 3 SSE chunks + `[DONE]`. Content matches what mock Ollama sent. Response headers include `Content-Type: text/event-stream`. |
| **Success** | Byte-for-byte SSE output matches expected. Latency overhead < 10ms. |

#### `T1.7.2` — Full round-trip: Proxy → Cloud → Proxy
| | |
|---|---|
| **Type** | E2E (mock cloud server) |
| **Given** | Glider proxy configured with cloud backend pointing to mock server. |
| **When** | Request is sent to Glider |
| **Then** | Glider forwards to mock cloud, streams response back. |
| **Success** | Response received. Auth header forwarded. SSE format correct. |

---

## Phase 2 — Config, Router & Rules Engine

> **Phase is DONE when:** Requests are routed to different backends based on explicit commands, regex patterns, context size, and Starlark scripts — with config changes taking effect without restart.

---

### 2.1 Config Loader (`internal/config/`)

#### `T2.1.1` — Parse valid glider.yaml
| | |
|---|---|
| **Type** | Unit |
| **Given** | A valid `glider.yaml` file with all sections populated |
| **When** | `LoadConfig("glider.yaml")` is called |
| **Then** | Returns a `Config` struct with all fields correctly populated. Nested structs (thresholds, vram, routing.rules) are parsed. |
| **Success** | Every field matches the YAML source. No error. |

#### `T2.1.2` — Reject invalid YAML (syntax error)
| | |
|---|---|
| **Type** | Unit |
| **Given** | A YAML file with a syntax error (e.g., bad indentation) |
| **When** | `LoadConfig()` is called |
| **Then** | Returns a descriptive parse error. |
| **Success** | Error includes line number. |

#### `T2.1.3` — Reject invalid config (missing required fields)
| | |
|---|---|
| **Type** | Unit |
| **Given** | A YAML file missing `server.proxy_port` |
| **When** | `LoadConfig()` is called |
| **Then** | Returns a validation error listing the missing field. |
| **Success** | Error is specific about what's missing. |

#### `T2.1.4` — Apply defaults for optional fields
| | |
|---|---|
| **Type** | Unit |
| **Given** | A minimal YAML with only required fields |
| **When** | Config is loaded |
| **Then** | Optional fields have sensible defaults (e.g., `idle_unload_timeout` = "5m", `vram.strategy` = "dynamic"). |
| **Success** | Default values match spec. |

---

### 2.2 Config Hot-Reload (`internal/config/`)

#### `T2.2.1` — Detect file change and reload
| | |
|---|---|
| **Type** | Integration |
| **Given** | Config watcher is running on a temp `glider.yaml`. Initial `max_local_context_tokens` = 8000. |
| **When** | The file is modified to set `max_local_context_tokens` = 12000 |
| **Then** | Within 2 seconds, `configProvider.Get().Thresholds.MaxLocalContextTokens` returns 12000. |
| **Success** | New value reflected without restart. |

#### `T2.2.2` — Invalid config change is rejected (old config preserved)
| | |
|---|---|
| **Type** | Integration |
| **Given** | Config watcher running. Current config is valid with `max_local_context_tokens` = 8000. |
| **When** | File is overwritten with invalid YAML |
| **Then** | Error is logged. `configProvider.Get()` still returns old config with 8000. |
| **Success** | Old config preserved. Error logged. No crash. |

#### `T2.2.3` — Subscriber callback fires on valid reload
| | |
|---|---|
| **Type** | Integration |
| **Given** | A subscriber registered via `configProvider.Watch(callback)` |
| **When** | Config file is modified with a valid change |
| **Then** | Callback is invoked with the new `*Config`. |
| **Success** | Callback received. New config values correct. |

---

### 2.3 Tokenizer (`internal/transform/`)

#### `T2.3.1` — Count tokens for known input
| | |
|---|---|
| **Type** | Unit |
| **Given** | Input string: `"Hello, world!"` with cl100k_base encoding |
| **When** | `tokenizer.Count("Hello, world!")` is called |
| **Then** | Returns token count consistent with tiktoken (expected: 4). |
| **Success** | Count matches reference implementation. |

#### `T2.3.2` — Estimate tokens for full CompletionRequest
| | |
|---|---|
| **Type** | Unit |
| **Given** | A `CompletionRequest` with 3 messages totaling ~500 words |
| **When** | `tokenizer.EstimateRequestTokens(req)` is called |
| **Then** | Returns an estimate within ±10% of the actual tiktoken count. |
| **Success** | Estimate is within tolerance. |

#### `T2.3.3` — Handle empty input
| | |
|---|---|
| **Type** | Unit |
| **Given** | Empty string input |
| **When** | `Count("")` is called |
| **Then** | Returns 0. No error. |
| **Success** | Returns 0. |

---

### 2.4 Rule Engine (`internal/router/`)

#### `T2.4.1` — ExplicitCommandRule: match "/local" prefix
| | |
|---|---|
| **Type** | Unit |
| **Given** | Rule configured with commands `["/local", "/fast"]`. Last message content: `"/local refactor this function"` |
| **When** | `rule.Evaluate(ctx, req)` is called |
| **Then** | Returns `{Matched: true, Action: {Target: "local", ...}}`. |
| **Success** | Matched = true. Action populated correctly. |

#### `T2.4.2` — ExplicitCommandRule: no match
| | |
|---|---|
| **Type** | Unit |
| **Given** | Same rule. Last message: `"refactor this function"` (no prefix) |
| **When** | `Evaluate()` is called |
| **Then** | Returns `{Matched: false}`. |
| **Success** | Matched = false. |

#### `T2.4.3` — ExplicitCommandRule: strip command from message before forwarding
| | |
|---|---|
| **Type** | Unit |
| **Given** | Last message: `"/local refactor this function"` |
| **When** | Rule matches and action specifies stripping the command prefix |
| **Then** | The `CompletionRequest` messages are updated so the content becomes `"refactor this function"` (without `/local`). |
| **Success** | Command prefix removed. Rest of message preserved. |

#### `T2.4.4` — RegexRule: match pattern
| | |
|---|---|
| **Type** | Unit |
| **Given** | Rule with pattern `(?i)\b(refactor|rename|extract)\b`. Message: `"Please refactor this class"` |
| **When** | `Evaluate()` is called |
| **Then** | Returns `{Matched: true}`. |
| **Success** | Matched = true. |

#### `T2.4.5` — RegexRule: no match
| | |
|---|---|
| **Type** | Unit |
| **Given** | Same pattern. Message: `"Explain how this works"` |
| **When** | `Evaluate()` is called |
| **Then** | Returns `{Matched: false}`. |
| **Success** | Matched = false. |

#### `T2.4.6` — ContextSizeRule: over threshold
| | |
|---|---|
| **Type** | Unit |
| **Given** | Rule with `operator: ">"`, `value: 8000`. Request has `EstimatedTokens: 12000`. |
| **When** | `Evaluate()` is called |
| **Then** | Returns `{Matched: true}`. |
| **Success** | Matched = true (12000 > 8000). |

#### `T2.4.7` — ContextSizeRule: under threshold
| | |
|---|---|
| **Type** | Unit |
| **Given** | Same rule. Request has `EstimatedTokens: 4000`. |
| **When** | `Evaluate()` is called |
| **Then** | Returns `{Matched: false}`. |
| **Success** | Matched = false (4000 ≤ 8000). |

#### `T2.4.8` — Router: first matching rule wins (priority ordering)
| | |
|---|---|
| **Type** | Unit |
| **Given** | 3 rules: ExplicitCommand (priority 100), Regex (priority 50), ContextSize (priority 10). Request matches both Regex and ContextSize rules. |
| **When** | `router.Route(ctx, req)` is called |
| **Then** | Returns the Regex rule's action (priority 50 > 10). `RuleName` field identifies which rule matched. |
| **Success** | Higher priority rule wins. RuleName correct. |

#### `T2.4.9` — Router: no rule matches → default rule
| | |
|---|---|
| **Type** | Unit |
| **Given** | Rules configured with an "always" default rule at priority 0. Request matches no other rules. |
| **When** | `Route()` is called |
| **Then** | Returns the default rule's action. |
| **Success** | Default action returned. |

---

### 2.5 Starlark Script Executor (`internal/router/`)

#### `T2.5.1` — Execute valid script and get routing decision
| | |
|---|---|
| **Type** | Unit |
| **Given** | A Starlark script that defines `def evaluate(request):` and returns `{"matched": True, "action": {"target": "local", "model": "codellama:7b"}}` when `"refactor"` is in the message |
| **When** | `starlarkExecutor.Run(scriptPath, request)` is called with a message containing "refactor" |
| **Then** | Returns `{Matched: true, Action: {Target: "local", Model: "codellama:7b"}}`. |
| **Success** | Return value correctly parsed into Go struct. |

#### `T2.5.2` — Script returns no match
| | |
|---|---|
| **Type** | Unit |
| **Given** | Same script. Message does not contain "refactor". |
| **When** | `Run()` is called |
| **Then** | Returns `{Matched: false}`. |
| **Success** | Matched = false. |

#### `T2.5.3` — Script with syntax error
| | |
|---|---|
| **Type** | Unit |
| **Given** | A Starlark script with invalid syntax (e.g., unclosed parenthesis) |
| **When** | `Run()` is called |
| **Then** | Returns error with line number and description of the syntax issue. |
| **Success** | Error is descriptive. No panic. |

#### `T2.5.4` — Script exceeds execution step limit
| | |
|---|---|
| **Type** | Unit |
| **Given** | A Starlark script with an infinite `for` loop: `for i in range(999999999): pass` |
| **When** | `Run()` is called (with `MaxExecutionSteps = 1_000_000`) |
| **Then** | Returns a timeout/step-limit error within 1 second. |
| **Success** | Error returned. No hang. Completes within time limit. |

#### `T2.5.5` — Script caching: compiled script is reused
| | |
|---|---|
| **Type** | Unit |
| **Given** | Same script file executed twice |
| **When** | `Run()` is called the second time |
| **Then** | Script is not re-parsed from disk. Cached compiled form is used. Second call is faster than first. |
| **Success** | File read count = 1 (verified via mock filesystem or counter). |

#### `T2.5.6` — Script cache invalidation on file change
| | |
|---|---|
| **Type** | Integration |
| **Given** | Script was previously cached. File is then modified. |
| **When** | `Run()` is called again |
| **Then** | New version of the script is loaded and executed. |
| **Success** | New behavior reflected in result. |

#### `T2.5.7` — Script cannot access filesystem
| | |
|---|---|
| **Type** | Unit |
| **Given** | A Starlark script that attempts to call a function like `read_file("secret.txt")` |
| **When** | `Run()` is called |
| **Then** | Returns error: function not found. No file access occurs. |
| **Success** | Sandbox holds. Error returned. |

#### `T2.5.8` — Request data is passed correctly to script
| | |
|---|---|
| **Type** | Unit |
| **Given** | Request with `messages`, `estimated_tokens: 5000`, `model: "gpt-4o"` |
| **When** | Script accesses `request["estimated_tokens"]` and `request["messages"]` |
| **Then** | Values match the Go-side `CompletionRequest` data. |
| **Success** | All fields accessible and correct in Starlark. |

#### `T2.4.10` — RoutingDecision defaults to Strategy = "single"
| | |
|---|---|
| **Type** | Unit |
| **Given** | A valid request matching an explicit rule (which does not specify a strategy). |
| **When** | `Evaluate()` returns the decision |
| **Then** | The `Strategy` field is set to `"single"`. |
| **Success** | `StrategySingle` is the default. |

---

### 2.6 Integrated Routing E2E (`e2e/`)

#### `T2.6.1` — Explicit `/local` command routes to Ollama
| | |
|---|---|
| **Type** | E2E |
| **Given** | Glider running with config. Mock Ollama + mock Cloud. Message starts with `/local`. |
| **When** | Request sent to Glider proxy |
| **Then** | Mock Ollama receives the request. Mock Cloud does NOT receive anything. Command prefix is stripped from the forwarded message. |
| **Success** | Ollama hit. Cloud not hit. Prefix stripped. |

#### `T2.6.2` — Large context routes to cloud
| | |
|---|---|
| **Type** | E2E |
| **Given** | `max_local_context_tokens: 8000`. Request has ~12000 tokens. |
| **When** | Request sent to Glider |
| **Then** | Mock Cloud receives the request. Mock Ollama does NOT. |
| **Success** | Cloud hit. Ollama not hit. |

#### `T2.6.3` — Config hot-reload changes routing behavior
| | |
|---|---|
| **Type** | E2E |
| **Given** | Initial config: `max_local_context_tokens: 8000`. A 10000-token request routes to cloud. |
| **When** | Config is changed to `max_local_context_tokens: 15000` (file edited while Glider runs) |
| **Then** | Same 10000-token request now routes to local. |
| **Success** | Routing behavior changed without restart. |

---

## Phase 3 — VRAM Management, Model Lifecycle & Orchestrator

> **Phase is DONE when:** Models load/unload dynamically based on VRAM state, idle models are evicted, request queuing prevents GPU contention, and backend failures trigger cloud fallback.

---

### 3.1 VRAM Monitor (`internal/vram/`)

#### `T3.1.1` — Parse nvidia-smi output
| | |
|---|---|
| **Type** | Unit |
| **Given** | Mock `nvidia-smi` output: `"8192, 3500, 4692"` (total, used, free in MiB) |
| **When** | `monitor.parseNvidiaSmiOutput(output)` is called |
| **Then** | Returns `GPUMemoryInfo{Total: 8589934592, Used: 3670016000, Free: 4919918592}` (bytes). |
| **Success** | All values correctly converted from MiB to bytes. |

#### `T3.1.2` — Handle nvidia-smi not found
| | |
|---|---|
| **Type** | Unit |
| **Given** | `nvidia-smi` is not in PATH |
| **When** | `monitor.GetMemoryInfo()` is called |
| **Then** | Returns a descriptive error (not a panic). |
| **Success** | Graceful error. |

#### `T3.1.3` — Multi-GPU: parse multiple GPU lines
| | |
|---|---|
| **Type** | Unit |
| **Given** | `nvidia-smi` output with 2 GPU lines |
| **When** | `GetDeviceCount()` and `GetMemoryInfo(gpuIndex)` are called |
| **Then** | Returns 2 devices. Each GPU's memory info is independently correct. |
| **Success** | Device count = 2. Per-GPU values correct. |

---

### 3.2 Model Registry (`internal/backend/`)

#### `T3.2.1` — Register model with metadata
| | |
|---|---|
| **Type** | Unit |
| **Given** | Model config: `{Name: "codellama:7b", VRAMEstimateMB: 4200, MaxContext: 16384, Capabilities: ["code"]}` |
| **When** | `registry.RegisterModel(config)` is called |
| **Then** | `registry.GetModel("codellama:7b")` returns the model with all metadata. |
| **Success** | All fields match. |

#### `T3.2.2` — Track model state (COLD/WARM)
| | |
|---|---|
| **Type** | Unit |
| **Given** | Model registered as COLD |
| **When** | `registry.SetModelState("codellama:7b", WARM)` is called |
| **Then** | `registry.GetModel("codellama:7b").State` returns WARM. |
| **Success** | State updated. |

#### `T3.2.3` — Find model by capability
| | |
|---|---|
| **Type** | Unit |
| **Given** | Registry has models with capabilities: `["code", "refactor"]` and `["general", "docs"]` |
| **When** | `registry.FindByCapability("code")` is called |
| **Then** | Returns only the model with the "code" capability. |
| **Success** | Correct model(s) returned. |

---

### 3.3 VRAM Allocator (`internal/vram/`)

#### `T3.3.1` — CanFit: model fits in free VRAM
| | |
|---|---|
| **Type** | Unit |
| **Given** | VRAM state: Total=8GB, Used=2GB, Free=6GB, Headroom=512MB. Model needs 4GB. |
| **When** | `manager.CanFit("model-x", 4GB)` is called |
| **Then** | Returns `(true, nil)` — fits without eviction. |
| **Success** | Returns true. No eviction plan. |

#### `T3.3.2` — CanFit: model needs eviction
| | |
|---|---|
| **Type** | Unit |
| **Given** | VRAM state: Total=8GB, Used=6GB, Free=2GB, Headroom=512MB. Model needs 4GB. Loaded models: A (2GB, idle 10m), B (4GB, idle 1m). |
| **When** | `CanFit("model-x", 4GB)` is called |
| **Then** | Returns `(true, EvictionPlan{ModelsToEvict: ["A"], BytesFreed: 2GB})` — evict LRU model A to make room. |
| **Success** | Eviction plan targets the least recently used model. |

#### `T3.3.3` — CanFit: model cannot fit even after full eviction
| | |
|---|---|
| **Type** | Unit |
| **Given** | VRAM state: Total=8GB, Headroom=512MB. Model needs 10GB. |
| **When** | `CanFit("model-x", 10GB)` is called |
| **Then** | Returns `(false, nil)`. |
| **Success** | Returns false. No eviction plan (impossible to fit). |

#### `T3.3.4` — Headroom is respected
| | |
|---|---|
| **Type** | Unit |
| **Given** | Total=8GB, Used=3GB, Free=5GB, Headroom=2GB. Model needs 4GB. |
| **When** | `CanFit()` is called |
| **Then** | Returns `(false, EvictionPlan{...})` — because 5GB free - 2GB headroom = only 3GB usable, and model needs 4GB. |
| **Success** | Headroom subtracted from available. |

#### `T3.3.5` — Reserve and release VRAM
| | |
|---|---|
| **Type** | Unit |
| **Given** | Manager tracking 0 bytes allocated. |
| **When** | `Reserve("model-a", 4GB)` then `Release("model-a")` |
| **Then** | After reserve: tracked allocation = 4GB. After release: tracked allocation = 0. |
| **Success** | Bookkeeping accurate. |

#### `T3.3.6` — BatchReserve allocates atomically
| | |
|---|---|
| **Type** | Unit |
| **Given** | VRAM state with 6GB free. A batch reservation request for 2 models requiring 4GB each (8GB total). |
| **When** | `BatchReserve(models)` is called |
| **Then** | Returns false/error. Zero bytes are reserved. (All-or-nothing allocation). |
| **Success** | State remains unchanged if batch cannot fit entirely. |

---

### 3.4 Execution Layer & State Machine (`internal/orchestrator/`)

#### `T3.4.0` — SimpleExecutor implements Executor interface
| | |
|---|---|
| **Type** | Unit (compile-time) |
| **Given** | `SimpleExecutor` struct |
| **When** | Assigned to a variable of type `Executor` |
| **Then** | Compiles without error. |
| **Success** | Interface compliance verified. |

#### `T3.4.1` — COLD → LOADING → WARM transition
| | |
|---|---|
| **Type** | Unit |
| **Given** | Model in COLD state. Mock backend `LoadModel()` succeeds. |
| **When** | Orchestrator receives a request for this model. |
| **Then** | State transitions: COLD → LOADING → WARM. Backend's `LoadModel()` is called. |
| **Success** | Final state = WARM. LoadModel called once. |

#### `T3.4.2` — WARM model stays WARM on subsequent requests
| | |
|---|---|
| **Type** | Unit |
| **Given** | Model already in WARM state. |
| **When** | Another request for this model arrives. |
| **Then** | No state transition. `LoadModel()` is NOT called again. Idle timer is reset. |
| **Success** | LoadModel not called. Timer reset. |

#### `T3.4.3` — WARM → UNLOADING → COLD on idle timeout
| | |
|---|---|
| **Type** | Integration |
| **Given** | Model in WARM state. `idle_unload_timeout` = 1 second (for testing). |
| **When** | No requests arrive for 1.5 seconds. |
| **Then** | State transitions: WARM → UNLOADING → COLD. Backend's `UnloadModel()` is called. |
| **Success** | Final state = COLD. UnloadModel called. VRAM released. |

#### `T3.4.4` — Request during LOADING state queues until ready
| | |
|---|---|
| **Type** | Unit |
| **Given** | Model in LOADING state (LoadModel in progress, takes 500ms). |
| **When** | A second request arrives for the same model. |
| **Then** | Second request waits until model reaches WARM. Does NOT trigger a second `LoadModel()`. |
| **Success** | Only 1 LoadModel call. Both requests eventually served. |

#### `T3.4.5` — LOADING fails → falls back (does not get stuck)
| | |
|---|---|
| **Type** | Unit |
| **Given** | Backend `LoadModel()` returns an error (e.g., OOM). |
| **When** | State tries to transition COLD → LOADING |
| **Then** | State returns to COLD. Error is propagated to the orchestrator for fallback. |
| **Success** | State = COLD. Error returned. No stuck LOADING state. |

---

### 3.5 Request Queue (`internal/orchestrator/`)

#### `T3.5.1` — Requests processed in priority order
| | |
|---|---|
| **Type** | Unit |
| **Given** | Queue receives: Request A (LOW priority), then Request B (HIGH priority). |
| **When** | Worker dequeues |
| **Then** | Request B is dequeued first. |
| **Success** | HIGH before LOW. |

#### `T3.5.2` — Same priority: FIFO ordering
| | |
|---|---|
| **Type** | Unit |
| **Given** | Queue receives: Request A (HIGH), then Request B (HIGH). |
| **When** | Worker dequeues |
| **Then** | A dequeued before B (FIFO within same priority). |
| **Success** | Insertion order preserved at same priority. |

#### `T3.5.3` — Queue respects context cancellation
| | |
|---|---|
| **Type** | Unit |
| **Given** | Request enqueued with a context that is cancelled after 100ms. |
| **When** | Request hasn't been dequeued yet at 100ms. |
| **Then** | Request is removed from queue. Caller receives context cancellation error. |
| **Success** | Cancelled request doesn't block the queue. |

---

### 3.6 Fallback Chain (`internal/orchestrator/`)

#### `T3.6.1` — Local failure triggers cloud fallback
| | |
|---|---|
| **Type** | Integration |
| **Given** | Routing decision targets local Ollama. Mock Ollama returns error. Cloud backend is healthy. |
| **When** | Orchestrator processes the request. |
| **Then** | Request is re-routed to cloud backend. Response streams back from cloud. |
| **Success** | Cloud response returned. No error to caller. |

#### `T3.6.2` — Both local and cloud fail → error to caller
| | |
|---|---|
| **Type** | Integration |
| **Given** | Both local and cloud backends return errors. |
| **When** | Orchestrator processes the request. |
| **Then** | Returns an OpenAI-format error response with HTTP 502. |
| **Success** | Error returned. No infinite retry. |

#### `T3.6.3` — Circuit breaker opens after repeated failures
| | |
|---|---|
| **Type** | Unit |
| **Given** | Backend fails 5 consecutive times (threshold = 5). |
| **When** | 6th request arrives. |
| **Then** | Circuit breaker is OPEN. Request immediately skips this backend (no attempt). Falls through to next in fallback chain. |
| **Success** | 6th request does NOT hit the failing backend. |

#### `T3.6.4` — Circuit breaker half-open: probe after cooldown
| | |
|---|---|
| **Type** | Unit |
| **Given** | Circuit breaker is OPEN. Cooldown period (e.g., 30s) has elapsed. |
| **When** | Next request arrives. |
| **Then** | Circuit breaker enters HALF-OPEN. One probe request is sent. If it succeeds, breaker closes. |
| **Success** | Probe sent. Breaker transitions correctly. |

---

### 3.7 Health Checks & Rate Limiting (`internal/orchestrator/`)

#### `T3.7.1` — Unhealthy backend is skipped during routing
| | |
|---|---|
| **Type** | Integration |
| **Given** | Ollama backend marked unhealthy by health checker. |
| **When** | Routing targets Ollama. |
| **Then** | Orchestrator skips Ollama and falls through to next backend. |
| **Success** | Unhealthy backend not hit. |

#### `T3.7.2` — Cloud rate limiter enforces RPM
| | |
|---|---|
| **Type** | Unit |
| **Given** | Rate limit: 10 requests/minute. 10 requests already sent this minute. |
| **When** | 11th request arrives targeting cloud. |
| **Then** | Request is rejected (or queued, depending on policy) with a rate-limit error. |
| **Success** | 11th request blocked. |

#### `T3.7.3` — Budget cap prevents cloud requests
| | |
|---|---|
| **Type** | Unit |
| **Given** | Budget cap: $50. Estimated spend so far: $49.90. Incoming request estimated cost: $0.20. |
| **When** | Request tries to route to cloud. |
| **Then** | Request is blocked with a budget-exceeded error. |
| **Success** | Request blocked. Error is descriptive. |

---

## Phase 4 — Dashboard, Observability & Request Transformation

> **Phase is DONE when:** Web UI displays live VRAM (discovery + GPU gauges), request logs (Mode/Action/Host/Rule/OriginalModel), session history, and cost tracking. Config (form + optional YAML) and Rules Engine can be edited from the UI. Request transformations (context trimming, augmentation) work opt-in.

---

### 4.1 Dashboard API (`internal/dashboard/`)

#### `T4.1.1` — GET /api/config returns current config
| | |
|---|---|
| **Type** | Integration |
| **Given** | Glider running with a loaded config. |
| **When** | `GET localhost:8081/api/config` |
| **Then** | Returns JSON representation of the current config. |
| **Success** | JSON matches loaded config. |

#### `T4.1.2` — PUT /api/config updates and persists config
| | |
|---|---|
| **Type** | Integration |
| **Given** | Current `max_local_context_tokens` = 8000. |
| **When** | `PUT /api/config` with body `{thresholds: {max_local_context_tokens: 12000}}` |
| **Then** | Config is updated in memory AND written back to `glider.yaml`. Subsequent requests use new threshold. |
| **Success** | Memory and file both updated. |

#### `T4.1.3` — PUT /api/config rejects invalid config
| | |
|---|---|
| **Type** | Integration |
| **Given** | PUT body with `max_local_context_tokens: -1` |
| **When** | Request sent |
| **Then** | Returns HTTP 400 with validation error. Config is NOT changed. |
| **Success** | 400 returned. Config unchanged. |

#### `T4.1.4` — GET /api/models returns loaded models with state
| | |
|---|---|
| **Type** | Integration |
| **Given** | Model registry has 2 models: one WARM, one COLD. |
| **When** | `GET /api/models` |
| **Then** | Returns JSON with both models, including state, VRAM usage, backend. |
| **Success** | Both models listed with correct state. |

#### `T4.1.5` — POST /api/models/:name/load triggers model load
| | |
|---|---|
| **Type** | Integration |
| **Given** | Model "codellama:7b" is COLD. |
| **When** | `POST /api/models/codellama:7b/load` |
| **Then** | Orchestrator loads the model. Returns 200 when model reaches WARM. |
| **Success** | Model state transitions to WARM. |

#### `T4.1.6` — POST /api/models/:name/unload triggers model unload
| | |
|---|---|
| **Type** | Integration |
| **Given** | Model "codellama:7b" is WARM. |
| **When** | `POST /api/models/codellama:7b/unload` |
| **Then** | Model is unloaded. State transitions to COLD. |
| **Success** | Model state = COLD. VRAM freed. |

#### `T4.1.7` — GET /api/vram discovers backends + GPU gauges
| | |
|---|---|
| **Type** | Integration |
| **Given** | Config with Ollama/vLLM URLs; optional nvidia-smi |
| **When** | `GET /api/vram` |
| **Then** | Snapshot includes models (config + discovered), `gpu_assignments`, and GPU memory when available |
| **Success** | JSON schema matches `VRAMSnapshot`; soft failures surface as errors/warnings not 500 |

#### `T4.1.8` — PUT /api/gpu-assignments persists map
| | |
|---|---|
| **Type** | Integration |
| **Given** | Running dashboard with FileConfigStore |
| **When** | `PUT /api/gpu-assignments` with `{ "codellama:7b": 0 }` |
| **Then** | Config `vram.gpu_assignments` updated; Provider Swap notified |
| **Success** | Subsequent `GET /api/vram` reflects assignment |

#### `T4.1.9` — Session history list + requests
| | |
|---|---|
| **Type** | Integration |
| **Given** | History store with session `run-…` and recorded requests |
| **When** | `GET /api/sessions` then `GET /api/sessions/{id}/requests` |
| **Then** | Sessions listed; requests include mode/action/rule fields |
| **Success** | Aggregates and request rows match stored JSONL |

---

### 4.2 WebSocket & Real-Time Updates (`internal/dashboard/`)

#### `T4.2.1` — WebSocket emits event on new request
| | |
|---|---|
| **Type** | Integration |
| **Given** | WebSocket client connected to `ws://localhost:8081/ws`. |
| **When** | A completion request is processed by Glider. |
| **Then** | WebSocket receives a JSON event: `{type: "request", data: {id, mode, action, host, path, rule, original_model, route, model, tokens, latency_ms}}` (fields populated per gateway/MITM path). |
| **Success** | Event received within 1 second of request completion. |

#### `T4.2.2` — WebSocket emits VRAM state updates
| | |
|---|---|
| **Type** | Integration |
| **Given** | WebSocket client connected. |
| **When** | A model is loaded/unloaded. |
| **Then** | WebSocket receives `{type: "vram_update", data: {total, used, free, models: [...]}}`. |
| **Success** | Event reflects new VRAM state. |

#### `T4.2.3` — Multiple WebSocket clients receive same events
| | |
|---|---|
| **Type** | Integration |
| **Given** | 2 WebSocket clients connected. |
| **When** | A request is processed. |
| **Then** | Both clients receive the event. |
| **Success** | Both receive identical events. |

---

### 4.3 Metrics Collector (`internal/metrics/`)

#### `T4.3.1` — Track request count by route
| | |
|---|---|
| **Type** | Unit |
| **Given** | 5 requests routed to "local", 3 to "cloud". |
| **When** | `metrics.GetRouteCounts()` is called |
| **Then** | Returns `{local: 5, cloud: 3}`. |
| **Success** | Counts accurate. |

#### `T4.3.2` — Track token usage
| | |
|---|---|
| **Type** | Unit |
| **Given** | 3 requests with estimated tokens: 2000, 5000, 1000. |
| **When** | `metrics.GetTokenStats()` is called |
| **Then** | Returns `{total: 8000, avg: 2666, min: 1000, max: 5000}`. |
| **Success** | Stats accurate. |

#### `T4.3.3` — Estimate cost savings
| | |
|---|---|
| **Type** | Unit |
| **Given** | 5 local requests that would have cost $0.10 each on cloud. |
| **When** | `metrics.GetCostSavings()` is called |
| **Then** | Returns `{estimated_cloud_cost: 0.50, actual_cost: 0.00, savings: 0.50}`. |
| **Success** | Savings calculation correct based on configured cloud pricing. |

#### `T4.3.4` — Latency tracking (percentiles)
| | |
|---|---|
| **Type** | Unit |
| **Given** | 100 requests with known latencies. |
| **When** | `metrics.GetLatencyPercentiles()` is called |
| **Then** | Returns p50, p90, p99 values. |
| **Success** | Percentiles are within expected ranges. |

---

### 4.4 Request Transformation (`internal/transform/`)

#### `T4.4.1` — Context trimming: truncate middle when oversized
| | |
|---|---|
| **Type** | Unit |
| **Given** | Request with 20000 tokens. Model's max context = 8192. Transformation enabled. |
| **When** | `transformer.TrimContext(req, 8192)` is called |
| **Then** | System prompt (first message) and user message (last message) are preserved. Middle context messages are truncated. Final token count ≤ 8192. |
| **Success** | First and last messages intact. Total tokens ≤ limit. |

#### `T4.4.2` — Context trimming: no-op when under limit
| | |
|---|---|
| **Type** | Unit |
| **Given** | Request with 3000 tokens. Model's max context = 8192. |
| **When** | `TrimContext()` is called |
| **Then** | Request is returned unmodified. |
| **Success** | Messages identical to input. |

#### `T4.4.3` — Prompt augmentation: prepend user instruction
| | |
|---|---|
| **Type** | Unit |
| **Given** | Config sets augmentation: `prepend: "Be concise. Respond in under 200 words."` |
| **When** | `transformer.Augment(req)` is called |
| **Then** | A new system message is prepended (after Cursor's system prompt) with the augmentation text. |
| **Success** | Augmentation message present. Cursor's system prompt untouched. |

#### `T4.4.4` — Transformation disabled by default
| | |
|---|---|
| **Type** | Unit |
| **Given** | No transformation config in `glider.yaml`. |
| **When** | Request passes through the transform pipeline. |
| **Then** | Request is completely unmodified (passthrough). |
| **Success** | Input == Output. |

#### `T4.4.5` — System prompts are never stripped unless user scripts it
| | |
|---|---|
| **Type** | Unit |
| **Given** | Request has a system message from Cursor. All built-in transformations enabled (trim + augment). |
| **When** | Transform pipeline runs. |
| **Then** | The original system message from Cursor is still present in the output. |
| **Success** | System message preserved. |

---

### 4.5 Dashboard Frontend (`internal/dashboard/static/`)

#### `T4.5.1` — Dashboard loads without errors
| | |
|---|---|
| **Type** | E2E (HTTP) |
| **Given** | Glider daemon running. |
| **When** | `GET localhost:8081/` |
| **Then** | Returns HTTP 200 with `Content-Type: text/html`. HTML contains expected root element (e.g., `<div id="app">`). |
| **Success** | 200 OK. HTML valid. |

#### `T4.5.2` — Static assets served with correct MIME types
| | |
|---|---|
| **Type** | E2E (HTTP) |
| **Given** | Dashboard frontend has JS and CSS files. |
| **When** | `GET /app.js` and `GET /style.css` |
| **Then** | JS returns `Content-Type: application/javascript`. CSS returns `Content-Type: text/css`. |
| **Success** | Correct MIME types. 200 OK. |

---

## Phase 5 — Integration Testing & Polish

> **Phase is DONE when:** All E2E scenarios pass, performance benchmarks meet targets, edge cases are handled gracefully, and documentation is complete.

---

### 5.1 End-to-End Scenarios (`e2e/`)

#### `T5.1.1` — Full lifecycle: cold start → serve → idle unload
| | |
|---|---|
| **Type** | E2E |
| **Given** | Glider running. No models loaded. Idle timeout = 2s (for testing). |
| **When** | 1) Send a request (triggers model load). 2) Wait 3 seconds. |
| **Then** | 1) First request succeeds (with model load latency). 2) Model is unloaded after idle timeout. VRAM freed. |
| **Success** | Response received. Model state: COLD after timeout. |

#### `T5.1.2` — Fallback cascade: Ollama crash → cloud save
| | |
|---|---|
| **Type** | E2E |
| **Given** | Glider running with Ollama and cloud backends. Ollama is killed mid-operation. |
| **When** | Request is sent. |
| **Then** | Ollama attempt fails. Cloud fallback activates. Response is returned from cloud. |
| **Success** | User receives a valid response despite Ollama being down. |

#### `T5.1.3` — Concurrent requests from multiple "clients"
| | |
|---|---|
| **Type** | E2E |
| **Given** | Glider running with request queue. |
| **When** | 5 concurrent HTTP requests are sent simultaneously. |
| **Then** | All 5 receive valid responses. No panics, no data races. Queue serializes GPU-bound work. |
| **Success** | All 5 succeed. `go test -race` passes. |

#### `T5.1.4` — Config edit via Dashboard updates routing live
| | |
|---|---|
| **Type** | E2E |
| **Given** | Threshold = 8000. Request with 6000 tokens routes to local. |
| **When** | Dashboard API changes threshold to 4000. Same 6000-token request is resent. |
| **Then** | Second request routes to cloud (6000 > 4000). |
| **Success** | Routing changed via Dashboard without restart. |

---

### 5.2 Performance Benchmarks (`bench/`)

#### `T5.2.1` — Proxy passthrough overhead < 5ms
| | |
|---|---|
| **Type** | Benchmark |
| **Given** | Mock backend with 0ms response time. |
| **When** | 1000 requests sent through Glider proxy. |
| **Then** | Median added latency (proxy overhead) < 5ms. p99 < 15ms. |
| **Success** | Meets latency targets. |

#### `T5.2.2` — Rule evaluation < 1ms (no Starlark)
| | |
|---|---|
| **Type** | Benchmark |
| **Given** | 10 rules (explicit, regex, context size). No Starlark scripts. |
| **When** | 1000 routing evaluations. |
| **Then** | Median evaluation time < 1ms. |
| **Success** | Meets latency target. |

#### `T5.2.3` — Starlark script execution < 5ms
| | |
|---|---|
| **Type** | Benchmark |
| **Given** | A typical routing Starlark script (string matching + conditional). |
| **When** | 1000 script executions (using cached compiled form). |
| **Then** | Median execution time < 5ms. |
| **Success** | Meets latency target. |

#### `T5.2.4` — Config hot-reload < 100ms
| | |
|---|---|
| **Type** | Benchmark |
| **Given** | A `glider.yaml` with 20 rules. |
| **When** | File is modified. |
| **Then** | New config is active within 100ms of file write. |
| **Success** | Reload latency < 100ms. |

---

### 5.3 Edge Cases & Resilience (`e2e/`)

#### `T5.3.1` — OOM during model load: graceful recovery
| | |
|---|---|
| **Type** | E2E |
| **Given** | Mock backend returns OOM error on LoadModel. |
| **When** | Request triggers model load. |
| **Then** | Model state returns to COLD. Error propagated. Fallback chain activates. System remains operational for subsequent requests. |
| **Success** | No crash. No stuck state. Subsequent requests work. |

#### `T5.3.2` — Backend crash mid-stream: partial response handling
| | |
|---|---|
| **Type** | E2E |
| **Given** | Backend sends 3 chunks then disconnects abruptly. |
| **When** | Proxy is streaming to client. |
| **Then** | Client receives the 3 chunks. Then receives an error event or connection close. No hang. |
| **Success** | Partial content delivered. Connection cleanly closed. |

#### `T5.3.3` — Corrupt glider.yaml: daemon stays alive
| | |
|---|---|
| **Type** | E2E |
| **Given** | Glider running with valid config. |
| **When** | `glider.yaml` is overwritten with binary garbage. |
| **Then** | Error is logged. Old config is preserved. Glider continues serving requests. |
| **Success** | No crash. Old config active. Requests still work. |

#### `T5.3.4` — Data race detection
| | |
|---|---|
| **Type** | Automated |
| **Given** | Full test suite. |
| **When** | `go test -race ./...` |
| **Then** | Zero data races detected. |
| **Success** | Exit code 0. No race warnings. |
| **Note** | On Windows without CGO, race detector may be unavailable — track as optional sign-off. |

---

## Phase 6 — Dual-mode MITM, Responses API, Model Aliases & Shared Harness

> **Phase is DONE when:** MITM CONNECT decrypts allowlisted hosts; **both modes use `PipelineCompleter`** (MITM via `CompleteLocal`); non-local MITM returns `ErrOriginPassthrough` → origin; gateway accepts Responses API; model aliases rewrite IDs; routing priority is explicit → script → threshold → default — with all tests below passing.

---

### 6.1 MITM CA & Host Allowlist (`internal/mitm/`)

#### `T6.1.1` — Generate and persist CA
| | |
|---|---|
| **Type** | Unit |
| **Given** | Empty CA paths under a temp dir |
| **When** | `LoadOrCreateAuthority(cert, key)` |
| **Then** | CA PEM written; reload returns the same cert |
| **Success** | Files exist; PEM non-empty; thumbprint stable across reload |

#### `T6.1.2` — Mint leaf cert for host
| | |
|---|---|
| **Type** | Unit |
| **Given** | Authority |
| **When** | `CertificateForHost("api2.cursor.sh")` |
| **Then** | Leaf CN/SAN matches host; signed by CA |
| **Success** | Second call returns cached cert |

#### `T6.1.3` — Host allowlist matching
| | |
|---|---|
| **Type** | Unit |
| **Given** | Patterns `*.cursor.sh`, `api.openai.com` |
| **When** | Match various hosts (with/without port) |
| **Then** | `api2.cursor.sh` and apex `cursor.sh` match; unrelated hosts do not |
| **Success** | Table-driven cases all pass |

---

### 6.2 MITM Proxy Behavior (`internal/mitm/`, `e2e/`)

#### `T6.2.1` — CONNECT + decrypt + origin passthrough
| | |
|---|---|
| **Type** | Integration / E2E |
| **Given** | Fake upstream TLS server; MITM with matching allowlist host; client trusts Glider CA |
| **When** | HTTPS GET through `http.proxy` |
| **Then** | Response body/headers from upstream |
| **Success** | Round-trip succeeds; upstream was hit |

#### `T6.2.2` — Blind tunnel for non-allowlisted host
| | |
|---|---|
| **Type** | Unit |
| **Given** | MITM with allowlist that does not match CONNECT target |
| **When** | CONNECT then raw bytes |
| **Then** | `200 Connection Established` and bytes tunnel without decrypt |
| **Success** | Echo/upstream receives payload |

#### `T6.2.3` — Local intercept short-circuit
| | |
|---|---|
| **Type** | Unit / E2E |
| **Given** | `LocalHandler` that always handles |
| **When** | POST `/v1/chat/completions` through MITM |
| **Then** | Local response returned; upstream not required |
| **Success** | Body matches stub |

#### `T6.2.4` — Interceptor: cloud target → passthrough signal
| | |
|---|---|
| **Type** | Unit |
| **Given** | Harness `CompleteLocal` returns `ErrOriginPassthrough` (cloud / non-local decision) |
| **When** | `Interceptor.TryHandle` |
| **Then** | `handled=false` (origin passthrough); body restored for upstream |
| **Success** | Harness called once; no local response written |

#### `T6.2.5` — Interceptor: `/local` → fulfill via shared harness
| | |
|---|---|
| **Type** | Unit |
| **Given** | Explicit `/local` rule; message contains `/local` |
| **When** | `Interceptor.TryHandle` |
| **Then** | `handled=true`; `CompleteLocal` invoked; chat JSON/SSE written |
| **Success** | Harness call count = 1 |

#### `T6.2.6` — Interceptor: context threshold drives local without `/local`
| | |
|---|---|
| **Type** | Unit |
| **Given** | Rules: context `<=8000` → local; no explicit command; small prompt |
| **When** | `Interceptor.TryHandle` via `PipelineCompleter.CompleteLocal` |
| **Then** | Local fulfillment (not `ErrOriginPassthrough`) |
| **Success** | Threshold rule matched; chunks returned |

#### `T6.2.7` — Interceptor: context overflow → origin passthrough
| | |
|---|---|
| **Type** | Unit |
| **Given** | Rules: context `>8000` → cloud; large estimated tokens |
| **When** | `CompleteLocal` / Interceptor |
| **Then** | `ErrOriginPassthrough` / `handled=false` |
| **Success** | No local execute

#### `T6.2.8` — Path classification: Agent RPC vs OpenAI vs control
| | |
|---|---|
| **Type** | Unit |
| **Given** | Paths: `/v1/chat/completions`, `/aiserver.v1.BidiService/BidiAppend`, `/aiserver.v1.DashboardService/...` |
| **When** | `ClassifyPath` / Interceptor `TryHandle` |
| **Then** | openai_compat → harness-eligible; agent_rpc → `skip_agent_rpc` Info; control → `skip_control` Debug; never `handled=true` for RPC |
| **Success** | Counters match; no request-log row for skips |

---

### 6.3 Responses API (`internal/api/`)

#### `T6.3.1` — Detect Responses-shaped body
| | |
|---|---|
| **Type** | Unit |
| **Given** | JSON with `input` and no `messages` |
| **When** | `LooksLikeResponses` |
| **Then** | Returns true; chat completions body returns false |
| **Success** | Both cases correct |

#### `T6.3.2` — Translate string `input` + instructions
| | |
|---|---|
| **Type** | Unit |
| **Given** | Responses JSON with `input: "hello"` and `instructions` |
| **When** | `ResponsesToCompletion` |
| **Then** | Messages include system + user; model/stream preserved |
| **Success** | No error; roles/content correct |

#### `T6.3.3` — `POST /v1/responses` returns Responses object
| | |
|---|---|
| **Type** | Unit (httptest) |
| **Given** | Stub Completer |
| **When** | `POST /v1/responses` |
| **Then** | HTTP 200; `object: response` |
| **Success** | JSON schema matches |

#### `T6.3.4` — Chat completions accepts Responses body
| | |
|---|---|
| **Type** | Unit (httptest) |
| **Given** | Responses-shaped POST to `/v1/chat/completions` |
| **When** | Handler runs |
| **Then** | Not 400 `missing messages`; Responses-shaped output |
| **Success** | Status 200 |

---

### 6.4 Model Aliases (`internal/orchestrator/`)

#### `T6.4.1` — Alias rewrites model and preserves OriginalModel
| | |
|---|---|
| **Type** | Unit |
| **Given** | `model_aliases: { "gpt-4o": "codellama:7b" }`; request model `gpt-4o` |
| **When** | `ApplyModelAlias` |
| **Then** | `req.Model == codellama:7b`; `OriginalModel == gpt-4o` |
| **Success** | Both fields set |

#### `T6.4.2` — Unknown model unchanged
| | |
|---|---|
| **Type** | Unit |
| **Given** | Alias map without key `claude-3` |
| **When** | `ApplyModelAlias` |
| **Then** | Model string unchanged |
| **Success** | No mutation |

---

### 6.5 Shared Harness (`internal/orchestrator/pipeline.go`)

#### `T6.5.1` — `Complete` executes cloud via BYOK path
| | |
|---|---|
| **Type** | Unit |
| **Given** | Router decides `target: cloud`; mock cloud executor |
| **When** | `PipelineCompleter.Complete` |
| **Then** | Chunks from cloud backend; no `ErrOriginPassthrough` |
| **Success** | Gateway semantics preserved |

#### `T6.5.2` — `CompleteLocal` maps cloud → `ErrOriginPassthrough`
| | |
|---|---|
| **Type** | Unit |
| **Given** | Same router decision `target: cloud` |
| **When** | `PipelineCompleter.CompleteLocal` |
| **Then** | Returns `errors.Is(err, ErrOriginPassthrough)`; executor not run |
| **Success** | MITM origin sentinel |

#### `T6.5.3` — `CompleteLocal` fulfills local through full pipeline
| | |
|---|---|
| **Type** | Unit |
| **Given** | Router decides `target: local` |
| **When** | `CompleteLocal` |
| **Then** | Chunks returned from local executor after alias/tokenize/route/transform |
| **Success** | Shared path with gateway local |

#### `T6.5.4` — Explicit override beats threshold
| | |
|---|---|
| **Type** | Unit |
| **Given** | Large context would match overflow→cloud; message has `/local` |
| **When** | Route via engine used by pipeline |
| **Then** | Decision target = local |
| **Success** | Explicit priority > threshold

---


## Test Tooling & Infrastructure

| Tool | Purpose |
|---|---|
| `testing` (stdlib) | Unit test framework |
| `net/http/httptest` | Mock HTTP servers for backend / gateway simulation |
| `crypto/tls` test servers | MITM upstream + client trust pool |
| `go test -race` | Data race detection (where CGO available) |
| `go test -bench` | Performance benchmarks |
| `go test -cover` | Coverage reporting (target ≥80%) |
| `gorilla/websocket` | WebSocket client for dashboard tests |

---

## Success Criteria Summary

| Phase | Test Count | Must Pass | Coverage Target |
|---|---|---|---|
| Phase 1 | 24 tests | 24/24 | ≥80% on `api/`, `backend/` |
| Phase 2 | 22 tests | 22/22 | ≥80% on `config/`, `router/`, `transform/tokenizer` |
| Phase 3 | 20 tests | 20/20 | ≥80% on `vram/`, `orchestrator/`, `backend/registry` |
| Phase 4 | 20 tests (incl. vram/sessions/gpu APIs) | 20/20 | ≥80% on `dashboard/`, `metrics/`, `transform/` |
| Phase 5 | 8 tests + 4 benchmarks | 8/8 + benches meet targets | ≥80% overall |
| Phase 6 | 18 tests (MITM + Responses + aliases + shared harness) | 18/18 | ≥80% on `mitm/`, `orchestrator` pipeline; Responses/alias in `api/` |
| **Total** | **112 tests + 4 benchmarks** | **All green** | **≥80% overall** |

**Manual:** `docs/CURSOR_CHECKLIST.md` (Mode A gateway + Mode B MITM Ask/Agent).
