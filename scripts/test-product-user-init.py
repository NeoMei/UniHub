#!/usr/bin/env python3
import importlib.util,json,subprocess,tempfile,unittest
from pathlib import Path
ROOT=Path(__file__).resolve().parents[1]
class StarterTests(unittest.TestCase):
 def test_fresh_scaffold_is_independent_buildable_and_public(self):
  with tempfile.TemporaryDirectory() as d:
   target=Path(d)/'notes'
   subprocess.run(['python3',str(ROOT/'scripts/product_user_init.py'),'--output',str(target),'--product-id','notes','--module','example.test/notes'],check=True)
   mod=(target/'go.mod').read_text();self.assertNotIn(str(ROOT),mod);self.assertIn('./third_party/unihub-userkit',mod)
   config=json.loads((target/'config.example.json').read_text());self.assertEqual(config['client_secret'],'');self.assertEqual(config['mode'],'unihub')
   for file in target.rglob('*'):
    if file.is_file():self.assertNotIn(str(ROOT).encode(),file.read_bytes())
   provenance=json.loads((target/'third_party/unihub-userkit/PROVENANCE.json').read_text());self.assertTrue(provenance['files']);self.assertFalse(provenance['published'])
   subprocess.run(['go','test','./...'],cwd=target,check=True)
   before=(target/'main.go').read_bytes()
   result=subprocess.run(['python3',str(ROOT/'scripts/product_user_init.py'),'--output',str(target),'--product-id','notes','--module','example.test/notes'],capture_output=True)
   self.assertNotEqual(result.returncode,0);self.assertEqual(before,(target/'main.go').read_bytes())
 def test_rejects_secret_or_path_in_identifiers(self):
  with tempfile.TemporaryDirectory() as d:
   for product in ['../x','NOTES']:
    r=subprocess.run(['python3',str(ROOT/'scripts/product_user_init.py'),'--output',str(Path(d)/'out'),'--product-id',product,'--module','example.test/notes'],capture_output=True);self.assertNotEqual(r.returncode,0);self.assertFalse((Path(d)/'out').exists())
if __name__=='__main__':unittest.main()
