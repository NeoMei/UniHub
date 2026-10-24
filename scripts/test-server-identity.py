#!/usr/bin/env python3
"""Offline identity-overlay regressions; all writes use disposable directories.

The small module graph exercises publication without depending on Docker. A
separate test uses the actual public branded assets when they are available.
No credentials, private configuration, browser profile or network is accessed.
"""

import hashlib
import importlib.util
import json
from pathlib import Path
import re
import shutil
import subprocess
import tempfile
import unittest
from unittest.mock import patch


ROOT = Path(__file__).resolve().parents[1]
MODULE = ROOT / "deploy/server/prepare-identity.py"
ORIGINAL = ('function LG(e){if(!e)return!1;try{const t=new URL(e);return '
            't.origin!==new URL(E0()).origin?!1:Cn===""||t.pathname===Cn||'
            't.pathname.startsWith(`${Cn}/`)}catch{return!1}}')
MAIN = "assets/index-cnXxJbP8.unihub-ad7cb130c0251a1a.js"
REVISION = "ad7cb130c0251a1a"


def sha256(data):
    return hashlib.sha256(data).hexdigest()


class IdentityOverlayTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        if MODULE.exists():
            spec = importlib.util.spec_from_file_location("server_identity", MODULE)
            cls.identity = importlib.util.module_from_spec(spec)
            spec.loader.exec_module(cls.identity)

    def setUp(self):
        self.assertTrue(MODULE.exists(), "same-origin identity overlay is not implemented")
        temporary = tempfile.TemporaryDirectory(prefix="unihub-identity-test-")
        self.addCleanup(temporary.cleanup)
        self.web = Path(temporary.name) / "casdoor-web"
        self.web.mkdir()
        self.source = ('import "./vendor-a.unihub-' + REVISION + '.js";\n'
                       + ORIGINAL + '\nexport {LG as ds};\n').encode()
        self.write(MAIN, self.source)
        self.write("index.html", '<script type="module" src="/' + MAIN + '"></script>')
        self.write("assets/vendor-a.unihub-" + REVISION + ".js",
                   'import {ds} from "./' + Path(MAIN).name + '"; '
                   'export const lazy=()=>import("./lazy-b.unihub-' + REVISION + '.js");')
        self.write("assets/lazy-b.unihub-" + REVISION + ".js",
                   'import "./style-c.unihub-' + REVISION + '.css"; export const ok=true;')
        self.write("assets/style-c.unihub-" + REVISION + ".css", ".login {color: teal}")
        self.write("branding-audit.json", '{"display_only":true}\n')
        # Only the upstream fixture digest is substituted. The production URL
        # behavior, strict validation and filesystem publisher run unchanged.
        digest_override = patch.object(self.identity, "SOURCE_SHA256", sha256(self.source))
        digest_override.start()
        self.addCleanup(digest_override.stop)

    def write(self, relative, content):
        path = self.web / relative
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_bytes(content if isinstance(content, bytes) else content.encode())
        return path

    def snapshot(self):
        return {str(path.relative_to(self.web)): path.read_bytes()
                for path in self.web.rglob("*") if path.is_file()}

    def patched_function(self):
        report = self.identity.prepare(self.web)
        text = (self.web / report["main"]["output_path"]).read_text()
        start = text.index("function LG(e){")
        end = text.index("catch{return!1}}", start) + len("catch{return!1}}")
        return text[start:end]

    @unittest.skipUnless(shutil.which("node"), "Node.js unavailable")
    def test_callback_keeps_code_flow_and_all_other_redirects_keep_upstream_behavior(self):
        function = self.patched_function()
        cases = [
            ["https://unihub.quukk.com/auth/callback", "https://unihub.quukk.com", "", False],
            ["https://unihub.quukk.com/auth/callback?next=1", "https://unihub.quukk.com", "", True],
            ["https://unihub.quukk.com/auth/callback#fragment", "https://unihub.quukk.com", "", True],
            ["https://user:pass@unihub.quukk.com/auth/callback", "https://unihub.quukk.com", "", True],
            ["https://unihub.quukk.com/auth/callback/", "https://unihub.quukk.com", "", True],
            ["https://unihub.quukk.com/auth/callback-other", "https://unihub.quukk.com", "", True],
            ["https://unihub.quukk.com/applications", "https://unihub.quukk.com", "", True],
            ["https://unihub.quukk.com/", "https://unihub.quukk.com", "", True],
            ["https://external.example/auth/callback", "https://unihub.quukk.com", "", False],
            ["http://unihub.quukk.com/auth/callback", "http://unihub.quukk.com", "", True],
            ["https://unihub.quukk.com/auth/callback", "https://elsewhere.example", "", False],
            ["https://elsewhere.example/auth/callback", "https://elsewhere.example", "", True],
            ["https://unihub.quukk.com/base/apps", "https://unihub.quukk.com", "/base", True],
            ["https://unihub.quukk.com/outside", "https://unihub.quukk.com", "/base", False],
            ["/auth/callback", "https://unihub.quukk.com", "", False],
            ["invalid-url", "https://unihub.quukk.com", "", False],
            ["", "https://unihub.quukk.com", "", False],
            [None, "https://unihub.quukk.com", "", False],
        ]
        script = 'const cases=' + json.dumps(cases) + ';\n'
        script += 'const original=' + ORIGINAL + '; const updated=' + function + ';\n'
        script += '''for (const [redirect,origin,base,want] of cases) {
          global.window={location:{origin}}; global.E0=()=>origin; global.Cn=base;
          const got=updated(redirect);
          if(got!==want) throw new Error(JSON.stringify({redirect,origin,base,got,want}));
          if(redirect!=="https://unihub.quukk.com/auth/callback" && got!==original(redirect))
            throw new Error("unrelated upstream behavior changed");
          const values={type:"code"};
          if(values.type!=="device" && updated(redirect)) values.type="login";
          if(redirect==="https://unihub.quukk.com/auth/callback" && origin==="https://unihub.quukk.com" && values.type!=="code")
            throw new Error("authorization code request was converted to login");
        }
        console.log("same-origin callback and upstream redirect behavior verified");'''
        result = subprocess.run([shutil.which("node")], input=script, text=True, capture_output=True)
        self.assertEqual(result.returncode, 0, result.stderr)

    def test_new_module_graph_is_published_before_entry_and_old_cache_urls_are_retained(self):
        original = self.snapshot()
        inode = self.web.stat().st_ino
        report = self.identity.prepare(self.web)
        self.assertEqual(self.web.stat().st_ino, inode)
        self.assertNotEqual(report["main"]["output_path"], MAIN)
        self.assertEqual(report["main"]["source_sha256"], sha256(self.source))
        self.assertIn('/' + report["main"]["output_path"], (self.web / "index.html").read_text())
        for relative, data in original.items():
            if relative != "index.html":
                self.assertEqual((self.web / relative).read_bytes(), data)
        mapped = report["asset_mapping"]
        self.assertEqual(len(mapped), 4)
        for output in mapped.values():
            self.assertTrue((self.web / output).is_file())
            text = (self.web / output).read_text()
            for old in mapped:
                self.assertNotIn(Path(old).name, text)
            for match in re.finditer(r'"\./([^"\s]+\.(?:js|css))"', text):
                self.assertTrue((self.web / output).parent.joinpath(match[1]).is_file(), match[1])
        self.assertEqual((self.web / "branding-audit.json").read_bytes(), original["branding-audit.json"])

    def test_second_run_verifies_outputs_and_changes_no_bytes(self):
        first_report = self.identity.prepare(self.web)
        first = self.snapshot()
        second_report = self.identity.prepare(self.web)
        self.assertEqual(first_report, second_report)
        self.assertEqual(first, self.snapshot())
        manifest = json.loads((self.web / self.identity.MANIFEST_NAME).read_text())
        self.assertEqual(manifest, first_report)

    def test_source_hash_drift_is_refused_before_any_write(self):
        self.write(MAIN, self.source + b"// unexpected upstream change\n")
        before = self.snapshot()
        with self.assertRaisesRegex(RuntimeError, "source SHA-256"):
            self.identity.prepare(self.web)
        self.assertEqual(before, self.snapshot())

    def test_nonunique_or_missing_reviewed_function_is_refused_before_any_write(self):
        for source in (self.source.replace(ORIGINAL.encode(), b"function LG(){return false}"),
                       self.source + ORIGINAL.encode()):
            with self.subTest(count=source.count(ORIGINAL.encode())):
                self.write(MAIN, source)
                with patch.object(self.identity, "SOURCE_SHA256", sha256(source)):
                    before = self.snapshot()
                    with self.assertRaisesRegex(RuntimeError, "expected 1"):
                        self.identity.prepare(self.web)
                    self.assertEqual(before, self.snapshot())

    def test_output_tampering_is_refused_on_repeat(self):
        report = self.identity.prepare(self.web)
        self.write(report["main"]["output_path"], b"// tampered output\n")
        before = self.snapshot()
        with self.assertRaisesRegex(RuntimeError, "output SHA-256"):
            self.identity.prepare(self.web)
        self.assertEqual(before, self.snapshot())

    def test_symlinked_public_tree_is_refused_before_any_write(self):
        (self.web / "assets/linked.js").symlink_to(self.web / MAIN)
        with self.assertRaisesRegex(RuntimeError, "symlink"):
            self.identity.prepare(self.web)


class ActualPublicAssetTests(unittest.TestCase):
    @unittest.skipUnless((ROOT / ".local/casdoor/web" / MAIN).is_file(), "reviewed public build unavailable")
    def test_unmodified_reviewed_public_build_runs_through_real_cli_and_is_idempotent(self):
        self.assertTrue(MODULE.exists(), "same-origin identity overlay is not implemented")
        with tempfile.TemporaryDirectory(prefix="unihub-identity-public-") as temporary:
            web = Path(temporary) / "casdoor-web"
            shutil.copytree(ROOT / ".local/casdoor/web", web)
            command = ["python3", str(MODULE), "--web", str(web)]
            first = subprocess.run(command, capture_output=True, text=True)
            self.assertEqual(first.returncode, 0, first.stderr)
            report = json.loads(first.stdout)
            self.assertEqual(report["source_sha256"], "551d29642ebe1dd4afb6821f56ece30ba74ba5d4104b772d332408f1205fd2db")
            first_index = (web / "index.html").read_bytes()
            second = subprocess.run(command, capture_output=True, text=True)
            self.assertEqual(second.returncode, 0, second.stderr)
            self.assertEqual(json.loads(second.stdout), report)
            self.assertEqual((web / "index.html").read_bytes(), first_index)


if __name__ == "__main__":
    unittest.main()
