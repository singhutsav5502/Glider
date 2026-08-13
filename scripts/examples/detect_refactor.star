# detect_refactor.star — Route refactor/rename/extract requests to local code model.
#
# Usage in glider.yaml:
#   trigger:
#     type: script
#     file: scripts/examples/detect_refactor.star
#
# The model below must match the rule's action.model in the config. A script
# that returns an action wins outright over the config block (see
# router.StarlarkScriptRule.Evaluate), so a mismatch routes somewhere the
# config never names.

load("re.star", "re")

REFACTOR_PATTERN = re.compile(r"(?i)\b(refactor|rename|extract)\b")

def evaluate(request):
    content = ""
    for msg in request.messages:
        content = content + msg.content

    if REFACTOR_PATTERN.search(content):
        return {
            "matched": True,
            "action": {
                "target": "local",
                "model": "qwen2.5-coder:14b",
            },
        }

    return {"matched": False}
