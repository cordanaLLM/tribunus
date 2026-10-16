# SPDX-License-Identifier: EUPL-1.2
"""Answers Ollama's /api/tags and /api/ps with one fixed model, for generate.sh."""
import http.server
import json
import sys

TAGS = {"models": [{"name": "fixture-model:7b", "model": "fixture-model:7b", "size": 4109865159,
                    "details": {"family": "llama", "parameter_size": "7B", "quantization_level": "Q4_K_M"}}]}
PS = {"models": [{"name": "fixture-model:7b", "model": "fixture-model:7b", "size_vram": 4109865159}]}


class Handler(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        body = {"/api/tags": TAGS, "/api/ps": PS}.get(self.path)
        if body is None:
            self.send_response(404)
            self.end_headers()
            return
        data = json.dumps(body).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def log_message(self, *args):
        pass


server = http.server.HTTPServer(("127.0.0.1", 0), Handler)
print(server.server_address[1], flush=True)
sys.stdout.close()
server.serve_forever()
