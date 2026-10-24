#!/usr/bin/env python3
"""Apply one reviewed Casdoor 4.23.0 same-origin OAuth compatibility exception.

Operate only on a complete public release/casdoor-web copy after branding. No
container, private configuration, account, OAuth server or Go code is accessed.
The pinned upstream helper treats every same-origin path as its own login when
BasePath is empty. UniHub's exact public callback belongs to the console and
must retain the authorization-code request type.
"""

import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import sys
import tempfile


MANIFEST_NAME = "unihub-identity-manifest.json"
SOURCE_PATH = "assets/index-cnXxJbP8.unihub-ad7cb130c0251a1a.js"
SOURCE_SHA256 = "551d29642ebe1dd4afb6821f56ece30ba74ba5d4104b772d332408f1205fd2db"
BRANDING_REVISION = "ad7cb130c0251a1a"
ORIGINAL = ('function LG(e){if(!e)return!1;try{const t=new URL(e);return '
            't.origin!==new URL(E0()).origin?!1:Cn===""||t.pathname===Cn||'
            't.pathname.startsWith(`${Cn}/`)}catch{return!1}}')
EXCEPTION = ('if(t.origin==="https://unihub.quukk.com"&&t.origin===window.location.origin'
             '&&t.pathname==="/auth/callback"&&!t.search&&!t.hash&&!t.username&&!t.password)return!1;')
PATCHED = ORIGINAL.replace("const t=new URL(e);", "const t=new URL(e);" + EXCEPTION)
SPEC = {
    "id": "unihub-casdoor-4.23.0-same-origin-callback-v1",
    "upstream_version": "v4.23.0",
    "upstream_source": "https://github.com/casdoor/casdoor/blob/v4.23.0/web/src/lib/setting.tsx",
    "helper": "isSelfRedirectUri (compiled LG)",
    "exception": {"origin": "https://unihub.quukk.com", "same_window_origin": True,
                  "pathname": "/auth/callback", "search": "", "hash": "",
                  "username": "", "password": "", "result": False},
    "replacement_count": 1,
    "original_function_sha256": hashlib.sha256(ORIGINAL.encode()).hexdigest(),
    "patched_function_sha256": hashlib.sha256(PATCHED.encode()).hexdigest(),
}


def digest(data):
    return hashlib.sha256(data).hexdigest()


def checked_tree(web):
    if web.is_symlink() or not web.is_dir():
        raise RuntimeError("Public web directory is missing or is a symlink")
    paths = list(web.rglob("*"))
    if any(path.is_symlink() for path in paths):
        raise RuntimeError("Public web tree contains a symlink")
    return {str(path.relative_to(web)): path for path in paths if path.is_file()}


def verified_repeat(web, files):
    report = json.loads(files[MANIFEST_NAME].read_text())
    if report.get("spec") != SPEC or report.get("main", {}).get("source_path") != SOURCE_PATH:
        raise RuntimeError("Identity manifest does not match the reviewed patch specification")
    if report["main"].get("source_sha256") != SOURCE_SHA256:
        raise RuntimeError("Identity manifest source SHA-256 does not match reviewed branding")
    source = files.get(SOURCE_PATH)
    if source is None or digest(source.read_bytes()) != SOURCE_SHA256:
        raise RuntimeError("Identity source SHA-256 changed after preparation")
    outputs = report.get("outputs", [])
    if not outputs or "index.html" not in {entry.get("path") for entry in outputs}:
        raise RuntimeError("Identity manifest is missing reviewed outputs")
    for entry in outputs:
        path = files.get(entry["path"])
        if path is None or digest(path.read_bytes()) != entry["output_sha256"]:
            raise RuntimeError("Identity output SHA-256 differs from the preparation manifest")
    main = report["main"]["output_path"]
    if main not in {entry["path"] for entry in outputs} or PATCHED not in files[main].read_text():
        raise RuntimeError("Identity prepared main bundle is missing the reviewed exception")
    return report


def atomic_write(path, content):
    descriptor, temporary = tempfile.mkstemp(prefix=".unihub-identity-", dir=path.parent)
    try:
        with os.fdopen(descriptor, "wb") as stream:
            stream.write(content)
        os.chmod(temporary, 0o644)
        os.replace(temporary, path)
    finally:
        if os.path.exists(temporary):
            os.unlink(temporary)


def prepare(web):
    """Validate the entire transformation before publishing; preserve old URLs."""
    web = Path(web)
    files = checked_tree(web)
    if MANIFEST_NAME in files:
        return verified_repeat(web, files)
    source = files.get(SOURCE_PATH)
    if source is None or digest(source.read_bytes()) != SOURCE_SHA256:
        raise RuntimeError("Casdoor branding source SHA-256 differs from the reviewed bundle")
    text = source.read_text()
    count = text.count(ORIGINAL)
    if count != 1:
        raise RuntimeError(f"Casdoor isSelfRedirectUri: expected 1 reviewed function, found {count}")
    index = files.get("index.html")
    if index is None or index.read_text().count("/" + SOURCE_PATH) != 1:
        raise RuntimeError("Casdoor index must reference the reviewed source bundle exactly once")
    active_suffix = ".unihub-" + BRANDING_REVISION
    assets = {relative: path.read_bytes() for relative, path in files.items()
              if relative.startswith("assets/") and path.suffix in {".js", ".css"}
              and path.stem.endswith(active_suffix)}
    # A shared revision versions every importer in the connected module graph,
    # including otherwise unchanged lazy chunks with immutable cache headers.
    revision_input = json.dumps(SPEC, sort_keys=True).encode()
    for relative, data in sorted(assets.items()):
        revision_input += relative.encode() + b"\0" + data + b"\0"
    revision = digest(revision_input)[:16]
    mapping = {relative: relative.replace(active_suffix + Path(relative).suffix,
                                         ".unihub-" + revision + Path(relative).suffix)
               for relative in assets}
    basenames = {Path(old).name: Path(new).name for old, new in mapping.items()}
    if len(basenames) != len(mapping):
        raise RuntimeError("Casdoor module basenames are not unique")
    names = re.compile(r"(?<![A-Za-z0-9_.-])(?:" + "|".join(map(re.escape, basenames))
                       + r")(?![A-Za-z0-9_.-])")

    def rewrite(data):
        return names.sub(lambda match: basenames[match.group(0)], data.decode()).encode()

    outputs = {}
    sources = {}
    for relative, data in assets.items():
        if relative == SOURCE_PATH:
            data = data.decode().replace(ORIGINAL, PATCHED).encode()
        output = mapping[relative]
        outputs[output] = rewrite(data)
        sources[output] = {"source_path": relative, "source_sha256": digest(assets[relative])}
    # Root helper scripts and HTML entry points can import the graph too. Old
    # versioned assets remain byte-identical for tabs opened before publication.
    for relative, path in files.items():
        if relative.startswith(("assets/", "swagger/")) or path.suffix not in {".html", ".js", ".css"}:
            continue
        data = path.read_bytes()
        updated = rewrite(data)
        if updated != data:
            outputs[relative] = updated
            sources[relative] = {"source_path": relative, "source_sha256": digest(data)}
    if "index.html" not in outputs:
        raise RuntimeError("Casdoor entry point was not updated")
    main_output = mapping[SOURCE_PATH]
    if outputs[main_output].decode().count(PATCHED) != 1:
        raise RuntimeError("Casdoor patched function must occur exactly once in output")
    # Reject missing imports and incomplete graph rewrites before the first write.
    asset_literal = re.compile(r'''["']((?:\./|/assets/|assets/)[^"'\s]+\.(?:js|css|woff2)(?:\?[^"'\s]*)?)["']''')
    for relative, data in outputs.items():
        output_text = data.decode()
        if names.search(output_text):
            raise RuntimeError("A stale Casdoor module reference remains in output")
        for match in asset_literal.finditer(output_text):
            url = match[1].split("?", 1)[0]
            target = url.lstrip("/") if url.startswith(("/", "assets/")) else str(Path(relative).parent / url)
            if target not in outputs and target not in files:
                raise RuntimeError("Prepared Casdoor module references a missing public asset")
        if relative in files and relative.startswith("assets/") and files[relative].read_bytes() != data:
            raise RuntimeError("Prepared Casdoor asset URL already contains different content")
    report = {"version": 1, "spec": SPEC, "revision": revision,
              "main": {"source_path": SOURCE_PATH, "source_sha256": SOURCE_SHA256,
                       "output_path": main_output, "output_sha256": digest(outputs[main_output])},
              "asset_mapping": mapping,
              "outputs": [{"path": relative, **sources[relative], "output_sha256": digest(data)}
                          for relative, data in sorted(outputs.items())]}
    # Publish modules first, entry points last. Keep the web directory inode and
    # the branding audit unchanged; only this separate identity manifest is new.
    for relative in sorted(outputs, key=lambda name: (Path(name).suffix == ".html", name == "index.html", name)):
        atomic_write(web / relative, outputs[relative])
    atomic_write(web / MANIFEST_NAME, (json.dumps(report, indent=2, sort_keys=True) + "\n").encode())
    return report


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--web", required=True, type=Path, help="Public release/casdoor-web directory")
    args = parser.parse_args()
    report = prepare(args.web)
    print(json.dumps({"manifest": str(args.web / MANIFEST_NAME), **report["main"]}, sort_keys=True))


if __name__ == "__main__":
    try:
        main()
    except (OSError, ValueError, KeyError, RuntimeError) as error:
        print(f"UniHub identity preparation failed: {error}", file=sys.stderr)
        sys.exit(1)
