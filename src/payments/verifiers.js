import { createDecipheriv, createHmac, createVerify, timingSafeEqual } from 'node:crypto';

function headerValue(headers, name) {
  const lower = name.toLowerCase();
  for (const [key, value] of Object.entries(headers ?? {})) {
    if (key.toLowerCase() === lower) return Array.isArray(value) ? value[0] : value;
  }
  return undefined;
}

function resolvePublicKey(keys, serial) {
  if (!keys) return null;
  if (typeof keys === 'string') return keys;
  if (keys instanceof Map) return keys.get(serial) ?? null;
  if (typeof keys === 'object') return keys[serial] ?? null;
  return null;
}

export function decryptWechatResource({ ciphertext, nonce, associatedData = '' }, apiV3Key) {
  if (typeof ciphertext !== 'string' || typeof nonce !== 'string') return null;
  const keyBuf = Buffer.isBuffer(apiV3Key) ? apiV3Key : Buffer.from(String(apiV3Key ?? ''), 'utf8');
  if (keyBuf.length !== 32) return null;

  const raw = Buffer.from(ciphertext, 'base64');
  if (raw.length < 16) return null;
  const data = raw.subarray(0, raw.length - 16);
  const authTag = raw.subarray(raw.length - 16);
  const decipher = createDecipheriv('aes-256-gcm', keyBuf, Buffer.from(nonce, 'utf8'));
  if (associatedData) decipher.setAAD(Buffer.from(associatedData, 'utf8'));
  decipher.setAuthTag(authTag);
  const plaintext = Buffer.concat([decipher.update(data), decipher.final()]).toString('utf8');
  return JSON.parse(plaintext);
}

export function verifyWechatV3({ headers, rawBody, platformPublicKeys, apiV3Key, maxSkewSeconds = 300, nowSeconds = Math.floor(Date.now() / 1000) }) {
  const timestamp = headerValue(headers, 'wechatpay-timestamp');
  const nonce = headerValue(headers, 'wechatpay-nonce');
  const serial = headerValue(headers, 'wechatpay-serial');
  const signature = headerValue(headers, 'wechatpay-signature');
  if (!timestamp || !nonce || !serial || !signature || typeof rawBody !== 'string') {
    return { valid: false, reason: 'missing_wechat_headers' };
  }
  const ts = Number(timestamp);
  if (!Number.isFinite(ts) || (maxSkewSeconds > 0 && Math.abs(nowSeconds - ts) > maxSkewSeconds)) {
    return { valid: false, reason: 'stale_wechat_timestamp' };
  }
  const publicKey = resolvePublicKey(platformPublicKeys, serial);
  if (!publicKey) return { valid: false, reason: 'unknown_wechat_serial' };

  const message = `${timestamp}\n${nonce}\n${rawBody}\n`;
  try {
    const verifier = createVerify('RSA-SHA256');
    verifier.update(message, 'utf8');
    verifier.end();
    if (!verifier.verify(publicKey, Buffer.from(signature, 'base64'))) {
      return { valid: false, reason: 'invalid_wechat_signature' };
    }
  } catch {
    return { valid: false, reason: 'invalid_wechat_signature' };
  }

  let parsed;
  try {
    parsed = JSON.parse(rawBody);
  } catch {
    return { valid: false, reason: 'invalid_json_body' };
  }

  if (parsed?.resource?.ciphertext && apiV3Key) {
    try {
      const resource = decryptWechatResource({
        ciphertext: parsed.resource.ciphertext,
        nonce: parsed.resource.nonce,
        associatedData: parsed.resource.associated_data ?? '',
      }, apiV3Key);
      if (!resource) return { valid: false, reason: 'wechat_decrypt_failed' };
      return { valid: true, payload: parsed, decrypted: resource };
    } catch {
      return { valid: false, reason: 'wechat_decrypt_failed' };
    }
  }
  return { valid: true, payload: parsed, decrypted: null };
}

export function buildAlipaySignString(params) {
  if (!params || typeof params !== 'object') return '';
  return Object.keys(params)
    .filter((key) => key !== 'sign' && key !== 'sign_type' && params[key] !== '' && params[key] !== null && params[key] !== undefined)
    .sort()
    .map((key) => `${key}=${params[key]}`)
    .join('&');
}

export function verifyAlipayRsa2({ params, alipayPublicKey }) {
  if (!params || typeof params !== 'object' || typeof params.sign !== 'string' || !alipayPublicKey) {
    return { valid: false, reason: 'missing_alipay_signature' };
  }
  if (params.sign_type && params.sign_type !== 'RSA2') {
    return { valid: false, reason: 'unsupported_alipay_sign_type' };
  }
  const content = buildAlipaySignString(params);
  if (!content) return { valid: false, reason: 'empty_alipay_params' };
  try {
    const verifier = createVerify('RSA-SHA256');
    verifier.update(content, 'utf8');
    verifier.end();
    const ok = verifier.verify(alipayPublicKey, Buffer.from(params.sign, 'base64'));
    return ok ? { valid: true, payload: params } : { valid: false, reason: 'invalid_alipay_signature' };
  } catch {
    return { valid: false, reason: 'invalid_alipay_signature' };
  }
}

export function verifyStripeWebhook({
  signatureHeader,
  rawBody,
  webhookSecret,
  toleranceSeconds = 300,
  nowSeconds = Math.floor(Date.now() / 1000),
}) {
  if (typeof signatureHeader !== 'string' || typeof rawBody !== 'string' || !webhookSecret) {
    return { valid: false, reason: 'missing_stripe_signature' };
  }
  const parts = signatureHeader.split(',').map((item) => item.trim());
  let timestamp = null;
  const signatures = [];
  for (const part of parts) {
    const idx = part.indexOf('=');
    if (idx <= 0) continue;
    const key = part.slice(0, idx);
    const val = part.slice(idx + 1);
    if (key === 't') timestamp = val;
    if (key === 'v1') signatures.push(val);
  }
  const ts = Number(timestamp);
  if (!timestamp || !Number.isFinite(ts) || signatures.length === 0) {
    return { valid: false, reason: 'malformed_stripe_signature' };
  }
  if (toleranceSeconds > 0 && Math.abs(nowSeconds - ts) > toleranceSeconds) {
    return { valid: false, reason: 'stale_stripe_timestamp' };
  }
  const expectedHex = createHmac('sha256', webhookSecret).update(`${timestamp}.${rawBody}`, 'utf8').digest('hex');
  const expectedBuf = Buffer.from(expectedHex, 'hex');
  const matched = signatures.some((sig) => {
    if (!/^[0-9a-f]+$/i.test(sig)) return false;
    const sigBuf = Buffer.from(sig, 'hex');
    return sigBuf.length === expectedBuf.length && timingSafeEqual(sigBuf, expectedBuf);
  });
  return matched ? { valid: true } : { valid: false, reason: 'invalid_stripe_signature' };
}
