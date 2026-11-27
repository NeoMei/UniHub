import { applyDeclarativeSeed } from './apply-seed.js';

export async function seedDocFlow(pool, { operatorUserId, region, currency }) {
  const isUs = region === 'us';
  const cpc = isUs ? 1500 : 200;

  return applyDeclarativeSeed(pool, {
    appName: 'DocFlow AI',
    appDescription: '企业智能文档解析、OCR 提取与语义检索平台',
    redirectUris: ['https://docflow.example.test/auth/callback'],
    operatorUserId,
    region,
    currency,
    creditsPerCurrency: cpc,
    personalCards: [
      {
        tierId: 'standard_monthly', tierRank: 10, productType: 'membership',
        rule: { billingCycle: 'monthly', priceMinor: isUs ? 900 : 5900, baseCredits: 1200, bonusCredits: 200 },
        display: { name: '标准月付', description: '日常文档扫描与语义检索', benefits: ['每月 1,200 基础 + 200 赠送积分', '支持百炼向量与百度智能云 PaddleOCR API'] },
      },
      {
        tierId: 'pro_monthly', tierRank: 30, productType: 'membership',
        rule: { billingCycle: 'monthly', priceMinor: isUs ? 1900 : 12900, baseCredits: 3200, bonusCredits: 600 },
        display: { name: '专业月付', description: '大批量 PDF/票据解析工作台', benefits: ['每月 3,200 基础 + 600 赠送积分', '大文件优先排队'] },
      },
      {
        tierId: 'ocr_pack', tierRank: 80, productType: 'credit_pack',
        rule: { priceMinor: isUs ? 400 : 2200, packCredits: 600, validDays: 60, packEligibility: 'active_member' },
        display: { name: 'OCR 加速包', description: '60 天文档解析专项加量包', benefits: ['600 独立积分', '先到期先扣'] },
      },
    ],
    enterpriseCards: [
      {
        tierId: 'ent_seat_monthly', tierRank: 100, productType: 'membership',
        rule: { billingCycle: 'monthly', priceMinor: isUs ? 1800 : 11900, baseCredits: 2500, bonusCredits: 500 },
        display: { name: '企业席位 ¥119/席', description: '团队文档协作共享池', benefits: ['每席 2,500 基础 + 500 赠送积分', '支持随时增席折算与成员限额'] },
      },
    ],
    routes: [
      {
        modelId: 'paddleocr-vl', routeId: 'baidu:paddleocr-vl', operation: 'ocr_parse',
        unit: '页', minBillableQty: '1', meterVersion: 'm_baidu_ocr_vl_v1', routerRateVersion: 'r_baidu_v1',
        baseAmountDecimal: isUs ? '0.005' : '0.03', unihubMultiplier: '0.95',
      },
      {
        modelId: 'text-embedding-v4', routeId: 'ali:text-embedding-v4', operation: 'embedding',
        unit: '1K tokens', minBillableQty: '0.04', meterVersion: 'm_ali_emb_v4', routerRateVersion: 'r_ali_v1',
        baseAmountDecimal: isUs ? '0.00007' : '0.0005', unihubMultiplier: '1.00',
      },
    ],
  });
}
