# Codelab: Hello CloudInfer

Welcome! This lab is for anyone who wants to see **CloudInfer** in action on their laptop in under 2 minutes. **No Kubernetes, no Docker, and no API keys required.**

---

## Prerequisites
- **Go 1.23+** installed on your laptop.
- A terminal with `curl` available.

---

## 1. Build CloudInfer
First, let's build the binary from the source code.

```bash
go build -o cloudinfer ./cmd/cloudinfer
```

You now have a single, self-contained `cloudinfer` file in your current directory.

---

## 2. Start the Server
Now, let's run CloudInfer using the "Hello World" configuration provided in this folder. This config uses a **mock backend**, which means CloudInfer will simulate a real AI provider response locally.

```bash
./cloudinfer -config router.yaml
```

**Wait for the log message:**  
`READY: starting cloudinfer server on 127.0.0.1:8080`

---

## 3. Send a Streaming Request
Open a **new terminal window** and send a streaming request to CloudInfer. We'll use `curl` to talk to it just like you would with OpenAI.

```bash
curl -N http://127.0.0.1:8080/v1/chat/completions 
  -H "Content-Type: application/json" 
  -d '{
    "model": "default",
    "stream": true,
    "messages": [{"role": "user", "content": "Hello!"}]
  }'
```

---

## 4. What just happened?
Look closely at the output in your terminal. You should see chunks of data arriving:

- **Tokens:** You'll see `data: {"id":"chatcmpl-...", "choices": [{"delta": {"content": "h"}}...]}`. This is **Server-Sent Events (SSE)**, the standard way to stream LLM responses.
- **Request IDs:** Every response has a unique `X-Request-Id`. This is critical for tracking and debugging in production.
- **Mock Mode:** Since we didn't provide an API key, CloudInfer's mock engine generated a simple "hello" response.

### Check the Server Logs
Switch back to your first terminal window. You'll see structured logs for the request:
- `ttft_ms`: Time-To-First-Token. This is the most important metric for LLM UX.
- `total_latency_ms`: How long the entire response took.
- `status`: Should be `ok`.

---

## Summary
You just successfully:
1.  **Built** CloudInfer from source.
2.  **Configured** it to run a mock sidecar.
3.  **Interacted** with it using a standard OpenAI-compatible API.

**Next Step:** In the next lab, we'll see how to run this same pattern inside **Docker Compose** to simulate a real multi-container application.

---

## Cleanup
Go back to the terminal where CloudInfer is running and press `Ctrl+C` to stop it.
