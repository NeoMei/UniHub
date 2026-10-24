#!/usr/bin/env python3
"""Build a minimal official-source Casdoor audit patch; never deploy or read secrets."""
import argparse
import fcntl
import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tarfile
import tempfile
import urllib.request

HERE = Path(__file__).resolve().parent
LOCK = json.loads((HERE / "source-lock.json").read_text())


def digest(path):
    value = hashlib.sha256()
    with Path(path).open("rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            value.update(chunk)
    return value.hexdigest()


def checked(path, expected):
    if Path(path).is_symlink() or digest(path) != expected:
        raise RuntimeError("Pinned public source SHA-256 mismatch: " + str(path))


def privacy(source, log, baseline=False):
    result = subprocess.run([sys.executable, str(HERE / "run-privacy-tests.py"), "--source", str(source)], stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True)
    log.write_text(result.stdout)
    if baseline:
        markers = ["audit retains sensitive " + key for key in ["nonce", "state", "code", "code_challenge"]]
        if result.returncode == 0 or not all(marker in result.stdout for marker in markers):
            raise RuntimeError("Pinned unpatched privacy regression did not reproduce; inspect public test log")
    elif result.returncode:
        raise RuntimeError("Patched privacy tests failed; inspect public test log")


def build(work, skip_build=False, image_tag=None):
    work = Path(work).absolute()
    if work.is_symlink():
        raise RuntimeError("Build workspace cannot be a symlink")
    work.mkdir(parents=True, exist_ok=True)
    lease_path = work / ".build.lock"
    if lease_path.is_symlink():
        raise RuntimeError("Build lock cannot be a symlink")
    with lease_path.open("a+") as lease:
        try:
            fcntl.flock(lease, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError:
            raise RuntimeError("Another identity build is using this workspace") from None
        return build_locked(work, skip_build, image_tag)


def build_locked(work, skip_build=False, image_tag=None):
    work = Path(work).absolute()
    if work.is_symlink():
        raise RuntimeError("Build workspace cannot be a symlink")
    work = work.resolve()
    work.mkdir(parents=True, exist_ok=True)
    if work.is_symlink():
        raise RuntimeError("Build workspace cannot be a symlink")
    archive = work / "source.tar.gz"
    if not archive.exists():
        with urllib.request.urlopen(LOCK["archive_url"], timeout=60) as response, archive.open("wb") as stream:
            shutil.copyfileobj(response, stream)
    checked(archive, LOCK["archive_sha256"])
    output = work / ("source-test-context" if skip_build else "image-context")
    output.mkdir(exist_ok=True)
    if output.is_symlink():
        raise RuntimeError("Image context cannot be a symlink")
    env = os.environ.copy()
    env.update({"CGO_ENABLED": "0", "GOOS": "linux", "GOARCH": "amd64", "GOTOOLCHAIN": "local", "GOPROXY": "https://proxy.golang.org,direct", "GOSUMDB": "sum.golang.org"})
    with tempfile.TemporaryDirectory(prefix="public-casdoor-", dir=work) as temporary:
        with tarfile.open(archive) as source_tar:
            source_tar.extractall(temporary, filter="data")
        source = Path(temporary) / ("casdoor-" + LOCK["commit"])
        for relative, sha in LOCK["files"].items():
            checked(source / relative, sha)
        privacy(source, work / "privacy-baseline.log", baseline=True)
        subprocess.run(["patch", "-p1", "--fuzz=0", "-i", str(HERE / "audit-redaction.patch")], cwd=source, check=True)
        checked(source / "object/record.go", LOCK["patched_record_sha256"])
        privacy(source, work / "privacy-patched.log")
        report = {"patch": "audit-redaction-v1", "source": LOCK, "patch_sha256": digest(HERE / "audit-redaction.patch"), "privacy_baseline_reproduced": True, "privacy_patched_source_functions_passed": True, "deployed_idp_privacy_verified": False, "go_version": subprocess.check_output(["go", "version"], text=True).strip()}
        if not skip_build:
            flags = "-w -s -X github.com/casdoor/casdoor/util.Version=v4.23.0 -X github.com/casdoor/casdoor/util.CommitId=" + LOCK["commit"]
            command = ["go", "build", "-mod=readonly", "-trimpath", "-ldflags=" + flags, "-o", str(output / "server"), "."]
            subprocess.run(command, cwd=source, env=env, check=True)
            header = (output / "server").read_bytes()[:20]
            if header[:6] != b"\x7fELF\x02\x01" or header[18:20] != b"\x3e\x00":
                raise RuntimeError("Output is not a Linux amd64 ELF binary")
            report.update({"binary_sha256": digest(output / "server"), "binary_target": "linux/amd64", "cgo_enabled": False, "build_command": command[:-1] + ["<locked-source>"]})
            os.chmod(output / "server", 0o755)
        shutil.copyfile(source / "LICENSE", output / "LICENSE")
        shutil.copyfile(HERE / "Dockerfile", output / "Dockerfile")
        (output / ".dockerignore").write_text("*\n!server\n!LICENSE\n!NOTICE\n!source-manifest.json\n!Dockerfile\n!.dockerignore\n")
        (output / "NOTICE").write_text("Casdoor v4.23.0\nCopyright The Casdoor Authors.\nLicensed under the Apache License, Version 2.0.\n\nUniHub modification: object/record.go secretRecordKeys now also masks nonce, state, code and code_challenge before audit storage and webhook delivery. Authentication and authorization validation are unchanged.\nOfficial source: " + LOCK["repository"] + " at " + LOCK["commit"] + "\n")
        (output / "source-manifest.json").write_text(json.dumps(report, indent=2, sort_keys=True) + "\n")
    if image_tag:
        if skip_build:
            raise RuntimeError("Cannot build an image after skipping the backend build")
        subprocess.run(["docker", "build", "--platform", "linux/amd64", "--tag", image_tag, str(output)], check=True)
        image_format = '{"id":{{json .Id}},"os":{{json .Os}},"architecture":{{json .Architecture}},"user":{{json .Config.User}},"entrypoint":{{json .Config.Entrypoint}}}'
        image = json.loads(subprocess.check_output(["docker", "image", "inspect", image_tag, "--format", image_format], text=True))
        if image["os"] != "linux" or image["architecture"] != "amd64" or image["user"] != "1000" or image["entrypoint"] != ["/server"]:
            raise RuntimeError("Custom image runtime contract changed")
        (work / "image-result.json").write_text(json.dumps({"tag": image_tag, **image, "binary_sha256": report["binary_sha256"]}, indent=2, sort_keys=True) + "\n")
    print(json.dumps({"artifact_context": str(output), "source_commit": LOCK["commit"], "backend_built": not skip_build, "privacy_source_functions": "passed", "deployed_idp_privacy_verified": False}, sort_keys=True))


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--work-dir", required=True, type=Path, help="Dedicated public-source/artifact build directory")
    parser.add_argument("--test-only", action="store_true", help="Run red/green source tests without compiling backend")
    parser.add_argument("--image-tag", help="Optionally build this local Docker image; never push or deploy")
    args = parser.parse_args()
    try:
        build(args.work_dir, args.test_only, args.image_tag)
    except (OSError, ValueError, RuntimeError, subprocess.CalledProcessError) as error:
        print("Casdoor audit build failed: " + str(error), file=sys.stderr)
        sys.exit(1)
