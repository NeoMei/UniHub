import { createSign, randomBytes } from 'node:crypto';
import { verifyWechatV3 } from './verifiers.js';

function signRsaSha256(privateKeyPem, message) {
  const signer = createSign('RSA-SHA256');
  signer.update(message, 'utf8');
  signer.end();
  return signer.sign(privateKeyPem, 'base64');
}

export function formatMinorToDecimal(amountMinor) {
  const raw = typeof amountMinor === 'bigint' ? amountMinor : BigInt(amountMinor);
  const whole = raw / 100n;
  const frac = (raw % 100n).toString().padStart(2, '0');
  return `${whole}.${frac}`;
}

function formatChinaTime(date) {
  const cst = new Date(date.getTime() + 8 * 3600_000);
  const p = (n) => String(n).padStart(2, '0');
  return `${cst.getUTCFullYear()}-${p(cst.getUTCMonth() + 1)}-${p(cst.getUTCDate())} `
    + `${p(cst.getUTCHours())}:${p(cst.getUTCMinutes())}:${p(cst.getUTCSeconds())}`;
}

export async function createWechatNativeCheckout({
  config,
  orderId,
  amountMinor,
  description = 'UniHub Wallet Recharge',
  expiresAt = new Date(Date.now() + 15 * 60_000),
}) {
  const baseUrl = String(config.apiBaseUrl ?? 'https://api.mch.weixin.qq.com').replace(/\/+$/, '');
  const path = '/v3/pay/transactions/native';
  const payload = {
    appid: config.appId,
    mchid: config.merchantId,
    description,
    out_trade_no: orderId,
    time_expire: expiresAt.toISOString(),
    notify_url: config.notifyUrl,
    amount: { total: Number(amountMinor), currency: 'CNY' },
  };
  const body = JSON.stringify(payload);
  const timestamp = String(Math.floor(Date.now() / 1000));
  const nonce = randomBytes(16).toString('hex');
  const signature = signRsaSha256(
    config.privateKey,
    `POST\n${path}\n${timestamp}\n${nonce}\n${body}\n`,
  );

  const response = await fetch(`${baseUrl}${path}`, {
    method: 'POST',
    headers: {
      accept: 'application/json',
      'content-type': 'application/json',
      authorization: `WECHATPAY2-SHA256-RSA2048 mchid="${config.merchantId}",nonce_str="${nonce}",timestamp="${timestamp}",serial_no="${config.certificateSerial}",signature="${signature}"`,
    },
    body,
  });

  const rawBody = await response.text();
  if (!response.ok) {
    throw new Error(`WeChat Native checkout failed (${response.status}): ${rawBody.slice(0, 200)}`);
  }

  if (config.platformPublicKeys) {
    const headers = Object.fromEntries(response.headers.entries());
    const verified = verifyWechatV3({
      headers,
      rawBody,
      platformPublicKeys: config.platformPublicKeys,
    });
    if (!verified.valid) {
      throw new Error(`WeChat response signature invalid: ${verified.reason}`);
    }
  }

  const json = JSON.parse(rawBody);
  if (!json.code_url) throw new Error('WeChat Native response missing code_url');
  return { kind: 'qrcode', provider: 'wechat', codeUrl: json.code_url, expiresAt: expiresAt.toISOString() };
}

export function createAlipayPageCheckout({
  config,
  orderId,
  amountMinor,
  description = 'UniHub Wallet Recharge',
  expiresAt = new Date(Date.now() + 15 * 60_000),
  returnUrl = null,
}) {
  const gatewayUrl = config.gatewayUrl ?? 'https://openapi.alipay.com/gateway.do';
  const bizContent = JSON.stringify({
    out_trade_no: orderId,
    total_amount: formatMinorToDecimal(amountMinor),
    subject: description,
    product_code: 'FAST_INSTANT_TRADE_PAY',
    time_expire: formatChinaTime(expiresAt),
  });
  const params = {
    app_id: config.appId,
    method: 'alipay.trade.page.pay',
    format: 'JSON',
    charset: 'utf-8',
    sign_type: 'RSA2',
    timestamp: formatChinaTime(new Date()),
    version: '1.0',
    notify_url: config.notifyUrl,
    return_url: returnUrl || config.returnUrl,
    biz_content: bizContent,
  };
  const signString = Object.keys(params)
    .filter((k) => params[k] !== '' && params[k] !== null && params[k] !== undefined)
    .sort()
    .map((k) => `${k}=${params[k]}`)
    .join('&');
  params.sign = signRsaSha256(config.privateKey, signString);
  const qs = new URLSearchParams(params).toString();
  return { kind: 'redirect', provider: 'alipay', redirectUrl: `${gatewayUrl}?${qs}`, expiresAt: expiresAt.toISOString() };
}

export async function createStripeCheckoutSession({
  config,
  orderId,
  amountMinor,
  currency = 'USD',
  description = 'UniHub Wallet Recharge',
  returnUrl = null,
}) {
  const baseUrl = String(config.apiBaseUrl ?? 'https://api.stripe.com').replace(/\/+$/, '');
  const successUrl = returnUrl || config.successUrl;
  const cancelUrl = config.cancelUrl || successUrl;
  const form = new URLSearchParams({
    mode: 'payment',
    'line_items[0][price_data][currency]': currency.toLowerCase(),
    'line_items[0][price_data][product_data][name]': description,
    'line_items[0][price_data][unit_amount]': String(amountMinor),
    'line_items[0][quantity]': '1',
    'metadata[paymentOrderId]': orderId,
    success_url: successUrl,
    cancel_url: cancelUrl,
  });

  const response = await fetch(`${baseUrl}/v1/checkout/sessions`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${config.secretKey}`,
      'content-type': 'application/x-www-form-urlencoded',
    },
    body: form.toString(),
  });
  const rawBody = await response.text();
  if (!response.ok) {
    throw new Error(`Stripe Checkout creation failed (${response.status}): ${rawBody.slice(0, 200)}`);
  }
  const json = JSON.parse(rawBody);
  if (!json.url) throw new Error('Stripe Checkout response missing url');
  return {
    kind: 'redirect',
    provider: 'stripe',
    redirectUrl: json.url,
    providerSessionId: json.id,
  };
}

export async function createProviderCheckoutSession(provider, paymentConfig, params) {
  const cfg = paymentConfig?.[provider];
  if (!cfg) return null;
  if (provider === 'wechat' && cfg.appId && cfg.merchantId && cfg.certificateSerial && cfg.privateKey && cfg.notifyUrl) {
    return createWechatNativeCheckout({ config: cfg, ...params });
  }
  if (provider === 'alipay' && cfg.appId && cfg.privateKey && cfg.notifyUrl && (params.returnUrl || cfg.returnUrl)) {
    return createAlipayPageCheckout({ config: cfg, ...params });
  }
  if (provider === 'stripe' && cfg.secretKey && (params.returnUrl || cfg.successUrl)) {
    return createStripeCheckoutSession({ config: cfg, ...params });
  }
  return null;
}
