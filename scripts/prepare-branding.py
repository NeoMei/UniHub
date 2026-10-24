#!/usr/bin/env python3
"""Prepare the pinned Casdoor frontend with auditable UniHub display patches.

No application container is started, stopped or modified. The pristine frontend
comes from a temporary, never-started container of the exact image digest. Every
patched source file and replacement count must match the reviewed patch manifest.
The output directory inode is retained for an existing read-only Docker bind mount.
"""

import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import sys
import tempfile
from concurrent.futures import ThreadPoolExecutor
from urllib.parse import urlsplit


ROOT = Path(__file__).resolve().parents[1]
PATCH_ROOT = ROOT / "branding" / "casdoor"
MANIFEST = PATCH_ROOT / "patches-v4.23.0.json"
SWAGGER_MANIFEST = PATCH_ROOT / "swagger-patches-v4.23.0.json"
# A pair consumes both key and value, so words inside keys and quoted values
# cannot become a second match. This is restricted to reviewed, hash-locked
# dictionary/tour spans, never applied to arbitrary JavaScript source.
LITERAL = r'''(?:"(?:\\.|[^"\\])*"|'(?:\\.|[^'\\])*'|`(?:\\.|[^`\\])*`)'''
DISPLAY_PAIR = re.compile(r'(?P<key>' + LITERAL + r'|[A-Za-z_$][\w$]*):(?P<value>' + LITERAL + r')')


def digest(data):
    return hashlib.sha256(data).hexdigest()


def command(args):
    result = subprocess.run(args, capture_output=True, text=True)
    if result.returncode:
        # These commands contain only image IDs and public asset paths.
        raise RuntimeError(f"{' '.join(args[:2])} failed: {result.stderr.strip()}")
    return result.stdout.strip()


def checked_replace(text, old, new, expected, label):
    count = text.count(old)
    if count != expected:
        raise RuntimeError(f"{label}: expected {expected} matches, found {count}")
    return text.replace(old, new)


def patch_display_values(text, rule, label):
    start_marker, end_marker = rule["start"], rule["end"]
    if start_marker:
        if text.count(start_marker) != 1:
            raise RuntimeError(f"{label}: display span start is not unique")
        start = text.index(start_marker)
    else:
        start = 0
    if end_marker:
        if text.count(end_marker) != 1:
            raise RuntimeError(f"{label}: display span end is not unique")
        end = text.index(end_marker, start)
    else:
        end = len(text)
    changed = 0
    preserved = 0

    def replace(match):
        nonlocal changed, preserved
        value = match.group("value")
        if "Casdoor" not in value and "casdoor" not in value and "CasDoor" not in value:
            return match.group(0)
        # Preserve actual third-party products/SDK names, marketplace references
        # and all original translation keys. URL text is lower-case and remains
        # untouched unless an exact reviewed replacement is explicitly declared.
        key = match.group("key").strip("\"'`")
        if key in rule.get("preserve_keys", []) or any(term in value for term in rule.get("preserve_values_containing", [])):
            preserved += 1
            return match.group(0)
        updated = value.replace("Casdoor", "UniHub").replace("CasDoor", "UniHub")
        if rule.get("welcome_lowercase"):
            updated = updated.replace("Welcome to casdoor", "Welcome to UniHub")
        if updated == value:
            return match.group(0)
        changed += 1
        return match.group(0)[:match.start("value") - match.start()] + updated

    patched = DISPLAY_PAIR.sub(replace, text[start:end])
    if (changed, preserved) != (rule["expected_changed"], rule["expected_preserved"]):
        raise RuntimeError(f"{label}: display matches {(changed, preserved)} differ from "
                           f"{(rule['expected_changed'], rule['expected_preserved'])}")
    return text[:start] + patched + text[end:]


def apply_file_patches(build, manifest):
    report = []
    for entry in manifest["files"]:
        path = build / entry["path"]
        original = path.read_bytes()
        if digest(original) != entry["sha256"]:
            raise RuntimeError(f"{entry['path']}: pristine file digest changed; review upstream before branding")
        text = original.decode("utf-8")
        for index, rule in enumerate(entry.get("display_values", [])):
            text = patch_display_values(text, rule, f"{entry['path']} display rule {index + 1}")
        for index, replacement in enumerate(entry.get("replacements", [])):
            text = checked_replace(text, replacement["old"], replacement["new"],
                                   replacement["count"], f"{entry['path']} replacement {index + 1}")
        notice = "Modified by UniHub: display branding only; upstream licenses and protocol identifiers retained."
        if path.suffix == ".html":
            text = "<!-- " + notice + " -->\n" + text
        elif path.suffix in {".js", ".css"}:
            text = "/* " + notice + " */\n" + text
        patched = text.encode("utf-8")
        path.write_bytes(patched)
        report.append({"path": entry["path"], "original_sha256": digest(original),
                       "branded_sha256": digest(patched)})
    return report


def swagger_metadata(document, suffix):
    """Separate the two display values from all untouched API schema content."""
    if suffix == ".json":
        schema = json.loads(document)
        info = schema["info"]
        metadata = {key: info.pop(key) for key in ("title", "description")}
        return metadata, schema
    # The hash-locked YAML uses two plain, single-line info scalars. Comparing
    # every other byte is stricter than semantic equality and needs no YAML
    # dependency; reject changed shape instead of guessing at arbitrary YAML.
    info = re.search(r"(?m)^info:\n(?P<body>(?:[ \t].*\n)+)", document)
    if info is None:
        raise RuntimeError("Swagger YAML info block does not match the reviewed shape")
    body = info.group("body")
    metadata = {}
    for key in ("title", "description"):
        pattern = re.compile(r"(?m)^  " + key + r": ([^\r\n]*)\n")
        matches = list(pattern.finditer(body))
        if len(matches) != 1:
            raise RuntimeError(f"Swagger YAML info.{key} is not one reviewed scalar")
        metadata[key] = matches[0].group(1)
        body = pattern.sub("", body)
    return metadata, document[:info.start("body")] + body + document[info.end("body"):]


def patch_swagger(build, manifest):
    before = {}
    for entry in manifest["files"]:
        path = build / entry["path"]
        if path.suffix in {".json", ".yml"}:
            before[entry["path"]] = swagger_metadata(path.read_text(), path.suffix)
    verify_swagger_preservation(build, manifest)
    report = apply_file_patches(build, manifest)
    for relative, (old_metadata, old_schema) in before.items():
        path = build / relative
        metadata, schema = swagger_metadata(path.read_text(), path.suffix)
        if schema != old_schema or metadata != manifest["metadata"]:
            raise RuntimeError(f"{relative}: changes must be limited to info.title and info.description")
        if any(metadata[key] == old_metadata[key] for key in metadata):
            raise RuntimeError(f"{relative}: expected Swagger display metadata was not changed")
    return report


def verify_swagger_preservation(build, manifest):
    for entry in manifest["preserve_files"]:
        if digest((build / entry["path"]).read_bytes()) != entry["sha256"]:
            raise RuntimeError(f"{entry['path']}: upstream Swagger library or license changed")


def patch_tree(build, manifest):
    report = apply_file_patches(build, manifest)
    asset_dir = build / "assets" / "unihub"
    asset_dir.mkdir()
    for name in ("manifest.webmanifest", "branding.css"):
        shutil.copyfile(PATCH_ROOT / name, asset_dir / name)
    shutil.copyfile(PATCH_ROOT / "LICENSE", asset_dir / "LICENSE-Casdoor.txt")
    shutil.copyfile(PATCH_ROOT / "UPSTREAM.txt", asset_dir / "UPSTREAM.txt")
    png_files = sorted((ROOT / "branding" / "assets").glob("*.png"))
    names = {path.name for path in png_files}
    required = {"unihub-logo-v1.png", "unihub-icon-v1.png"}
    if not required.issubset(names):
        raise RuntimeError("Missing required UniHub logo/icon in branding/assets")
    for source in png_files:
        if source.is_symlink() or not source.read_bytes().startswith(b"\x89PNG\r\n\x1a\n"):
            raise RuntimeError(f"Invalid public PNG asset: {source.name}")
        shutil.copyfile(source, asset_dir / source.name)
    for path in build.rglob("*"):
        if path.is_symlink():
            raise RuntimeError(f"Unexpected frontend symlink: {path.relative_to(build)}")
        path.chmod(0o755 if path.is_dir() else 0o644)
    build.chmod(0o755)
    return report


def tree_files(directory):
    return {path.relative_to(directory): path for path in directory.rglob("*") if path.is_file()}


def version_frontend(build, manifest):
    """Give the entire connected module graph a fresh, deterministic URL.

    Renaming only patched modules is insufficient: a cached unmodified importer
    can retain its old imports. Every asset JS/CSS module gets the same revision,
    and every importer/preload table is rewritten. Original paths remain usable
    aliases for pages that were already open during publication.
    """
    inputs = hashlib.sha256()
    inputs.update(json.dumps(manifest, sort_keys=True).encode())
    inputs.update(Path(__file__).read_bytes())
    originals = tree_files(build)
    for relative, path in sorted(originals.items()):
        inputs.update(str(relative).encode() + b"\0" + path.read_bytes() + b"\0")
    revision = inputs.hexdigest()[:16]
    mapping = {str(relative): str(relative.with_name(relative.stem + ".unihub-" + revision + relative.suffix))
               for relative in originals
               if relative.parts[0] == "assets" and relative.suffix in {".js", ".css"}}
    # Each original build basename is unique. Match complete filenames, including
    # Vite dependency arrays and dynamic imports, without altering protocol IDs.
    basename_map = {Path(old).name: Path(new).name for old, new in mapping.items()}
    if len(basename_map) != len(mapping):
        raise RuntimeError("Frontend asset basenames are no longer unique")
    names = re.compile(r"(?<![A-Za-z0-9_.-])(?:" + "|".join(map(re.escape, basename_map)) + r")(?![A-Za-z0-9_.-])")
    references = {name: 0 for name in basename_map}

    def rewrite(match):
        references[match.group(0)] += 1
        return basename_map[match.group(0)]

    for relative, path in originals.items():
        if relative.parts[0] == "swagger" or path.suffix not in {".html", ".js", ".css"}:
            continue
        text = names.sub(rewrite, path.read_text())
        path.write_text(text)
        if str(relative) in mapping:
            (build / mapping[str(relative)]).write_text(text)
    if any(count == 0 for count in references.values()):
        missing = [name for name, count in references.items() if count == 0]
        raise RuntimeError("Unreferenced versioned frontend assets: " + ", ".join(missing))
    for path in build.rglob("*"):
        if path.suffix in {".html", ".js", ".css"} and names.search(path.read_text()):
            raise RuntimeError(f"Unversioned module reference remains in {path.relative_to(build)}")
    return revision, mapping


def version_swagger(build, revision):
    """Version the changed API metadata while keeping upstream libraries intact."""
    index = build / "swagger" / "index.html"
    text = index.read_text()
    mapping = {}
    for name, expected in (("swagger.json", 2), ("swagger.yml", 1)):
        versioned = Path(name).stem + ".unihub-" + revision + Path(name).suffix
        text = checked_replace(text, "./" + name, "./" + versioned, expected,
                               "Swagger documentation reference " + name)
        relative, published = "swagger/" + name, "swagger/" + versioned
        shutil.copyfile(build / relative, build / published)
        mapping[relative] = published
    index.write_text(text)
    return mapping


def verify_frontend(build, mapping):
    """Reject malformed modules and missing local imports before publication."""
    node = shutil.which("node")
    if not node:
        raise RuntimeError("Node.js is required to validate the prepared frontend JavaScript")
    js_files = [build / path for path in mapping.values() if path.endswith(".js")]
    js_files += [path for path in build.glob("*.js")]
    js_files += [path for path in (build / "swagger").glob("*.js")]

    def check_js(path):
        result = subprocess.run([node, "--check", str(path)], capture_output=True, text=True)
        if result.returncode:
            raise RuntimeError(f"JavaScript syntax validation failed for {path.name}: {result.stderr.strip()}")

    with ThreadPoolExecutor(max_workers=4) as executor:
        list(executor.map(check_js, js_files))
    inline_count = 0
    for relative in ("index.html", "swagger/index.html"):
        for script in re.finditer(r"<script\b[^>]*>(.*?)</script>", (build / relative).read_text(), re.DOTALL):
            if not script.group(1).strip():
                continue
            result = subprocess.run([node, "--check"], input=script.group(1), capture_output=True, text=True)
            if result.returncode:
                raise RuntimeError(f"Inline JavaScript syntax validation failed for {relative}: {result.stderr.strip()}")
            inline_count += 1
    # Imported asset literals cover static imports, dynamic import() expressions,
    # CSS dependencies, and Vite's preload tables. Only local asset-shaped URLs
    # are selected; runtime API strings and external links are outside this check.
    asset_literal = re.compile(r'''["']((?:\./|/assets/|assets/)[^"'\s]+\.(?:js|css|woff2)(?:\?[^"'\s]*)?)["']''')
    reference_count = 0
    for relative in ["index.html", "swagger/index.html", *mapping.values()]:
        path = build / relative
        literal = asset_literal
        if relative == "swagger/index.html":
            literal = re.compile(r'''["']((?:\./|/assets/)[^"'\s]+\.(?:js|css|png|json|yml)(?:\?[^"'\s]*)?)["']''')
        for match in literal.finditer(path.read_text()):
            url = urlsplit(match.group(1)).path
            if url.startswith("/"):
                target = build / url.lstrip("/")
            elif url.startswith("assets/"):
                target = build / url
            else:
                target = path.parent / url
            if not target.is_file():
                raise RuntimeError(f"Missing frontend asset reference in {relative}: {url}")
            reference_count += 1
    return {"javascript_files": len(js_files), "inline_javascript_blocks": inline_count,
            "local_asset_references": reference_count, "swagger_schema_metadata_only": True}


def publish(build, output):
    """Validate everything before atomically replacing individual public files."""
    if output.is_symlink():
        raise RuntimeError("Refusing to publish through a frontend directory symlink")
    if output.exists() and any(path.is_symlink() for path in output.rglob("*")):
        raise RuntimeError("Refusing to publish into a frontend tree containing symlinks")
    old = tree_files(output) if output.exists() else {}
    new = tree_files(build)
    # Open tabs can still import a lazy chunk from an earlier revision. Retain
    # only our strictly named public JS/CSS and Swagger metadata revisions,
    # while removing unrelated stale files. Identical preparations still agree.
    revision_name = re.compile(r"[A-Za-z0-9_-]+\.unihub-[0-9a-f]{16}\.(?:js|css|json|yml)\Z")
    retained = {relative for relative in old.keys() - new.keys()
                if relative.parts[0] in {"assets", "swagger"} and revision_name.fullmatch(relative.name)}
    changed = old.keys() - retained != new.keys()
    output.mkdir(mode=0o755, parents=True, exist_ok=True)
    output.chmod(0o755)
    # Publish the complete graph first and the HTML entry point last, so a page
    # loaded during a rebuild cannot point to a module that has not arrived yet.
    for relative, source in sorted(new.items(), key=lambda item: (
            item[0].name == "index.html", str(item[0]) == "index.html", str(item[0]))):
        destination = output / relative
        if relative in old and destination.read_bytes() == source.read_bytes():
            destination.chmod(0o644)
            continue
        changed = True
        destination.parent.mkdir(mode=0o755, parents=True, exist_ok=True)
        descriptor, temporary = tempfile.mkstemp(prefix=".unihub-branding-", dir=destination.parent)
        try:
            with os.fdopen(descriptor, "wb") as stream:
                stream.write(source.read_bytes())
            os.chmod(temporary, 0o644)
            os.replace(temporary, destination)
        finally:
            if os.path.exists(temporary):
                os.unlink(temporary)
    for relative in old.keys() - new.keys() - retained:
        old[relative].unlink()
    # dev-setup uses umask 077. Every public directory must be traversable by
    # Casdoor's uid 1000 even when mkdir(parents=True) created intermediate paths.
    for directory in output.rglob("*"):
        if directory.is_dir():
            directory.chmod(0o755)
    return changed, len(new) + len(retained)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--json", action="store_true", help="Emit only machine-readable status")
    parser.add_argument("--check", action="store_true", help="Validate a fresh copy without publishing")
    args = parser.parse_args()
    manifest = json.loads(MANIFEST.read_text())
    swagger_manifest = json.loads(SWAGGER_MANIFEST.read_text())
    image = manifest["image"]
    if swagger_manifest["image"] != image:
        raise RuntimeError("Frontend and Swagger manifests must use the same pinned image")
    if "@sha256:" not in image:
        raise RuntimeError("Branding source image must be pinned by digest")
    inspect = subprocess.run(["docker", "image", "inspect", image], capture_output=True, text=True)
    if inspect.returncode:
        command(["docker", "pull", image])
        inspect = subprocess.run(["docker", "image", "inspect", image], capture_output=True, text=True)
        if inspect.returncode:
            raise RuntimeError("Pinned Casdoor image is unavailable after pull")
    info = json.loads(inspect.stdout)[0]
    digest_reference = image.split(":v", 1)[0] + "@sha256:" + image.rsplit("@sha256:", 1)[1]
    if digest_reference not in info.get("RepoDigests", []):
        raise RuntimeError("The local image does not advertise the reviewed repository digest")
    parent = ROOT / ".local" / "casdoor"
    parent.mkdir(parents=True, exist_ok=True)
    container = None
    try:
        with tempfile.TemporaryDirectory(prefix="branding-build-", dir=parent) as temporary:
            build = Path(temporary) / "build"
            build.mkdir()
            container = command(["docker", "create", "--network", "none", image])
            command(["docker", "cp", container + ":/web/build/.", str(build)])
            (build / "swagger").mkdir()
            command(["docker", "cp", container + ":/swagger/.", str(build / "swagger")])
            command(["docker", "rm", container])
            container = None
            report = patch_tree(build, manifest)
            report += patch_swagger(build, swagger_manifest)
            revision, mapping = version_frontend(build, {"frontend": manifest, "swagger": swagger_manifest})
            mapping.update(version_swagger(build, revision))
            verify_swagger_preservation(build, swagger_manifest)
            validation = verify_frontend(build, mapping)
            for entry in report:
                entry["published_path"] = mapping.get(entry["path"], entry["path"])
                entry["branded_sha256"] = digest((build / entry["published_path"]).read_bytes())
            output = parent / "web"
            changed, files = (False, len(tree_files(build))) if args.check else publish(build, output)
            if not args.check:
                report_path = parent / "branding-report.json"
                report_path.write_text(json.dumps({"image": image, "revision": revision,
                                                  "validation": validation, "patches": report}, indent=2) + "\n")
                report_path.chmod(0o600)
            result = {"changed": changed, "output": str(output), "files": files,
                      "patched_files": len(report), "check_only": args.check,
                      "revision": revision, "validation": validation}
            print(json.dumps(result) if args.json else "UniHub frontend prepared: " + json.dumps(result))
    finally:
        if container:
            subprocess.run(["docker", "rm", container], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)


if __name__ == "__main__":
    try:
        main()
    except (OSError, ValueError, KeyError, RuntimeError) as error:
        print(f"UniHub branding preparation failed: {error}", file=sys.stderr)
        sys.exit(1)
