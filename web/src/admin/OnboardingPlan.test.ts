import { execFileSync } from 'node:child_process';
import { mkdtempSync, readFileSync, rmSync, symlinkSync, existsSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { resolve } from 'node:path';
import { expect, test } from 'vitest';
import { buildOnboardingPlan } from './onboardingPlan';
const input={product:'notes',callback:"https://notes.example.com/cb?q='$(id)'",origin:'https://notes.example.com',issuer:'https://identity.example.com',clientId:"notes-web'$(id)",application:'app-notes',instance:'default',profile:'hybrid',scopes:['accounts:read','catalog:read'],maturity:'existing',billing:'product',browserSSO:true,sandbox:true};
test('generated offline commands run against the actual Python argparse contracts and quote all untrusted arguments',()=>{
 const plan=buildOnboardingPlan(input);
 const directory=mkdtempSync(resolve(tmpdir(),'unihub-onboarding-'));
 try{
  symlinkSync(resolve('..','scripts'),resolve(directory,'scripts'),'dir');
  // Only offline prepare/validate commands are executable; apply/readback require explicit review.
  execFileSync('sh',['-c',plan.commands],{cwd:directory,encoding:'utf8'});
  const clientPlan=JSON.parse(readFileSync(resolve(directory,'notes-client-plan.json'),'utf8'));
  expect(clientPlan.client.client_id).toBe(input.clientId);expect(clientPlan.client.redirect_uris).toEqual([input.callback]);
  expect(clientPlan.client.scopes).toEqual(input.scopes);
  expect(JSON.parse(readFileSync(resolve(directory,'notes-billing.json'),'utf8'))).toEqual([{product_id:'notes',version:1,preset:'hybrid'}]);
  expect(existsSync(resolve(directory,'product-clients.json'))).toBe(false);
  expect(plan.commands).toContain('# python3 scripts/product_client.py apply --plan');
  expect(plan.commands).toContain('# python3 scripts/product_client.py readback --plan');
  expect(plan.commands).not.toContain('--sandbox');expect(plan.commands).not.toContain('unihubctl');
  expect(JSON.stringify(plan)).not.toContain('HEADLESS_MANAGEMENT_CLIENT_SECRET=');
 }finally{rmSync(directory,{recursive:true,force:true});}
});
test.each([{...input,product:'../bad'},{...input,callback:'https://evil.test/%20'},{...input,origin:'https://notes.example.com/'},{...input,issuer:'http://example.com'},{...input,scopes:['cash:payment']},{...input,application:'app-built-in'}])('rejects unsupported or unsafe public client input',value=>{expect(()=>buildOnboardingPlan(value)).toThrow();});
