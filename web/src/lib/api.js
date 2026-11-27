const UNITS_PER_CREDIT = 1_000_000n;

export async function apiRequest(url, { method = 'GET', body, csrf } = {}) {
  const headers = {};
  if (body !== undefined) headers['content-type'] = 'application/json';
  if (method !== 'GET' && csrf) headers['x-csrf-token'] = csrf;
  const response = await fetch(url, {
    method,
    headers,
    credentials: 'same-origin',
    body: body !== undefined ? JSON.stringify(body) : undefined,
  });
  if (response.status === 204) return { ok: true, status: 204, data: null };
  const text = await response.text();
  let data = null;
  try {
    data = text ? JSON.parse(text) : null;
  } catch {
    data = { raw: text };
  }
  if (!response.ok) {
    const err = new Error(data?.error || `HTTP_${response.status}`);
    err.status = response.status;
    err.code = data?.error || `http_${response.status}`;
    err.data = data;
    throw err;
  }
  return { ok: true, status: response.status, data };
}

export function formatMoney(minorStr, currency = 'CNY') {
  const raw = BigInt(minorStr ?? '0');
  const sign = raw < 0n ? '-' : '';
  const abs = raw < 0n ? -raw : raw;
  const whole = abs / 100n;
  const frac = (abs % 100n).toString().padStart(2, '0');
  const symbol = currency === 'USD' ? '$' : '¥';
  return `${sign}${symbol}${whole.toLocaleString('en-US')}.${frac}`;
}

export function formatCredits(unitsStr) {
  const raw = BigInt(unitsStr ?? '0');
  const sign = raw < 0n ? '-' : '';
  const abs = raw < 0n ? -raw : raw;
  const whole = abs / UNITS_PER_CREDIT;
  const rem = abs % UNITS_PER_CREDIT;
  if (rem === 0n) return `${sign}${whole.toLocaleString('en-US')}`;
  if (whole === 0n && rem < 10_000n) {
    return `${sign}0.${rem.toString().padStart(6, '0').slice(0, 4)}`;
  }
  const twoDec = (rem / 10_000n).toString().padStart(2, '0');
  return `${sign}${whole.toLocaleString('en-US')}.${twoDec}`;
}

export function formatDateTime(iso) {
  if (!iso) return '-';
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return String(iso);
  return d.toLocaleString('zh-CN', { hour12: false });
}

export const ROLE_LABELS = {
  owner: '企业所有者',
  admin: '企业管理员',
  member: '企业成员',
};
