# detect_refactor.star — Route refactor/rename/extract requests to local code model.
#
# Usage in glider.yaml:
#   trigger:
#     type: script
#     file: scripts/examples/detect_refactor.star

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
                "model": "codellama:7b",
            },
        }

    return {"matched": False}
