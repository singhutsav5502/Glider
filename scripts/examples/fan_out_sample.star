# Sample Starlark: opt into StrategyFanOut for multi-perspective prompts.
# Wire via a routing rule with trigger.type=script and this file path.
# Requires orchestration.fan_out.enabled: true (default in configs/glider.yaml).

def evaluate(request):
    content = ""
    for m in request.get("messages", []):
        if m.get("role") == "user":
            content += str(m.get("content", "")) + " "
    lower = content.lower()
    if "fanout" in lower or "fan-out" in lower or "multi-agent review" in lower or "/swarm" in lower:
        return {
            "matched": True,
            "action": {
                "target": "local",
                "strategy": "fan_out",
                "reason": "starlark_fan_out_sample",
            },
        }
    return {"matched": False}
