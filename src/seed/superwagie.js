import { applyDeclarativeSeed } from './apply-seed.js';

export async function seedSuperWagie(pool, { operatorUserId, region, currency }) {
  const isUs = region === 'us';
  const cpc = isUs ? 1500 : 200;

  return applyDeclarativeSeed(pool, {
    appName: 'SuperWagie',
    appDescription: '全能 AI Agent 工作台：多模型协同、多模态生图与智能文档处理',
    redirectUris: ['https://superwagie.example.test/auth/callback'],
    operatorUserId,
    region,
    currency,
    creditsPerCurrency: cpc,
    personalCards: [
      {
        tierId: 'pro_monthly', tierRank: 20, productType: 'membership',
        rule: { billingCycle: 'monthly', priceMinor: isUs ? 1500 : 9900, baseCredits: 2000, bonusCredits: 500 },
        display: { name: 'Pro 月付', description: '高频个人工作流', benefits: ['每月 2,000 基础积分 + 500 赠送积分', '全五类云 API 模型目录接入'] },
      },
      {
        tierId: 'pro_yearly', tierRank: 30, productType: 'membership',
        rule: { billingCycle: 'yearly', priceMinor: isUs ? 9900 : 67200, baseCredits: 2000, bonusCredits: 500 },
        display: { name: 'Pro 年付', description: '按月发放全年享优惠', benefits: ['连续 12 个月每月 2,000+500 积分', '等价月均大幅节省'] },
      },
      {
        tierId: 'max_monthly', tierRank: 40, productType: 'membership',
        rule: { billingCycle: 'monthly', priceMinor: isUs ? 2900 : 19900, baseCredits: 5000, bonusCredits: 1000 },
        display: { name: 'Max 月付', description: '重度多模态与批量分析', benefits: ['每月 5,000 基础积分 + 1,000 赠送积分', '高额度优先调度'] },
      },
      {
        tierId: 'ultra_monthly', tierRank: 60, productType: 'membership',
        rule: { billingCycle: 'monthly', priceMinor: isUs ? 5900 : 39900, baseCredits: 12000, bonusCredits: 3000 },
        display: { name: 'Ultra 月付', description: '旗舰高频工作站档位', benefits: ['每月 12,000 基础积分 + 3,000 赠送积分', '高并发配额支持'] },
      },
      {
        tierId: 'pack_1000', tierRank: 90, productType: 'credit_pack',
        rule: { priceMinor: isUs ? 600 : 3900, packCredits: 1000, validDays: 90, packEligibility: 'active_member' },
        display: { name: '加量积分包 1,000', description: '90 天有效期（限有效会员加购）', benefits: ['到账即用，1,000 独立积分', '先到期先扣'] },
      },
    ],
    enterpriseCards: [
      {
        tierId: 'ent_monthly', tierRank: 100, productType: 'membership',
        rule: { billingCycle: 'monthly', priceMinor: isUs ? 2200 : 14900, baseCredits: 3000, bonusCredits: 500 },
        display: { name: 'Enterprise 席位月付', description: '按席位订阅共享企业积分池', benefits: ['每席每月 3,000 基础 + 500 赠送积分', '支持管理员分席位与成员周期上限'] },
      },
      {
        tierId: 'ent_yearly', tierRank: 110, productType: 'membership',
        rule: { billingCycle: 'yearly', priceMinor: isUs ? 22000 : 149000, baseCredits: 3000, bonusCredits: 500 },
        display: { name: 'Enterprise 席位年付', description: '年度企业席位合同价', benefits: ['12 期按月发放共享积分池', '随时增席按剩余周期自动折算'] },
      },
      {
        tierId: 'ent_pack_10000', tierRank: 190, productType: 'credit_pack',
        rule: { priceMinor: isUs ? 5200 : 35000, packCredits: 10000, validDays: 180, packEligibility: 'active_member' },
        display: { name: '企业共享池积分包 10,000', description: '180 天企业共享加量包', benefits: ['10,000 积分注入企业共享池', '所有已分配应用席位成员共用'] },
      },
    ],
    routes: [
      {
        modelId: 'text-embedding-v4', routeId: 'ali:text-embedding-v4', operation: 'embedding',
        unit: '1K tokens', minBillableQty: '0.04', meterVersion: 'm_ali_emb_v4', routerRateVersion: 'r_ali_v1',
        baseAmountDecimal: isUs ? '0.00007' : '0.0005', unihubMultiplier: '1.00',
      },
      {
        modelId: 'gte-rerank-v2', routeId: 'ali:gte-rerank-v2', operation: 'rerank',
        unit: '1K tokens', minBillableQty: '0.1', meterVersion: 'm_ali_rerank_v2', routerRateVersion: 'r_ali_v1',
        baseAmountDecimal: isUs ? '0.00014' : '0.001', unihubMultiplier: '1.10',
      },
      {
        modelId: 'seedream-4.0', routeId: 'volc:seedream-4.0', operation: 'image_generation',
        unit: '张', minBillableQty: '1', meterVersion: 'm_volc_seedream_v1', routerRateVersion: 'r_volc_v1',
        baseAmountDecimal: isUs ? '0.03' : '0.20', unihubMultiplier: '1.15',
      },
      {
        modelId: 'qwen3-vl-embedding', routeId: 'ali:qwen3-vl-embedding', operation: 'multimodal_embedding',
        unit: '1K tokens', minBillableQty: '0.1', meterVersion: 'm_ali_vl_emb_v1', routerRateVersion: 'r_ali_v1',
        baseAmountDecimal: isUs ? '0.00028' : '0.002', unihubMultiplier: '1.10',
      },
      {
        modelId: 'paddleocr-vl', routeId: 'baidu:paddleocr-vl', operation: 'ocr_parse',
        unit: '页', minBillableQty: '1', meterVersion: 'm_baidu_ocr_vl_v1', routerRateVersion: 'r_baidu_v1',
        baseAmountDecimal: isUs ? '0.005' : '0.03', unihubMultiplier: '1.20',
      },
    ],
  });
}
