# fanout_dual_view.star — Opt-in gateway StrategyFanOut (2 workers, text merge).
#
# Trigger when the user message contains /swarm or [fanout] (case-insensitive).
# Requires orchestration.fan_out.enabled: true in glider.yaml.
#
# Usage in glider.yaml:
#   - name: Sample FanOut Dual View
#     priority: 55
#     enabled: true
#     trigger:
#       type: script
#       file: scripts/examples/fanout_dual_view.star
#     action:
#       target: local
#       strategy: fan_out
#       model: codellama:7b

def evaluate(request):
    content = ""
    for msg in request.messages:
        content = content + msg.content
    lower = content.lower()
    if "/swarm" in lower or "[fanout]" in lower:
        return {
            "matched": True,
            "action": {
                "target": "local",
                "strategy": "fan_out",
                "model": "codellama:7b",
            },
        }
    return {"matched": False}
