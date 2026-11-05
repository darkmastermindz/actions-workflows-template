#!/usr/bin/env python3
"""Tiny local mock of the GitHub REST API used by the offline tests.

Usage: mock_api.py PORT_FILE

Control endpoints (not part of the GitHub API):
  POST /__load   body = fixture JSON, resets the request log
  GET  /__log    returns the request log as JSON

Fixture format:
  {"routes": {"GET /repos/o/r/actions/runs/1/jobs?per_page=100&page=1":
                 {"status": 200, "body": {...}}            # or
                 [{"status": 500}, {"status": 200, "body": {...}}],   # sequence
              "POST /repos/o/r/issues/42/comments": {"status": 201, "body": {}}},
   "files": {"<route key>": "/path/to/body.json"}}         # raw body from file
Unknown routes answer 404. A sequence is consumed one response per request;
its last entry repeats.
"""
import json
import sys
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

STATE = {"routes": {}, "files": {}, "log": [], "counters": {}}
LOCK = threading.Lock()


class Handler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def log_message(self, *args):  # silence
        pass

    def _send(self, status, payload, ctype="application/json"):
        data = payload if isinstance(payload, bytes) else json.dumps(payload).encode()
        self.send_response(status)
        self.send_header("Content-Type", ctype)
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def _handle(self, method):
        length = int(self.headers.get("Content-Length") or 0)
        body = self.rfile.read(length) if length else b""
        if self.path == "/__load" and method == "POST":
            fx = json.loads(body)
            with LOCK:
                STATE["routes"] = fx.get("routes", {})
                STATE["files"] = fx.get("files", {})
                STATE["log"] = []
                STATE["counters"] = {}
            return self._send(200, {"ok": True})
        if self.path == "/__log":
            with LOCK:
                return self._send(200, STATE["log"])
        key = f"{method} {self.path}"
        with LOCK:
            STATE["log"].append({
                "method": method,
                "path": self.path,
                "body": body.decode("utf-8", "replace"),
                "authorization": self.headers.get("Authorization"),
                "accept": self.headers.get("Accept"),
                "api_version": self.headers.get("X-GitHub-Api-Version"),
                "user_agent": self.headers.get("User-Agent"),
                "content_type": self.headers.get("Content-Type"),
            })
            if key in STATE["files"]:
                with open(STATE["files"][key], "rb") as fh:
                    return self._send(200, fh.read())
            route = STATE["routes"].get(key)
            if isinstance(route, list):
                i = STATE["counters"].get(key, 0)
                STATE["counters"][key] = i + 1
                route = route[min(i, len(route) - 1)]
        if route is None:
            return self._send(404, {"message": "Not Found"})
        if route.get("raw") is not None:
            return self._send(route.get("status", 200), route["raw"].encode())
        return self._send(route.get("status", 200), route.get("body", {}))

    def do_GET(self):
        self._handle("GET")

    def do_POST(self):
        self._handle("POST")

    def do_PATCH(self):
        self._handle("PATCH")


def main():
    srv = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
    with open(sys.argv[1] + ".tmp", "w") as fh:
        fh.write(str(srv.server_address[1]))
    import os
    os.replace(sys.argv[1] + ".tmp", sys.argv[1])
    srv.serve_forever()


if __name__ == "__main__":
    main()
