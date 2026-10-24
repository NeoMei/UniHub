#!/usr/bin/env python3
"""Explicit sandbox acceptance using only JSON HTTP, no UniHub UI or IdP admin API.

Requires UNIHUB_E2E_BASE_URL, UNIHUB_E2E_CLIENT_ID, UNIHUB_E2E_REDIRECT_URI.
Creates two unique ordinary users, one test team, and sandbox financial objects.
Optional UNIHUB_E2E_SERVICE_TOKEN enables synthetic router settlement against
UNIHUB_E2E_INTERNAL_BASE_URL (default public base); never calls an actual model.
No password, token, authorization binding or service credential is printed.
"""
import argparse, base64, hashlib, json, os, secrets, sys, time
from pathlib import Path
from urllib.error import HTTPError
from urllib.parse import urlsplit
from urllib.request import Request, build_opener, HTTPRedirectHandler

class NoRedirect(HTTPRedirectHandler):
    def redirect_request(self, *args, **kwargs):
        raise RuntimeError('unexpected redirect in JSON-only acceptance')

class Client:
    def __init__(self, base, client, redirect, origin=None):
        parsed = urlsplit(base)
        if parsed.scheme != 'https' and not (parsed.scheme == 'http' and parsed.hostname in {'localhost','127.0.0.1','::1'}):
            raise RuntimeError('HTTPS or explicit loopback required')
        self.base, self.client, self.redirect, self.origin = base.rstrip('/'), client, redirect, origin
        self.token = ''
        self.opener = build_opener(NoRedirect())
        self.bindings = []
    def request(self, method, path, body=None, key=None, expected=(200,), token=None, origin=None):
        headers = {'Accept':'application/json'}
        if self.origin or origin: headers['Origin'] = origin or self.origin
        if key: headers['Idempotency-Key'] = key
        credential = self.token if token is None else token
        if credential: headers['Authorization'] = 'Bearer ' + credential
        if body is not None: headers['Content-Type'] = 'application/json'
        req = Request(self.base + path, data=json.dumps(body).encode() if body is not None else None, headers=headers, method=method)
        try:
            response = self.opener.open(req, timeout=40)
        except HTTPError as error:
            response = error
        with response:
            status, content_type = response.status, response.headers.get('Content-Type','')
            raw = response.read()
            if status not in expected:
                try: code = json.loads(raw).get('error', 'unknown')
                except Exception: code = 'non-json'
                raise RuntimeError(f'{method} {path.split("?")[0]}: HTTP {status} ({code})')
            if not raw: return None
            if 'application/json' not in content_type:
                # A blocked private route may be rejected by the HTTPS proxy
                # before the JSON application. Only explicit error assertions
                # accept that denial; every successful product call is JSON.
                if status >= 400: return {'error':'proxy_denied'}
                raise RuntimeError('non-JSON success response')
            result = json.loads(raw)
            if self.origin and status < 400 and response.headers.get('Access-Control-Allow-Origin') != self.origin:
                raise RuntimeError('missing exact product CORS response')
            return result
    def login(self, username, password):
        verifier, state, nonce = [secrets.token_urlsafe(32) for _ in range(3)]
        challenge = base64.urlsafe_b64encode(hashlib.sha256(verifier.encode()).digest()).decode().rstrip('=')
        auth = self.request('POST','/v1/identity/authorize/password',{
            'client_id':self.client,'redirect_uri':self.redirect,'scope':'openid profile email',
            'state':state,'nonce':nonce,'code_challenge':challenge,'code_challenge_method':'S256',
            'username':username,'password':password},token='')
        if auth['state'] != state: raise RuntimeError('state mismatch')
        body = {'grant_type':'authorization_code','client_id':self.client,'redirect_uri':self.redirect,
                'authorization_id':auth['authorization_id'],'code':auth['code'],'code_verifier':verifier}
        tokens = self.request('POST','/v1/identity/token',body,token='')
        if tokens['token_type'] != 'Bearer' or 'refresh_token' in tokens: raise RuntimeError('invalid token contract')
        self.token = tokens['access_token']
        self.request('POST','/v1/identity/token',body,token='',expected=(400,))
        self.bindings += [state, nonce, verifier, challenge, auth['code'], password]
        return tokens

def run(args):
    base, client, redirect = [os.environ[k] for k in ('UNIHUB_E2E_BASE_URL','UNIHUB_E2E_CLIENT_ID','UNIHUB_E2E_REDIRECT_URI')]
    origin = os.environ.get('UNIHUB_E2E_ORIGIN')
    owner, member = [Client(base,client,redirect,origin) for _ in range(2)]
    suffix = secrets.token_hex(6)
    credentials = []
    for index, api in enumerate((owner,member)):
        username, password = f'he2e{suffix}{index}', 'Aa9!' + secrets.token_urlsafe(24)
        key = f'e2e-register-{suffix}-{index}'
        body = {'client_id':client,'username':username,'password':password,'display_name':'Headless acceptance'}
        registered = api.request('POST','/v1/identity/registrations',body,key,expected=(201,))
        replay = api.request('POST','/v1/identity/registrations',body,key)
        assert registered['status'] == replay['status'] == 'CREATED'
        assert registered['registration_id'] == replay['registration_id']
        tokens = api.login(username,password)
        credentials.append({'username':username,'password':password,'access_token':tokens['access_token']})
    me, member_me = owner.request('GET','/v1/me'), member.request('GET','/v1/me')
    personal = next(a for a in owner.request('GET','/v1/accounts') if a['kind']=='personal')
    account = personal['id']; path = '/v1/accounts/' + account
    capabilities = owner.request('GET','/v1/capabilities')
    assert capabilities['sandbox'] and not capabilities['real_payments_configured']
    assert capabilities['unified_wallet_recharge'] is False
    owner.request('GET','/v1/me',expected=(403,),origin='https://unregistered.invalid')
    recharge = owner.request('POST',path+'/recharges',{'product_id':'superwagie','amount':'10000000'},'e2e-recharge-'+suffix,expected=(201,))
    duplicate = owner.request('POST',path+'/recharges',{'product_id':'superwagie','amount':'10000000'},'e2e-recharge-'+suffix,expected=(201,))
    assert recharge['id'] == duplicate['id']
    operation = owner.request('POST',path+'/recharges/'+recharge['id']+'/sandbox-settle',expected=(202,))
    for _ in range(20):
        operation = owner.request('GET',path+'/operations/'+operation['id'])
        if operation['status']=='APPLIED': break
        time.sleep(.2)
    assert operation['status']=='APPLIED'
    catalog = owner.request('GET','/v1/catalog')
    assert any(o['id']=='sandbox-sw-month-1' and o['sandbox'] for o in catalog)
    order = owner.request('POST',path+'/orders',{'offer_id':'sandbox-sw-month-1'},'e2e-buy-'+suffix,expected=(202,))
    for _ in range(20):
        orders = owner.request('GET',path+'/orders')
        order = next(o for o in orders if o['id']==order['id'])
        if order['fulfillment_status']=='FULFILLED': break
        time.sleep(.2)
    assert order['payment_status']=='PAID' and order['fulfillment_status']=='FULFILLED'
    team = owner.request('POST','/v1/accounts',{'name':'Headless acceptance '+suffix},expected=(201,))
    team_path = '/v1/accounts/' + team['id']
    invitation = owner.request('POST',team_path+'/invitations',{'user_id':member_me['id'],'role':'member'},expected=(201,))
    assert any(i['id']==invitation['id'] for i in member.request('GET','/v1/invitations'))
    member.request('POST','/v1/invitations/'+invitation['id']+'/accept')
    assert len(owner.request('GET',team_path+'/members')) == 2
    member.request('GET',team_path+'/wallets',expected=(403,))
    member.request('GET',path+'/resources',expected=(403,404))
    team_resources = member.request('GET',team_path+'/resources')
    assert 'wallets' not in team_resources
    router_settlement = False
    service_token = os.environ.get('UNIHUB_E2E_SERVICE_TOKEN')
    if service_token:
        router = Client(os.environ.get('UNIHUB_E2E_INTERNAL_BASE_URL',base),client,redirect)
        grant = owner.request('POST',path+'/model-credentials',{'models':['synthetic-model'],'task_id':'headless-e2e'},expected=(201,))
        call_id = 'headless-e2e-'+suffix
        reserve = {'credential':grant['token'],'call_id':call_id,'request_digest':'synthetic-e2e-'+suffix,
                   'rate_id':os.environ.get('UNIHUB_E2E_RATE_ID','headless-e2e-rate'),'amount':'100','task_id':'headless-e2e'}
        router.request('POST','/internal/v1/calls/reserve',reserve,token=service_token)
        settlement = {'usage':{'input':'3','output':'4','cache_read':'0','cache_write':'0','reasoning':'0'},'provider_cost':'0'}
        router.request('POST','/internal/v1/calls/'+call_id+'/settle',settlement,token=service_token)
        owner.request('POST','/internal/v1/calls/'+call_id+'/settle',settlement,expected=(401,404))
        calls, summary = owner.request('GET',path+'/calls'), owner.request('GET',path+'/usage/summary')
        assert calls['total']=='1' and summary['settled_calls']=='1' and summary['charged']=='4'
        detail = owner.request('GET',path+'/calls/'+call_id)
        assert detail['charged']=='4' and detail['origin_product_id']=='superwagie'
        assert 'provider_cost' not in json.dumps([calls,summary,detail])
        router_settlement = True
    resources = owner.request('GET',path+'/resources')
    sw_cash = next(w['amount'] for w in resources['wallets'] if w['product_id']=='superwagie')
    assert sw_cash=='9000000' and resources['credits']['spendable']==('999996' if router_settlement else '1000000')
    result = {'result':'PASS','json_only':True,'html_pages_loaded':0,'real_payment':False,
        'router_synthetic_settlement':router_settlement,'owner_id':me['id'],'member_id':member_me['id'],
        'personal_account_id':account,'team_account_id':team['id'],'client_id':client,
        'cash':sw_cash,'credit':resources['credits']['spendable'],
        'checks':['registration-replay','password-pkce-token','token-replay-denied','exact-origin-cors','recharge-replay','sandbox-settlement','purchase','resources','team-invitation-acceptance','member-finance-denied','cross-account-denied']}
    if args.private_evidence:
        target=Path(args.private_evidence); target.parent.mkdir(parents=True,exist_ok=True)
        fd=os.open(target,os.O_CREAT|os.O_EXCL|os.O_WRONLY,0o600)
        with os.fdopen(fd,'w') as stream: json.dump({'credentials':credentials,'audit_markers':owner.bindings+member.bindings},stream)
    if args.result:
        Path(args.result).write_text(json.dumps(result,indent=2)+'\n')
    print(json.dumps(result))

if __name__=='__main__':
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--result',help='public result JSON path')
    parser.add_argument('--private-evidence',help='optional private credentials/audit markers for controlled follow-up validation')
    options=parser.parse_args()
    try: run(options)
    except Exception as error:
        print('Headless acceptance failed: '+str(error),file=sys.stderr)
        sys.exit(1)
