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

function normalizePem(value) {
  return typeof value === 'string' && value.trim() ? value.replace(/\\n/g, '\n') : '';
}

function positiveMs(value, fallback) {
  if (value === undefined || value === '') return fallback;
  const n = Number(value);
  if (!Number.isFinite(n) || n < 500) throw new Error('Worker interval must be >= 500ms');
  return n;
}

function readPaymentConfig(env) {
  const paymentConfig = {
    allowSandboxSettle: env.UNIHUB_ALLOW_SANDBOX_SETTLE === 'true',
    allowUnsignedTestWebhooks: false,
  };

  const wxSerial = env.WECHAT_PLATFORM_SERIAL;
  const wxPlatformPub = normalizePem(env.WECHAT_PLATFORM_PUBLIC_KEY);
  const wxApiV3Key = env.WECHAT_APIV3_KEY;
  if (wxSerial && wxPlatformPub) {
    paymentConfig.wechat = {
      platformPublicKeys: { [wxSerial]: wxPlatformPub },
      apiV3Key: wxApiV3Key || undefined,
      appId: env.WECHAT_APP_ID || undefined,
      merchantId: env.WECHAT_MCH_ID || undefined,
      certificateSerial: env.WECHAT_CERT_SERIAL || undefined,
      privateKey: normalizePem(env.WECHAT_PRIVATE_KEY) || undefined,
      notifyUrl: env.WECHAT_NOTIFY_URL || undefined,
      apiBaseUrl: env.WECHAT_API_BASE_URL || undefined,
    };
  }

  const aliPublic = normalizePem(env.ALIPAY_PUBLIC_KEY);
  if (aliPublic) {
    paymentConfig.alipay = {
      alipayPublicKey: aliPublic,
      appId: env.ALIPAY_APP_ID || undefined,
      privateKey: normalizePem(env.ALIPAY_PRIVATE_KEY) || undefined,
      notifyUrl: env.ALIPAY_NOTIFY_URL || undefined,
      returnUrl: env.ALIPAY_RETURN_URL || undefined,
      gatewayUrl: env.ALIPAY_GATEWAY_URL || undefined,
    };
  }

  if (env.STRIPE_WEBHOOK_SECRET || env.STRIPE_SECRET_KEY) {
    paymentConfig.stripe = {
      webhookSecret: env.STRIPE_WEBHOOK_SECRET || undefined,
      secretKey: env.STRIPE_SECRET_KEY || undefined,
      successUrl: env.STRIPE_SUCCESS_URL || undefined,
      cancelUrl: env.STRIPE_CANCEL_URL || undefined,
      apiBaseUrl: env.STRIPE_API_BASE_URL || undefined,
    };
  }

  return paymentConfig;
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

  let fxRates = null;
  if (env.UNIHUB_FX_RATES_JSON) {
    const parsedFx = JSON.parse(env.UNIHUB_FX_RATES_JSON);
    if (!parsedFx || typeof parsedFx !== 'object' || Array.isArray(parsedFx)) {
      throw new Error('UNIHUB_FX_RATES_JSON must be a JSON object mapping currencies to rates');
    }
    fxRates = parsedFx;
  }

  const allowedVersionsList = (env.UNIHUB_ALLOWED_GATEWAY_VERSIONS ?? '1.0,1.1')
    .split(',')
    .map((s) => s.trim())
    .filter(Boolean);

  return {
    host: env.HOST ?? '127.0.0.1',
    port,
    origin,
    issuer,
    clientId: env.LOGTO_CLIENT_ID,
    clientSecret: env.LOGTO_CLIENT_SECRET,
    sessionKey,
    databaseUrl: env.DATABASE_URL,
    region,
    currency,
    operatorSubjects: new Set(subjects),
    gatewayServiceToken: env.UNIHUB_GATEWAY_SERVICE_TOKEN ?? '',
    allowedGatewayVersions: new Set(allowedVersionsList),
    fxRates,
    lagoConfig: {
      enabled: env.LAGO_ENABLED === 'true',
      baseUrl: env.LAGO_API_URL ?? '',
      apiKey: env.LAGO_API_KEY ?? '',
    },
    routerAdminConfig: {
      baseUrl: env.LLMROUTER_ADMIN_URL ?? '',
      token: env.LLMROUTER_ADMIN_TOKEN ?? '',
    },
    paymentConfig: readPaymentConfig(env),
    workerConfig: {
      enabled: env.UNIHUB_WORKER_ENABLED !== 'false',
      outboxIntervalMs: positiveMs(env.UNIHUB_OUTBOX_INTERVAL_MS, 5000),
      holdReconcileIntervalMs: positiveMs(env.UNIHUB_HOLD_RECONCILE_INTERVAL_MS, 30_000),
      renewalIntervalMs: positiveMs(env.UNIHUB_RENEWAL_INTERVAL_MS, 60_000),
      creditExpireIntervalMs: positiveMs(env.UNIHUB_CREDIT_EXPIRE_INTERVAL_MS, 60_000),
    },
  };
}
