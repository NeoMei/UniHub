#!/usr/bin/env python3
"""Offline regression checks for branding publication and development setup.

Uses only Python's standard library and disposable directories. No Docker,
database, browser, credentials, or existing .local files are accessed.
"""

import ast
import copy
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import stat
import tempfile
from types import SimpleNamespace
import unittest


ROOT = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location("prepare_branding", ROOT / "scripts/prepare-branding.py")
branding = importlib.util.module_from_spec(spec)
spec.loader.exec_module(branding)


def setup_helpers():
    """Load only pure helpers from the embedded Python, never its setup commands."""
    shell = (ROOT / "scripts/dev-setup.sh").read_text()
    source = shell.split("<<'PY'\n", 1)[1].rsplit("\nPY", 1)[0]
    constants = {"brand_logo", "brand_icon", "default_casbin_avatar", "brand_theme",
                 "organization_branding", "application_branding", "official_homepages"}
    functions = {"apply_seed_branding", "mount_source_matches", "ensure_product_application"}
    selected, found = [], set()
    for node in ast.parse(source).body:
        if isinstance(node, ast.Assign):
            names = {target.id for target in node.targets if isinstance(target, ast.Name)}
            if names & constants:
                selected.append(node)
                found.update(names)
        elif isinstance(node, ast.FunctionDef) and node.name in functions:
            selected.append(node)
            found.add(node.name)
    if not (constants | functions) <= found:
        raise RuntimeError("Expected dev-setup helpers were removed or renamed")
    namespace = {"Path": Path, "sys": SimpleNamespace(platform="darwin"), "json": json}
    exec(compile(ast.Module(body=selected, type_ignores=[]), "dev-setup-pure-helpers", "exec"), namespace)
    return namespace


class BrandingRegression(unittest.TestCase):
    def test_product_client_is_separate_preserved_and_uses_exact_callbacks(self):
        create = setup_helpers()["ensure_product_application"]
        original = {"owner":"admin", "name":"app-unihub", "clientId":"console", "clientSecret":"console-secret", "organization":"unihub", "cert":"cert", "grantTypes":["authorization_code"], "redirectUris":["http://localhost:8088/auth/callback"]}
        seed = {"applications":[copy.deepcopy(original)]}
        self.assertTrue(create(seed, "separate-product-secret"))
        self.assertEqual(seed["applications"][0], original)
        product = seed["applications"][1]
        self.assertEqual(product["clientId"], "superwagie-unihub-local")
        self.assertEqual(product["clientSecret"], "separate-product-secret")
        self.assertEqual(set(product["redirectUris"]), {"http://localhost:57991/api/platform-account/callback", "http://127.0.0.1:57991/api/platform-account/callback"})
        self.assertFalse(create(seed, "do-not-rotate"))
        self.assertEqual(seed["applications"][1]["clientSecret"], "separate-product-secret")
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory(prefix="unihub-branding-test-")
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)

    def write(self, relative, content):
        path = self.root / relative
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(content)
        return path

    def graph(self, name, panel_text):
        # The unchanged vendor is an importer too: renaming only the edited
        # panel reproduces the old cached-module/default-page branding defect.
        files = {
            "index.html": '<script type="module" src="/assets/entry-A.js"></script>',
            "assets/entry-A.js": 'import {open} from "./vendor-B.js"; export {open};',
            "assets/vendor-B.js": 'export const open = () => import("./panel-C.js");\n'
                                  'export const preload = ["assets/panel-C.js", "assets/panel-C.css"];',
            "assets/panel-C.js": 'import "./panel-C.css"; export default ' + repr(panel_text) + ';',
            "assets/panel-C.css": '.panel { color: #087F8C; }',
        }
        for relative, content in files.items():
            self.write(name + "/" + relative, content)
        build = self.root / name
        revision, mapping = branding.version_frontend(build, {"fixture": "lazy-panel"})
        return build, revision, mapping

    def assert_graph(self, output, mapping, panel_text):
        entry = (output / mapping["assets/entry-A.js"]).read_text()
        vendor = (output / mapping["assets/vendor-B.js"]).read_text()
        panel = (output / mapping["assets/panel-C.js"]).read_text()
        self.assertIn('"./' + Path(mapping["assets/vendor-B.js"]).name + '"', entry)
        self.assertIn('import("./' + Path(mapping["assets/panel-C.js"]).name + '")', vendor)
        self.assertIn('"' + mapping["assets/panel-C.js"] + '"', vendor)
        self.assertIn('"' + mapping["assets/panel-C.css"] + '"', vendor)
        self.assertIn('"./' + Path(mapping["assets/panel-C.css"]).name + '"', panel)
        self.assertIn(repr(panel_text), panel)
        for path in mapping.values():
            self.assertTrue((output / path).is_file(), path)

    def test_source_drift_and_ambiguous_replacements_fail_closed(self):
        original = 'document.title = "Casdoor";\n'
        source = self.write("build/assets/entry-A.js", original)
        manifest = {"files": [{"path": "assets/entry-A.js",
                    "sha256": hashlib.sha256(original.encode()).hexdigest(),
                    "replacements": [{"old": "Casdoor", "new": "UniHub", "count": 1}]}]}
        source.write_text(original + "// upstream changed\n")
        with self.assertRaisesRegex(RuntimeError, "pristine file digest changed"):
            branding.apply_file_patches(self.root / "build", manifest)
        self.assertEqual(source.read_text(), original + "// upstream changed\n")
        for text in ('document.title = "Renamed";', original + original):
            with self.subTest(text=text):
                source.write_text(text)
                manifest["files"][0]["sha256"] = hashlib.sha256(text.encode()).hexdigest()
                with self.assertRaisesRegex(RuntimeError, "expected 1 matches"):
                    branding.apply_file_patches(self.root / "build", manifest)
                self.assertEqual(source.read_text(), text)
        source.write_text(original)
        manifest["files"][0]["sha256"] = hashlib.sha256(original.encode()).hexdigest()
        branding.apply_file_patches(self.root / "build", manifest)
        self.assertIn('document.title = "UniHub";', source.read_text())

    def test_swagger_branding_cannot_change_api_contract_or_preserved_libraries(self):
        schema = {
            "swagger": "2.0", "basePath": "/api",
            "info": {"title": "Casdoor API", "description": "Casdoor documentation", "version": "1.0"},
            "paths": {"/accounts": {"get": {"operationId": "listAccounts",
                                           "responses": {"200": {"description": "OK"}}}}},
            "securityDefinitions": {"OAuth2": {"type": "oauth2", "flow": "implicit",
                "authorizationUrl": "https://example.test/oauth/authorize", "scopes": {"read": "Read accounts"}}},
        }
        yaml = '''swagger: "2.0"
basePath: /api
info:
  title: Casdoor API
  description: Casdoor documentation
  version: "1.0"
paths:
  /accounts:
    get:
      operationId: listAccounts
      responses:
        "200":
          description: OK
securityDefinitions:
  OAuth2:
    type: oauth2
    flow: implicit
    authorizationUrl: https://example.test/oauth/authorize
    scopes:
      read: Read accounts
'''
        originals = {"swagger/swagger.json": json.dumps(schema, indent=2) + "\n",
                     "swagger/swagger.yml": yaml}
        metadata = {"title": "UniHub API", "description": "UniHub documentation"}
        library = "window.SwaggerUIBundle = function () {};\n"
        manifest = {"metadata": metadata, "files": [], "preserve_files": [{
            "path": "swagger/swagger-ui-bundle.js", "sha256": hashlib.sha256(library.encode()).hexdigest()}]}
        for path, content in originals.items():
            manifest["files"].append({"path": path, "sha256": hashlib.sha256(content.encode()).hexdigest(),
                "replacements": [{"old": old, "new": new, "count": 1} for old, new in
                                 (("Casdoor API", metadata["title"]),
                                  ("Casdoor documentation", metadata["description"]))]})

        def reset():
            for path, content in originals.items():
                self.write("api-build/" + path, content)
            self.write("api-build/swagger/swagger-ui-bundle.js", library)
            return self.root / "api-build"

        build = reset()
        branding.patch_swagger(build, manifest)
        expected = copy.deepcopy(schema)
        expected["info"].update(metadata)
        self.assertEqual(json.loads((build / "swagger/swagger.json").read_text()), expected)
        self.assertEqual((build / "swagger/swagger.yml").read_text(),
                         yaml.replace("Casdoor API", metadata["title"])
                             .replace("Casdoor documentation", metadata["description"]))
        self.assertEqual((build / "swagger/swagger-ui-bundle.js").read_text(), library)

        # Valid source fingerprints and matching replacement counts must not let
        # an accidentally broadened branding manifest modify either API format.
        for index, path in enumerate(originals):
            for field, old, new in (("basePath", "/api", "/changed"),
                                    ("paths", "/accounts", "/renamed-accounts"),
                                    ("securityDefinitions", "https://example.test/oauth/authorize",
                                     "https://example.test/oauth/changed")):
                with self.subTest(path=path, field=field):
                    reset()
                    bad = copy.deepcopy(manifest)
                    bad["files"][index]["replacements"].append({"old": old, "new": new, "count": 1})
                    with self.assertRaisesRegex(RuntimeError, "limited to info.title and info.description"):
                        branding.patch_swagger(build, bad)
        reset()
        (build / "swagger/swagger-ui-bundle.js").write_text(library + "// unexpected upstream drift\n")
        with self.assertRaisesRegex(RuntimeError, "upstream Swagger library or license changed"):
            branding.patch_swagger(build, manifest)
        for path, content in originals.items():
            self.assertEqual((build / path).read_text(), content)

    def test_cached_and_current_lazy_graphs_survive_republication(self):
        old_build, old_revision, old_map = self.graph("old", "old panel")
        output = self.root / "public"
        self.assertTrue(branding.publish(old_build, output)[0])
        inode = output.stat().st_ino
        old_bytes = {path: (output / path).read_bytes() for path in old_map.values()}
        self.write("public/assets/stale.js", "obsolete")
        self.write("public/assets/fake.unihub-not-a-revision.js", "obsolete")
        new_build, new_revision, new_map = self.graph("new", "new panel")
        self.assertNotEqual(old_revision, new_revision)
        self.assertTrue(set(old_map.values()).isdisjoint(new_map.values()))
        self.assertTrue(branding.publish(new_build, output)[0])
        self.assertEqual(output.stat().st_ino, inode)
        self.assertIn(new_map["assets/entry-A.js"], (output / "index.html").read_text())
        self.assert_graph(output, old_map, "old panel")
        self.assert_graph(output, new_map, "new panel")
        for path, content in old_bytes.items():
            self.assertEqual((output / path).read_bytes(), content)
        for original, versioned in new_map.items():
            self.assertEqual((output / original).read_bytes(), (output / versioned).read_bytes())
        self.assertFalse((output / "assets/stale.js").exists())
        self.assertFalse((output / "assets/fake.unihub-not-a-revision.js").exists())
        # Independently rebuilding identical inputs must not rotate asset URLs.
        same_build, same_revision, same_map = self.graph("same", "new panel")
        self.assertEqual((same_revision, same_map), (new_revision, new_map))
        snapshots = {p: (p.read_bytes(), p.stat().st_ino) for p in output.rglob("*") if p.is_file()}
        changed, count = branding.publish(same_build, output)
        self.assertFalse(changed)
        self.assertEqual(count, len(snapshots))
        self.assertEqual(output.stat().st_ino, inode)
        self.assertEqual(snapshots, {p: (p.read_bytes(), p.stat().st_ino) for p in snapshots})

    def test_publish_repairs_public_permissions_under_private_umask(self):
        previous = os.umask(0o077)
        try:
            build, _, _ = self.graph("private-build", "panel")
            self.write("private-build/assets/unihub/nested/brand.css", "body {}")
            output = self.root / "public"
            branding.publish(build, output)
            for path in [output, *output.rglob("*")]:
                self.assertEqual(stat.S_IMODE(path.stat().st_mode), 0o755 if path.is_dir() else 0o644)
            (output / "index.html").chmod(0o600)
            (output / "assets/unihub").chmod(0o700)
            self.assertFalse(branding.publish(build, output)[0])
            self.assertEqual(stat.S_IMODE((output / "index.html").stat().st_mode), 0o644)
            self.assertEqual(stat.S_IMODE((output / "assets/unihub").stat().st_mode), 0o755)
        finally:
            os.umask(previous)

    def test_publish_refuses_external_symlinks(self):
        build, _, _ = self.graph("build", "panel")
        external = self.write("external/keep.txt", "must survive")
        output = self.root / "public"
        output.symlink_to(external.parent, target_is_directory=True)
        with self.assertRaisesRegex(RuntimeError, "symlink"):
            branding.publish(build, output)
        output.unlink()
        output.mkdir()
        (output / "index.html").symlink_to(external)
        with self.assertRaisesRegex(RuntimeError, "symlink"):
            branding.publish(build, output)
        self.assertEqual(external.read_text(), "must survive")

    def test_seed_upgrade_preserves_authentication_and_custom_values(self):
        helpers = setup_helpers()
        upstream_avatar = "https://cdn.casbin.org/img/casbin.svg"
        seed = {
            "organizations": [
                {"owner": "admin", "name": "built-in", "defaultAvatar": upstream_avatar, "passwordType": "bcrypt"},
                {"owner": "admin", "name": "unihub", "defaultAvatar": "https://example.test/custom.png"},
                {"owner": "another-tenant", "name": "unihub", "displayName": "Keep tenant"}],
            "applications": [
                {"owner": "admin", "name": "app-built-in", "homepageUrl": "https://casdoor.org/",
                 "providers": [{"name": "existing-provider", "canSignIn": True}]},
                {"owner": "admin", "name": "app-unihub", "homepageUrl": "https://example.test/home",
                 "clientId": "synthetic-client", "clientSecret": "synthetic-not-a-live-secret",
                 "grantTypes": ["authorization_code"], "redirectUris": ["http://localhost:8089/callback"],
                 "cert": "keep-cert", "enablePassword": True, "enableSignUp": False,
                 "signinItems": [{"name": "Login button", "visible": False, "rule": "None"}]},
                {"owner": "admin", "name": "other-app", "displayName": "Keep other app"}],
            "users": [
                {"owner": "built-in", "name": "admin", "avatar": upstream_avatar, "password": "synthetic-hash"},
                {"owner": "unihub", "name": "alice", "avatar": "https://example.test/personal.png", "isAdmin": False}],
            "certs": [{"owner": "admin", "name": "keep-cert", "privateKey": "synthetic-key"}],
        }
        before = copy.deepcopy(seed)
        self.assertTrue(helpers["apply_seed_branding"](seed))
        for collection in ("organizations", "applications"):
            for item in seed[collection][:2]:
                self.assertEqual(item["displayName"], "UniHub")
                self.assertTrue(item["logo"].endswith("/unihub-logo-v1.png"))
            self.assertEqual(seed[collection][2], before[collection][2])
        for key, value in before["applications"][1].items():
            self.assertEqual(seed["applications"][1][key], value, key)
        self.assertEqual(seed["applications"][0]["providers"], before["applications"][0]["providers"])
        self.assertEqual(seed["applications"][0]["homepageUrl"], "/login/built-in")
        self.assertEqual(seed["organizations"][0]["passwordType"], "bcrypt")
        self.assertEqual(seed["organizations"][0]["defaultAvatar"], helpers["brand_icon"])
        self.assertEqual(seed["organizations"][1]["defaultAvatar"], "https://example.test/custom.png")
        self.assertEqual(seed["users"][0]["avatar"], helpers["brand_icon"])
        self.assertEqual(seed["users"][0]["password"], before["users"][0]["password"])
        self.assertEqual(seed["users"][1], before["users"][1])
        self.assertEqual(seed["certs"], before["certs"])
        seed["users"][0]["avatar"] = "https://example.test/admin.png"
        branded = copy.deepcopy(seed)
        self.assertFalse(helpers["apply_seed_branding"](seed))
        self.assertEqual(seed, branded)

    def test_mount_mapping_requires_exact_workspace_and_macos(self):
        helpers = setup_helpers()
        expected = self.root / "workspace/.local/casdoor/web"
        direct = str(expected.resolve())
        matches = helpers["mount_source_matches"]
        for platform in ("darwin", "linux"):
            helpers["sys"].platform = platform
            self.assertTrue(matches(direct, expected))
            self.assertEqual(matches("/host_mnt" + direct, expected), platform == "darwin")
            for source in (None, 123, "relative/web", direct + "-other", direct + "/nested",
                           "/host_mnt" + direct + "-other", "/host_mnt/host_mnt" + direct):
                with self.subTest(platform=platform, source=source):
                    self.assertFalse(matches(source, expected))


if __name__ == "__main__":
    unittest.main(verbosity=2)
