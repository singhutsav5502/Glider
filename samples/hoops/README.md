# Sample Loop Engineering hoops

Runnable hoop YAML for local smoke tests. See [docs/site/samples.html](../docs/site/samples.html).

```powershell
# Glider + Ollama must be up
.\glider.exe --config configs\glider.local.yaml

go run ./scripts/loadhoop -file samples/hoops/hello-critic.yaml -start
# or
powershell -File scripts\run-sample-hoop.ps1 -Name explain-snippet
```
