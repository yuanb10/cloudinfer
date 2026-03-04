import http.client
import json
import sys


def main() -> int:
    conn = http.client.HTTPConnection("127.0.0.1", 8080, timeout=10)
    body = json.dumps(
        {
            "model": "default",
            "stream": True,
            "messages": [{"role": "user", "content": "What's your name?"}],
        }
    )

    conn.request(
        "POST",
        "/v1/chat/completions",
        body=body,
        headers={"Content-Type": "application/json"},
    )
    resp = conn.getresponse()

    print(f"HTTP {resp.status}")
    request_id = resp.getheader("X-Request-Id", "")
    if request_id:
        print(f"X-Request-Id: {request_id}")

    body_text = resp.read().decode("utf-8", errors="replace")
    for text in body_text.splitlines():
        if text:
            print(text)

    conn.close()
    return 0


if __name__ == "__main__":
    sys.exit(main())
