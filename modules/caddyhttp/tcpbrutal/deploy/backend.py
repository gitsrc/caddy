#!/usr/bin/env python3
"""Tiny backend for the reverse_proxy test: echoes requests as JSON."""
import hashlib
import json
import sys
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import urlparse


class Handler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def _respond(self, obj, code=200):
        body = json.dumps(obj, indent=2, sort_keys=True).encode()
        self.send_response(code)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_GET(self):  # noqa: N802
        self._respond({
            "method": "GET",
            "path": urlparse(self.path).path,
            "query": urlparse(self.path).query,
            "host": self.headers.get("Host"),
            "xff": self.headers.get("X-Forwarded-For"),
            "proto": self.headers.get("X-Forwarded-Proto"),
            "user_agent": self.headers.get("User-Agent"),
            "backend": "python3",
        })

    def do_POST(self):  # noqa: N802
        n = int(self.headers.get("Content-Length") or 0)
        data = self.rfile.read(n) if n else b""
        self._respond({
            "method": "POST",
            "path": urlparse(self.path).path,
            "bytes": len(data),
            "sha256": hashlib.sha256(data).hexdigest(),
            "backend": "python3",
        })

    def log_message(self, fmt, *args):  # keep the journal readable
        sys.stderr.write("backend %s - %s\n" % (self.address_string(), fmt % args))


if __name__ == "__main__":
    srv = ThreadingHTTPServer(("127.0.0.1", 9100), Handler)
    print("backend listening on 127.0.0.1:9100", flush=True)
    srv.serve_forever()
