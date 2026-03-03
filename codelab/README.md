# CloudInfer Codelabs

Welcome to the CloudInfer educational journey! 

CloudInfer is a **telemetry-driven inference control plane** for streaming LLM workloads. These codelabs are designed to take you from a curious developer to a production-grade infrastructure engineer, regardless of your starting point.

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
| 🟢 | **01** | **[Hello CloudInfer](./01-hello-cloudinfer)** | Run a mock sidecar on your laptop in 2 mins. |
| 🟡 | **02** | **Sidecars with Docker** | *Coming Soon:* Multi-container apps without K8s. |
| 🟡 | **03** | **Kubernetes Basics** | *Coming Soon:* Your first Pod and Readiness probes. |
| 🟠 | **04** | **Intelligent Routing** | *Coming Soon:* EWMA TTFT and dynamic failover. |
| 🔴 | **05** | **Safe Operations** | *Coming Soon:* SIGTERM drains and zero-drop rollouts. |

---

## 🛠️ Prerequisites
For most labs, you will only need:
1.  **Go 1.23+**
2.  **curl**
3.  A terminal and your favorite text editor.

*Later labs will introduce Docker and Kind (Kubernetes-in-Docker).*

---

## 🧪 CI-Verified
Every lab in this directory is automatically tested by our CI pipeline. If the code changes, the labs are updated to match. This means the instructions you see here are **guaranteed to work**.

**Ready to start?** Head over to **[Module 01: Hello CloudInfer](./01-hello-cloudinfer)**!
