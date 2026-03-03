# Codelab: Hello CloudInfer

Welcome to the CloudInfer educational journey! 

CloudInfer is a **telemetry-driven inference control plane** for streaming LLM workloads. These codelabs are designed to take you from a curious developer to a production-grade infrastructure engineer, regardless of your starting point.

This first lab is for anyone who wants to see **CloudInfer** in action on their laptop in under 2 minutes. **No Kubernetes, no Docker, and no API keys required.**

---

## 🚀 Who is this for?
- **LLM Developers:** Learn how to make your apps faster and more reliable.
- **Backend Engineers:** Discover how to manage multiple AI providers without complex code.
- **SRE/Platform Engineers:** Master Kubernetes-native LLM orchestration and lifecycle.
- **Beginners:** We have "No-K8s" paths specifically for you!

---

## 🗺️ The Learning Path

Our labs are progressive. Each one builds on the last:

| Level | Module | Title | Key Learning |
| :--- | :--- | :--- | :--- |
| 🟢 | **01** | **Hello CloudInfer** | Run a mock sidecar on your laptop in 2 mins. |
| 🟡 | **02** | **Sidecars with Docker** | *Coming Soon:* Multi-container apps without K8s. |
| 🟡 | **03** | **Kubernetes Basics** | *Coming Soon:* Your first Pod and Readiness probes. |
| 🟠 | **04** | **Intelligent Routing** | *Coming Soon:* EWMA TTFT and dynamic failover. |
| 🔴 | **05** | **Safe Operations** | *Coming Soon:* SIGTERM drains and zero-drop rollouts. |

---

## Prerequisites
- **Go 1.23+** installed on your laptop.
- A terminal with `curl` available.
- Run the commands below from the repository root.

---

## 1. Build CloudInfer
First, let's build the binary from the source code.

```bash
go build -o ./codelab/01-hello-cloudinfer/cloudinfer ./cmd/cloudinfer
```

You now have a single, self-contained `cloudinfer` file in `codelab/01-hello-cloudinfer/`.

---

## 2. Start the Server
Now, let's run CloudInfer using the "Hello World" configuration provided in this folder. This config uses CloudInfer's built-in **mock mode**, which means it will simulate a real AI provider response locally.

```bash
./codelab/01-hello-cloudinfer/cloudinfer -config ./codelab/01-hello-cloudinfer/router.yaml
```

**Wait for the log message:**  
`READY: starting cloudinfer server on 127.0.0.1:8080`

---

## 3. Send a Streaming Request
Open a **new terminal window** and send a streaming request to CloudInfer. We'll use `curl` to talk to it just like you would with OpenAI. `-D -` prints the response headers first so you can see the request ID.

```bash
curl -N -D - http://127.0.0.1:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
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
- **Mock Mode:** Since we didn't configure any backends, CloudInfer generated a simple "hello" response locally.

### Check the Server Logs
Switch back to your first terminal window. You'll see structured logs for the request:
- `ttft_ms`: Time-To-First-Token. This is the most important metric for LLM UX.
- `total_latency_ms`: How long the entire response took.
- `status`: Should be `ok`.

---

## 🧪 CI-Verified
Every lab in this directory is automatically tested by our CI pipeline. If the code changes, the labs are updated to match. This means the instructions you see here are **guaranteed to work**.

---

## Summary
You just successfully:
1.  **Built** CloudInfer from source.
2.  **Configured** it to run in built-in mock mode.
3.  **Interacted** with it using a standard OpenAI-compatible API.

**Next Step:** In the next lab, we'll see how to run this same pattern inside **Docker Compose** to simulate a real multi-container application.

---

## Cleanup
Go back to the terminal where CloudInfer is running and press `Ctrl+C` to stop it.
You can also remove the lab binary if you want:

```bash
rm -f ./codelab/01-hello-cloudinfer/cloudinfer
```
