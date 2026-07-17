# Glider: AI Harness & Proxy Orchestrator

This document outlines the architecture, infrastructure, and technical implementation plan for **Glider**, a local AI harness that intercepts Cursor Chat requests, manages context limits, and dynamically routes tasks to local SLMs/LoRAs or heavy cloud models while optimizing VRAM usage.

## Planned Features

- **Proxy/Orchestrator:** Intercepts OpenAI-compatible requests from Cursor.
- **Dynamic VRAM Management:** Load models/LoRAs into VRAM only when needed and unload them after a timeout (Scale-to-Zero).
- **Rule Engine with Python Scripting:** Explicit and implicit routing based on regex, context size, and custom Python scripts executed dynamically.
- **Context Thresholding:** Configurable limits (e.g., >8000 tokens goes to cloud) to prevent local models from freezing due to massive prompt sizes.
- **Observability Dashboard (Web UI):** A graphical interface to:
  - Configure routing rules and context thresholds.
  - Set VRAM allotments and Permutations/Combinations (PnCs) for models and adapters.
  - View real-time logs, active routes, VRAM usage, and token consumption.
- **LoRA Hot-Swapping:** Quickly switch task-specific LoRAs on a persistent base model.

## User Review Required

> [!IMPORTANT]
> **Proxy Language & Python Scripting Integration**
> To fulfill your requirement for custom Python scripting in the rule engine, we have two primary architectural paths:
> 
> **Path A: Python Proxy (FastAPI)**
> We build the entire proxy in Python. This allows native, instant execution of custom Python routing scripts. It has supreme compatibility with the ML ecosystem (e.g., using `LiteLLM` for routing). The trade-off is slightly higher memory overhead compared to compiled languages.
> 
> **Path B: Go Proxy + embedded Starlark**
> We build the proxy in Go (for maximum memory efficiency and speed) and embed `Starlark` (a dialect of Python designed for configuration). Users can write Python-like scripts for rules, which are executed safely and instantly inside the Go runtime without spawning heavy Python subprocesses.
> 
> *Recommendation:* **Path B (Go + Starlark)** perfectly balances your requirements for "good memory management" and "Python scripting". Please confirm which path you prefer.

> [!WARNING]
> **Inference Engine Dependency**
> I propose using **Ollama** as the local inference engine dependency. It wraps `llama.cpp` but provides stable HTTP APIs for model loading, LoRA swapping, and native VRAM management (keep_alive).

## Architecture & Infrastructure Plan

The system consists of four distinct layers:

### 1. The Proxy & Orchestrator (Glider Core)
This is a local daemon (e.g., running on `localhost:8080`) that acts as a fake OpenAI endpoint for Cursor.
*   **API Interface:** Implements `POST /v1/chat/completions`.
*   **Tokenizer Module:** A fast BPE tokenizer to calculate incoming request sizes *before* routing.
*   **Rules Engine (Starlark/Python):** Evaluates the payload against configuration rules. It can run custom Python scripts to determine the routing target based on prompt contents.
*   **VRAM State Manager:** Keeps a lightweight state of the Inference Engine to orchestrate model loading/unloading based on user PnCs.

### 2. The Observability & Configuration Web UI
A local dashboard served by the Proxy.
*   **Frontend:** HTML/JS/CSS (Vanilla or lightweight React/Vite) served directly from the proxy binary.
*   **Capabilities:** 
    * Real-time monitoring of VRAM, active LLMs, and token flow.
    * UI to edit `glider.yaml` rules, VRAM allotments, and paste in custom Python scripts for the Rule Engine.

### 3. The Inference Engine (Local Execution)
*   **Backend:** Ollama (or directly `llama.cpp` server).
*   **Base Models:** Kept "warm" in VRAM (configurable static/dynamic).
*   **LoRAs / Adapters:** Hot-swapped via API calls.
*   **SLMs:** Pushed into RAM/VRAM on-demand for specific deterministic rules.

### 4. Fallback Tier (Cloud/Heavy)
*   Forwards unaltered requests to actual OpenAI/Anthropic APIs when thresholds are exceeded or complexity demands it.

## Interfaces & Configuration Map

### 1. Configuration (`glider.yaml`)
Users will define their PnCs and basic rules here. The UI will expose these settings.

```yaml
server:
  port: 8080

thresholds:
  max_local_context_tokens: 8000
  vram_allocation_strategy: "dynamic"

routing:
  rules:
    - name: "Explicit Local"
      trigger: 
        type: "regex"
        pattern: "^/local.*"
      action:
        target: "local"
        model: "llama3:8b"
        adapter: "general-coding"
        
    - name: "Script Evaluator"
      trigger:
        type: "script"
        file: "scripts/detect_refactor.py" # Custom python script
      action:
        target: "local"
        model: "llama3:8b"
        adapter: "refactor-lora"
```

### 2. Custom Script Interface (Python/Starlark)
The Rule Engine will pass the request context to the script, which must return a routing decision:

```python
# scripts/detect_refactor.py
def evaluate(request):
    # request is a dict containing the OpenAI payload and token estimates
    prompt_text = request["messages"][-1]["content"].lower()
    
    # If the user is just asking to refactor and the file isn't huge
    if "refactor" in prompt_text and request["estimated_tokens"] < 3000:
        return {
            "matched": True, 
            "action": {
                "target": "local", 
                "model": "llama3:8b", 
                "adapter": "refactor-lora"
            }
        }
    
    return {"matched": False}
```

### 3. Data Flow
1. **Receive:** Cursor -> `POST localhost:8080/v1/chat/completions`.
2. **Tokenize:** Glider estimates token count.
3. **Evaluate:** Match against `glider.yaml` rules. Run custom Python scripts if specified in the rules list.
4. **Orchestrate:** Send commands to Ollama to allocate VRAM and preload models/LoRAs.
5. **Execute:** Stream response from target back to Cursor. Dashboard visualizes this flow in real-time.

## Verification Plan
*   **Automated Tests:** Verify the Rule Engine can safely execute a Python script and route accordingly based on the script's return value.
*   **Manual Verification:** Run the daemon, open the Web UI, adjust a VRAM parameter, and send a Cursor request to observe the real-time logger trace the routing decision.
