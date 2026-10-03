import http.server
import os

GREETING = "hello-v1"
VERSION = os.environ.get("APP_VERSION", "unset")
STAMP = open("/deps-stamp").read().strip()


class H(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        self.send_response(200)
        self.end_headers()
        self.wfile.write(f"{GREETING} {VERSION}\n".encode())


print(f"greeter greeting={GREETING} version={VERSION} built_for={os.environ.get('BUILT_FOR')} {STAMP}", flush=True)
http.server.HTTPServer(("", 8080), H).serve_forever()
