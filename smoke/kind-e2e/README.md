# kind E2E Smoke

This smoke check exercises the Kubernetes deployment path with the dev overlay.

What it does:

- builds a local CloudInfer image
- loads it into kind
- runs `kubectl apply -k deploy/overlays/dev`
- updates the Deployment to the locally built image
- sends streaming requests through the Service during a rollout restart
- fails if streams stop completing or `/metrics` regresses its content type

Run it:

```bash
bash smoke/kind-e2e/verify.sh
```
