import hashlib
import io
import re

html = io.open("index.html", encoding="utf-8").read()
match = re.search(r"<script>(.*?)</script>", html, re.S)
script = match.group(1)
for label, text in (("as-is", script), ("crlf->lf", script.replace("\r\n", "\n"))):
    digest = hashlib.sha256(text.encode("utf-8")).digest()
    import base64
    print(label, "sha256-" + base64.b64encode(digest).decode())

caddy = io.open("../deploy/production/Caddyfile", encoding="utf-8").read()
for found in re.findall(r"sha256-[A-Za-z0-9+/=]+", caddy):
    print("caddy has", found)
print("crlf in index.html:", "\r\n" in html)
