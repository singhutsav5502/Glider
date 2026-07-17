# large_file_router.star — Route large-context requests to cloud backends.
#
# Usage in glider.yaml:
#   trigger:
#     type: script
#     file: scripts/examples/large_file_router.star

LARGE_CONTEXT_THRESHOLD = 8000

def evaluate(request):
    tokens = request.estimated_tokens
    if tokens > LARGE_CONTEXT_THRESHOLD:
        return {
            "matched": True,
            "action": {
                "target": "cloud",
                "backend": "openai",
                "model": "gpt-4o",
            },
        }
    return {"matched": False}
