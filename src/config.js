const loopbackHosts = new Set(['localhost', '127.0.0.1', '[::1]']);
const regionalCurrencies = new Map([['cn', 'CNY'], ['us', 'USD']]);

export function validateRegion(region, currency) {
  if (!regionalCurrencies.has(region) || regionalCurrencies.get(region) !== currency) {
    throw new Error('UNIHUB_REGION and UNIHUB_CURRENCY must be cn/CNY or us/USD');
  }
}

function endpoint(value, variable, requireOrigin = false) {
  let url;
  try {
    url = new URL(value);
  } catch {
    throw new Error(`${variable} must be a valid URL`);
  }

  const validScheme = url.protocol === 'https:' || (url.protocol === 'http:' && loopbackHosts.has(url.hostname));
  if (!validScheme || url.username || url.password || url.search || url.hash || (requireOrigin && url.pathname !== '/')) {
    throw new Error(`${variable} must be HTTPS or local loopback HTTP, with no credentials, query, or fragment`);
  }
  return url;
}

export function readConfig(env) {
  const origin = endpoint(env.UNIHUB_ORIGIN, 'UNIHUB_ORIGIN', true);
  const issuer = endpoint(env.LOGTO_ISSUER, 'LOGTO_ISSUER');
  const sessionKey = Buffer.from(env.UNIHUB_SESSION_KEY ?? '', 'base64url');
  if (sessionKey.length !== 32 || sessionKey.toString('base64url') !== env.UNIHUB_SESSION_KEY) {
    throw new Error('UNIHUB_SESSION_KEY must be a base64url-encoded 32-byte key');
  }
  if (!env.LOGTO_CLIENT_ID || !env.LOGTO_CLIENT_SECRET) {
    throw new Error('LOGTO_CLIENT_ID and LOGTO_CLIENT_SECRET are required');
  }
  const port = Number(env.PORT ?? 14000);
  if (!Number.isInteger(port) || port < 1 || port > 65535) throw new Error('PORT must be between 1 and 65535');
  const region = env.UNIHUB_REGION;
  const currency = env.UNIHUB_CURRENCY;
  validateRegion(region, currency);
  if (!/^postgres(?:ql)?:\/\//.test(env.DATABASE_URL ?? '')) throw new Error('DATABASE_URL must be a PostgreSQL URL');
  let subjects;
  try {
    subjects = JSON.parse(env.UNIHUB_OPERATOR_SUBJECTS ?? '[]');
  } catch {
    throw new Error('UNIHUB_OPERATOR_SUBJECTS must be a JSON array of OIDC subjects');
  }
  if (!Array.isArray(subjects) || subjects.some((entry) => typeof entry !== 'string' || !entry || entry.trim() !== entry)) {
    throw new Error('UNIHUB_OPERATOR_SUBJECTS must be a JSON array of OIDC subjects');
  }

  return {
    host: env.HOST ?? '127.0.0.1', port, origin, issuer,
    clientId: env.LOGTO_CLIENT_ID, clientSecret: env.LOGTO_CLIENT_SECRET, sessionKey,
    databaseUrl: env.DATABASE_URL, region, currency, operatorSubjects: new Set(subjects),
  };
}
