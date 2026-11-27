import { readFile } from 'node:fs/promises';
import secureSession from '@fastify/secure-session';
import fastify from 'fastify';
import * as oidc from 'openid-client';
import { accountContext, registerAccountRoutes } from './accounts.js';
import { issueAppLogin, readAppLogin, registerAppRoutes } from './apps.js';
import { registerCommerceRoutes } from './commerce/index.js';
import { registerBillingRoutes } from './billing/index.js';
import { registerPaymentRoutes } from './payments/index.js';
import { migrateDatabase } from './database.js';
import { createSession, readSession, revokeSession } from './identity.js';
import { renderPortal } from './portal.js';

const loginLifetime = 10 * 60 * 1000;
const sessionLifetime = 8 * 60 * 60;

export async function buildApp({
  origin, issuer, clientId, clientSecret, sessionKey, pool, region, currency, operatorSubjects = new Set(),
  paymentConfig = {},
}) {
  await migrateDatabase(pool, region, currency);
  const execution = [oidc.enableNonRepudiationChecks];
  if (issuer.protocol === 'http:') execution.push(oidc.allowInsecureRequests);
  const client = await oidc.discovery(issuer, clientId, clientSecret, oidc.ClientSecretBasic(clientSecret), {
    execute: execution,
  });
  const callback = new URL('/auth/callback', origin);
  const stylesheet = await readFile(new URL('./portal.css', import.meta.url), 'utf8');
  const app = fastify({
    logger: true, logController: new fastify.LogController({ disableRequestLogging: true }), bodyLimit: 4096,
  });

  app.register(secureSession, {
    key: sessionKey,
    expiry: sessionLifetime,
    cookieName: 'unihub_session',
    cookie: {
      path: '/', httpOnly: true, sameSite: 'lax', secure: origin.protocol === 'https:', maxAge: sessionLifetime,
    },
  });
  app.decorateRequest('rawBody', '');
  app.addContentTypeParser('application/json', { parseAs: 'string' }, (request, body, done) => {
    request.rawBody = body;
    if (!body) return done(null, undefined);
    try {
      done(null, JSON.parse(body));
    } catch (error) {
      error.statusCode = 400;
      done(error);
    }
  });
  app.addContentTypeParser('application/x-www-form-urlencoded', { parseAs: 'string' }, (request, body, done) => {
    request.rawBody = body;
    done(null, Object.fromEntries(new URLSearchParams(body)));
  });
  app.addHook('onRequest', async (_request, reply) => {
    reply.headers({
      'cache-control': 'no-store',
      'content-security-policy': "default-src 'none'; style-src 'self'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'",
      'referrer-policy': 'no-referrer',
      'x-content-type-options': 'nosniff',
    });
  });

  async function requireSession(request, reply) {
    request.auth = await readSession(pool, request.session.get('sessionId'));
    if (!request.auth) return reply.code(401).send({ error: 'login_required' });
  }

  async function verifyCsrf(request, reply) {
    const token = request.headers['x-csrf-token'] ?? request.body?.csrf;
    if (token !== request.auth.csrf) return reply.code(403).send({ error: 'invalid_request' });
  }

  app.decorateRequest('auth', null);
  const auth = { preHandler: requireSession };
  const write = { preHandler: [requireSession, verifyCsrf] };
  const isPlatformOperator = (session) => session.issuer === issuer.href && operatorSubjects.has(session.subject);
  registerAccountRoutes(app, pool, auth, write);
  registerAppRoutes(app, pool, auth, write, isPlatformOperator);
  registerCommerceRoutes(app, pool, auth, write, isPlatformOperator, { region, currency });
  registerPaymentRoutes(app, pool, auth, write, { region, currency, paymentConfig });
  registerBillingRoutes(app, pool, auth, write, isPlatformOperator, { region, currency });

  app.get('/', async (request, reply) => {
    const session = await readSession(pool, request.session.get('sessionId'));
    const context = session ? await accountContext(pool, session) : null;
    const failed = request.query.login === 'failed';
    return reply.type('text/html; charset=utf-8').send(renderPortal(context, failed));
  });
  app.get('/portal.css', async (_request, reply) => reply.type('text/css; charset=utf-8').send(stylesheet));
  app.get('/health', async () => ({ status: 'ok' }));

  app.get('/login', async (request, reply) => {
    const appParameters = ['app_id', 'redirect_uri', 'state'].some((key) => Object.hasOwn(request.query, key));
    const handoff = appParameters ? await readAppLogin(pool, request.query) : null;
    if (appParameters && !handoff) return reply.code(400).send({ error: 'invalid_app_login' });

    const session = await readSession(pool, request.session.get('sessionId'));
    if (session) {
      if (!handoff) return reply.redirect('/');
      const target = await issueAppLogin(pool, session.id, handoff);
      return target ? reply.redirect(target, 303) : reply.code(400).send({ error: 'invalid_app_login' });
    }

    const verifier = oidc.randomPKCECodeVerifier();
    const state = oidc.randomState();
    const nonce = oidc.randomNonce();
    const challenge = await oidc.calculatePKCECodeChallenge(verifier);
    const authorization = oidc.buildAuthorizationUrl(client, {
      redirect_uri: callback.href,
      scope: 'openid profile email',
      code_challenge: challenge,
      code_challenge_method: 'S256',
      state,
      nonce,
    });
    request.session.regenerate();
    request.session.set('login', { verifier, state, nonce, handoff, expiresAt: Date.now() + loginLifetime });
    return reply.redirect(authorization.href);
  });

  app.get('/auth/callback', async (request, reply) => {
    const attempt = request.session.get('login');
    const responseUrl = new URL(callback);
    responseUrl.search = new URL(request.url, origin).search;
    const states = responseUrl.searchParams.getAll('state');
    if (!attempt || Date.now() >= attempt.expiresAt || states.length !== 1 || states[0] !== attempt.state) {
      return reply.redirect('/?login=failed', 303);
    }

    request.session.regenerate();
    let claims;
    try {
      const response = await oidc.authorizationCodeGrant(client, responseUrl, {
        pkceCodeVerifier: attempt.verifier,
        expectedState: attempt.state,
        expectedNonce: attempt.nonce,
      });
      claims = response.claims();
    } catch {
      return reply.redirect('/?login=failed', 303);
    }

    if (!claims || claims.iss !== issuer.href || typeof claims.sub !== 'string' || !claims.sub || claims.sub.includes('\0')) {
      return reply.redirect('/?login=failed', 303);
    }
    const sessionId = await createSession(pool, {
      issuer: claims.iss,
      subject: claims.sub,
      name: typeof claims.name === 'string' ? claims.name.replaceAll('\0', '').slice(0, 160) : '',
      email: typeof claims.email === 'string' ? claims.email.replaceAll('\0', '').slice(0, 254) : '',
      csrf: oidc.randomState(),
      lifetime: sessionLifetime,
    });
    request.session.set('sessionId', sessionId);
    if (!attempt.handoff) return reply.redirect('/', 303);
    const target = await issueAppLogin(pool, sessionId, attempt.handoff);
    return target ? reply.redirect(target, 303) : reply.redirect('/?login=failed', 303);
  });

  app.post('/logout', async (request, reply) => {
    const session = await readSession(pool, request.session.get('sessionId'));
    if (!session) return reply.redirect('/', 303);
    if (request.body?.csrf !== session.csrf) return reply.code(403).send({ error: 'invalid_request' });

    await revokeSession(pool, session.id);
    request.session.delete();
    const signOut = oidc.buildEndSessionUrl(client, {
      client_id: clientId,
      post_logout_redirect_uri: new URL('/', origin).href,
    });
    return reply.redirect(signOut.href, 303);
  });

  return app;
}
