# Local Builder LM (llama.cpp) — setup notes

Model and binaries are **not** in git.

## Runtime

- Homebrew: `llama.cpp` 0.4.1 (`llama-server`)
- Do **not** use Ollama for this path

## Model (outside repo)

```bash
mkdir -p ~/.cache/builder-lm
curl -L -o ~/.cache/builder-lm/qwen2.5-0.5b-instruct-q4_k_m.gguf \
  https://huggingface.co/Qwen/Qwen2.5-0.5B-Instruct-GGUF/resolve/main/qwen2.5-0.5b-instruct-q4_k_m.gguf
```

## Start server

```bash
llama-server \
  --model ~/.cache/builder-lm/qwen2.5-0.5b-instruct-q4_k_m.gguf \
  --alias qwen2.5-0.5b-instruct \
  --host 127.0.0.1 \
  --port 8090 \
  --ctx-size 2048 \
  --n-predict 400 \
  --threads 6
```

## App config (default OFF)

```
BUILDER_LOCAL_LM_ENABLED=false
BUILDER_LOCAL_LM_PROVIDER=llamacpp
BUILDER_LOCAL_LM_URL=http://127.0.0.1:8090/v1
BUILDER_LOCAL_LM_MODEL=qwen2.5-0.5b-instruct
BUILDER_LOCAL_LM_TIMEOUT_MS=1500
```
