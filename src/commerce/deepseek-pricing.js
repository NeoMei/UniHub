import { computeUnitLedgerUnits } from './validation.js';

// Official CNY tariff per million tokens, pinned to the gateway meter version.
// https://api-docs.deepseek.com/zh-cn/quick_start/pricing
export const deepSeekProRoute = Object.freeze({
  routeId: 'deepseek:deepseek-v4-pro', modelId: 'deepseek-v4-pro', operation: 'chat',
  meterVersion: 'deepseek-v4-pro-chat-v1', routerRateVersion: 'deepseek-v4-pro-cn-20261017-v1',
  unit: '1K tokens', minBillableQty: '0.001', baseAmountDecimal: '0.027', unihubMultiplier: '1',
});

export function deepSeekProPrices(route, version) {
  if (version.currency !== 'CNY' || route.routeId !== deepSeekProRoute.routeId ||
      route.routerRateVersion !== deepSeekProRoute.routerRateVersion || route.meterVersion !== deepSeekProRoute.meterVersion) return null;
  return [
    ['缓存命中输入', '0.15', '0.30'], ['缓存未命中输入', '4.5', '9'], ['输出', '13.5', '27'],
  ].map(([label, offPeak, peak]) => {
    const units = baseAmountDecimal => computeUnitLedgerUnits({
      baseAmountDecimal, unihubMultiplier: route.unihubMultiplier, creditsPerCurrency: version.creditsPerCurrency,
    });
    const minimum = units(offPeak), maximum = units(peak);
    const credits = value => {
      const fraction = (value % 1000000n).toString().padStart(6, '0').replace(/0+$/, '');
      return String(value / 1000000n) + (fraction ? '.' + fraction : '');
    };
    return { unit: '百万 tokens', component: label, minLedgerUnits: String(minimum), maxLedgerUnits: String(maximum),
      display: `${label}每百万 tokens，空闲 ${credits(minimum)} 积分，高峰 ${credits(maximum)} 积分` };
  });
}
