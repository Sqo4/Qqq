#!/usr/bin/env python3
"""Download a pinned llama.cpp release build and unpack llama-server with its
libraries into OUT_DIR. Used by the release workflow.

    python3 fetch_engine.py TAG ASSET_REGEX OUT_DIR

GITHUB_TOKEN is used for the API if set. The SHA-256 of the archive is
checked against the digest GitHub publishes for the asset and printed, so it
ends up in the release notes.
"""
import hashlib, io, json, os, re, shutil, sys, tarfile, tempfile, urllib.request, zipfile


def api(url):
    req = urllib.request.Request(url, headers={"Accept": "application/vnd.github+json"})
    if os.environ.get("GITHUB_TOKEN"):
        req.add_header("Authorization", "Bearer " + os.environ["GITHUB_TOKEN"])
    with urllib.request.urlopen(req) as r:
        return json.load(r)


def main():
    tag, pattern, out = sys.argv[1], sys.argv[2], sys.argv[3]
    rel = api(f"https://api.github.com/repos/ggml-org/llama.cpp/releases/tags/{tag}")
    assets = [a for a in rel["assets"] if re.fullmatch(pattern, a["name"])]
    if not assets:
        names = "\n  ".join(a["name"] for a in rel["assets"])
        sys.exit(f"no asset matches {pattern!r} in {tag}; available:\n  {names}")
    asset = assets[0]
    print(f"downloading {asset['name']} ({asset['size']} bytes)")
    with urllib.request.urlopen(asset["browser_download_url"]) as r:
        data = r.read()
    digest = hashlib.sha256(data).hexdigest()
    published = (asset.get("digest") or "").removeprefix("sha256:")
    if published and published != digest:
        sys.exit(f"sha256 mismatch: got {digest}, GitHub says {published}")
    print(f"sha256 {digest}  {asset['name']}")

    tmp = tempfile.mkdtemp()
    if asset["name"].endswith(".zip"):
        zipfile.ZipFile(io.BytesIO(data)).extractall(tmp)
    else:
        tf = tarfile.open(fileobj=io.BytesIO(data))
        try:
            tf.extractall(tmp, filter="data")
        except TypeError:  # Python without extraction filters (macOS system python)
            tf.extractall(tmp)

    server = None
    for root, _, files in os.walk(tmp):
        for f in files:
            if f in ("llama-server", "llama-server.exe"):
                server = os.path.join(root, f)
    if not server:
        sys.exit("llama-server not found in the archive")
    src = os.path.dirname(server)
    os.makedirs(out, exist_ok=True)
    # Keep only the server and the shared libraries it needs.
    keep = re.compile(r"((llama|ggml-rpc|rpc)-server(\.exe)?|.*\.(dll|so(\.\d+)*|dylib|metal|metallib)|LICENSE.*)$")
    for f in os.listdir(src):
        p = os.path.join(src, f)
        if os.path.isfile(p) and keep.fullmatch(f):
            shutil.copy2(p, os.path.join(out, f))
    for root, _, files in os.walk(tmp):
        for f in files:
            if f.startswith("LICENSE") and not os.path.exists(os.path.join(out, "LICENSE-llama.cpp")):
                shutil.copy2(os.path.join(root, f), os.path.join(out, "LICENSE-llama.cpp"))
    for f in os.listdir(out):
        if f.endswith("-server") or f.endswith("-server.exe"):
            os.chmod(os.path.join(out, f), 0o755)
    if not any("rpc-server" in f for f in os.listdir(out)):
        print("WARNING: this build has no rpc-server; the big shared model will not work")
    with open(os.path.join(out, "ENGINE_VERSION.txt"), "w") as fh:
        fh.write(f"llama.cpp {tag}\n{asset['name']}\nsha256 {digest}\n")
    print("engine files:", ", ".join(sorted(os.listdir(out))))


if __name__ == "__main__":
    main()
