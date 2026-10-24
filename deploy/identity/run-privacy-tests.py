#!/usr/bin/env python3
"""Run verbatim upstream audit functions with substituted external I/O sinks."""
import argparse
import os
from pathlib import Path
import shutil
import subprocess
import tempfile


def run(source):
    source = Path(source).resolve()
    here = Path(__file__).resolve().parent
    with tempfile.TemporaryDirectory(prefix="unihub-casdoor-audit-test-") as temporary:
        root = Path(temporary)
        subprocess.run(["go", "run", str(here / "tests/extract.go"), "-source", str(source), "-out", str(root)], check=True)
        (root / "go.mod").write_text("module github.com/casdoor/casdoor\n\ngo 1.25.0\n\nrequire github.com/beego/beego/v2 v2.3.8\n")
        shutil.copyfile(source / "go.sum", root / "go.sum")
        for name in ["record_privacy_test.go", "record_sinks.go"]:
            shutil.copyfile(here / "tests" / (name + ".txt"), root / "object" / name)
        (root / "conf/stub.go").write_text('''package conf
func GetConfigBool(string)bool{return false}
func GetAcceptLanguage(value string)string{return value}
''')
        (root / "util/stub.go").write_text('''package util
import "net/http"
func GetClientIpFromRequest(r *http.Request)string{return r.RemoteAddr}
func GenerateId()string{return "fixture-id"}
func GetCurrentTime()string{return "2026-10-25T00:00:00Z"}
''')
        env = os.environ.copy()
        env.update({"GOTOOLCHAIN": "local", "GOPROXY": "https://proxy.golang.org,direct", "GOSUMDB": "sum.golang.org"})
        subprocess.run(["go", "test", "-mod=mod", "-count=1", "-race", "-timeout", "90s", "./object"], cwd=root, env=env, check=True)


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--source", required=True, type=Path)
    run(parser.parse_args().source)
