import assert from 'node:assert/strict';
import { test } from 'node:test';
import {
  allocateCreditUnits,
  creditsToUnits,
  normalizeCheckoutInput,
  parseAmountMinor,
  providerAllowedForRegion,
} from '../src/billing/validation.js';

test('账区只接受匹配的支付渠道和币种', () => {
  assert.equal(providerAllowedForRegion('cn', 'CNY', 'wechat'), true);
  assert.equal(providerAllowedForRegion('cn', 'CNY', 'alipay'), true);
  assert.equal(providerAllowedForRegion('cn', 'CNY', 'stripe'), false);
  assert.equal(providerAllowedForRegion('us', 'USD', 'stripe'), true);
  assert.equal(providerAllowedForRegion('us', 'USD', 'wechat'), false);
  assert.equal(providerAllowedForRegion('cn', 'USD', 'wechat'), false);
});

test('现金和积分输入使用整数精度', () => {
  assert.equal(parseAmountMinor(9900), 9900n);
  assert.equal(parseAmountMinor('9900'), 9900n);
  assert.equal(parseAmountMinor('0'), 0n);
  assert.equal(parseAmountMinor('09'), null);
  assert.equal(parseAmountMinor(0.5), null);
  assert.equal(parseAmountMinor('-1'), null);
  assert.equal(creditsToUnits(2000), 2_000_000_000n);
  assert.equal(creditsToUnits('0.05'), 50_000n);
});

test('积分预扣按最早到期和批次 ID 顺序分配', () => {
  const batches = [
    { id: 'b-late', expiresAt: '2026-12-31T00:00:00Z', availableUnits: 900n },
    { id: 'b-early', expiresAt: '2026-11-30T00:00:00Z', availableUnits: 40n },
    { id: 'b-same-2', expiresAt: '2026-11-30T00:00:00Z', availableUnits: 80n },
    { id: 'b-same-1', expiresAt: '2026-11-30T00:00:00Z', availableUnits: 100n },
  ];
  assert.deepEqual(
    allocateCreditUnits(batches, 180n),
    [
      { batchId: 'b-early', units: 40n },
      { batchId: 'b-same-1', units: 100n },
      { batchId: 'b-same-2', units: 40n },
    ],
  );
  assert.throws(() => allocateCreditUnits(batches, 1_121n), /insufficient/i);
});

test('统一结账输入固定应用、商品、数量和订单类型', () => {
  const input = normalizeCheckoutInput({
    appId: '123e4567-e89b-12d3-a456-426614174000',
    tierId: 'tier_pro_m',
    quantity: 2,
    orderType: 'seat_expand',
    autoRenew: true,
  });
  assert.deepEqual(input, {
    appId: '123e4567-e89b-12d3-a456-426614174000',
    versionId: null,
    tierId: 'tier_pro_m',
    quantity: 2,
    orderType: 'seat_expand',
    autoRenew: true,
  });
  assert.equal(normalizeCheckoutInput({ appId: 'bad', tierId: 'x', quantity: 1 }), null);
  assert.equal(normalizeCheckoutInput({
    appId: '123e4567-e89b-12d3-a456-426614174000', tierId: 'x', quantity: 0,
  }), null);
});
