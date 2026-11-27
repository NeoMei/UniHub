import React from 'react';
import { ArrowRight, CheckCircle2, Globe2, Layers, Shield, Sparkles } from 'lucide-react';
import { NavLink } from '../components/Layout.jsx';

export function HomePage({ publicApps, regionInfo, session, onNavigate }) {
  const regionText = regionInfo?.region === 'us' ? '北美服务区 (US · USD)' : '中国大陆服务区 (CN · CNY)';
  return (
    <div className="mx-auto max-w-6xl space-y-12 px-4 py-12 sm:px-6">
      <section className="rounded-3xl border border-[#DEE8EC] bg-white p-8 shadow-xs sm:p-12">
        <div className="grid items-center gap-8 lg:grid-cols-[1.2fr_0.8fr]">
          <div className="space-y-5">
            <span className="inline-flex items-center gap-2 rounded-full bg-[#35C4A3]/15 px-3.5 py-1 text-xs font-semibold text-[#087F8C]">
              <Globe2 className="h-3.5 w-3.5" />
              {regionText} · 全云端模型网关直连
            </span>
            <h1 className="text-3xl font-bold tracking-tight text-[#142B35] sm:text-4xl">
              一个 UniHub 账户，统一管理全系 AI 应用的身份、钱包、席位与 API Key
            </h1>
            <p className="text-base leading-relaxed text-[#142B35]/75">
              个人开发与企业团队各自拥有独立现金账本与积分池；微信/支付宝与 Stripe 按区合规收款；
              面向全五类云端模型 API 提供统一鉴权与可核对的逐笔预扣结算。
            </p>
            <div className="flex flex-wrap gap-3 pt-2">
              {session?.user ? (
                <NavLink
                  href="/console"
                  onNavigate={onNavigate}
                  className="inline-flex items-center gap-2 rounded-xl bg-[#087F8C] px-5 py-3 text-sm font-semibold text-white hover:bg-[#066973]"
                >
                  进入个人 / 企业控制台
                  <ArrowRight className="h-4 w-4" />
                </NavLink>
              ) : (
                <a
                  href="/login"
                  className="inline-flex items-center gap-2 rounded-xl bg-[#087F8C] px-5 py-3 text-sm font-semibold text-white hover:bg-[#066973]"
                >
                  登录 UniHub 开始使用
                  <ArrowRight className="h-4 w-4" />
                </a>
              )}
              <NavLink
                href="/docs"
                onNavigate={onNavigate}
                className="inline-flex items-center gap-2 rounded-xl border border-[#DEE8EC] bg-[#F3F8FA] px-5 py-3 text-sm font-semibold text-[#142B35] hover:border-[#087F8C]"
              >
                查看 API Key 文档
              </NavLink>
            </div>
          </div>
          <div className="rounded-2xl border border-[#DEE8EC] bg-[#F3F8FA] p-6 space-y-4">
            <div className="flex items-center gap-3">
              <img src="/assets/unihub-icon-v1.png" alt="UniHub Icon" className="h-10 w-10 rounded-xl" />
              <div>
                <div className="text-sm font-bold text-[#142B35]">可追溯双账户账本</div>
                <div className="text-xs text-[#142B35]/65">百万分之一积分精度 · 不可变版本快照</div>
              </div>
            </div>
            <ul className="space-y-2.5 text-sm text-[#142B35]/80">
              <li className="flex items-start gap-2">
                <CheckCircle2 className="mt-0.5 h-4 w-4 shrink-0 text-[#087F8C]" />
                <span>个人订阅升档 / 月转年按未用基础积分精确折算返还现金</span>
              </li>
              <li className="flex items-start gap-2">
                <CheckCircle2 className="mt-0.5 h-4 w-4 shrink-0 text-[#087F8C]" />
                <span>企业按应用独立分席位、设成员周期上限，并发分配零超配</span>
              </li>
              <li className="flex items-start gap-2">
                <CheckCircle2 className="mt-0.5 h-4 w-4 shrink-0 text-[#087F8C]" />
                <span>统一模型目录与按次两层锁版结算，拒绝隐藏 Truncation</span>
              </li>
            </ul>
          </div>
        </div>
      </section>

      <section className="grid gap-6 md:grid-cols-3">
        {[
          {
            icon: Shield,
            title: '双区强隔离身份与账库',
            desc: '中国大陆区（CNY + 微信/支付宝）与北美区（USD + Stripe）账库、凭证与回调严格物理与校验隔离。',
          },
          {
            icon: Layers,
            title: '版本化商品与 4D 模型定价',
            desc: '每次单卡上架/下架仅影响对应卡片；历史订单与在途预扣始终绑定成交或授权瞬间的不可变快照。',
          },
          {
            icon: Sparkles,
            title: '全云端多模态 API 统一网关',
            desc: '百炼文本向量、重排、VL 多模态向量、火山方舟 Seedream 4.0 与百度智能云文档解析同一 API Key 通行。',
          },
        ].map((item) => (
          <div key={item.title} className="rounded-2xl border border-[#DEE8EC] bg-white p-6">
            <item.icon className="mb-3 h-6 w-6 text-[#087F8C]" />
            <h2 className="mb-2 text-base font-bold text-[#142B35]">{item.title}</h2>
            <p className="text-sm leading-relaxed text-[#142B35]/70">{item.desc}</p>
          </div>
        ))}
      </section>

      <section className="space-y-4">
        <h2 className="text-lg font-bold text-[#142B35]">已接入 UniHub 生态应用</h2>
        {publicApps.length === 0 ? (
          <div className="rounded-2xl border border-dashed border-[#DEE8EC] bg-white p-6 text-sm text-[#142B35]/65">
            平台尚未启用公开展示应用，运营可在管理后台登记 SuperWagie 或 DocFlow AI。
          </div>
        ) : (
          <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
            {publicApps.map((app) => (
              <div key={app.id} className="rounded-2xl border border-[#DEE8EC] bg-white p-5">
                <div className="text-base font-bold text-[#142B35]">{app.name}</div>
                <p className="mt-1 text-sm text-[#142B35]/70">{app.description || '支持 UniHub 统一登录与积分计费'}</p>
              </div>
            ))}
          </div>
        )}
      </section>
    </div>
  );
}

export function DocsPage() {
  return (
    <div className="mx-auto max-w-4xl space-y-8 px-4 py-10 sm:px-6">
      <div className="space-y-2">
        <h1 className="text-2xl font-bold text-[#142B35]">开发者接入与 API Key 使用文档</h1>
        <p className="text-sm text-[#142B35]/75">面向客户端与接入应用：所有模型调用统一携带 UniHub API Key，按授权版本实时扣减对应应用积分池。</p>
      </div>

      <section className="rounded-2xl border border-[#DEE8EC] bg-white p-6 space-y-4">
        <h2 className="text-base font-bold text-[#142B35]">1. 统一鉴权与模型目录</h2>
        <p className="text-sm text-[#142B35]/75">
          在「控制台 → API Key」为当前个人账户或已获得应用席位的企业账户创建密钥（格式为 <code className="rounded bg-[#F3F8FA] px-1.5 py-0.5 font-mono text-xs">uh_live_...</code>）。
        </p>
        <pre className="overflow-x-auto rounded-xl bg-[#142B35] p-4 text-xs leading-relaxed text-[#F3F8FA]">
{`curl -s https://gateway.unihub.example/v1/models \\
  -H "Authorization: Bearer uh_live_your_unified_api_key"`}
        </pre>
      </section>

      <section className="rounded-2xl border border-[#DEE8EC] bg-white p-6 space-y-3">
        <h2 className="text-base font-bold text-[#142B35]">2. 支持的五类云端 API 能力与计量口径</h2>
        <div className="overflow-x-auto">
          <table className="w-full text-left text-sm">
            <thead>
              <tr className="border-b border-[#DEE8EC] text-xs text-[#142B35]/65">
                <th className="py-2 pr-4">模型标识</th>
                <th className="py-2 pr-4">云端路由能力</th>
                <th className="py-2">计量单位与口径</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-[#DEE8EC]">
              <tr><td className="py-2.5 pr-4 font-mono text-xs">text-embedding-v4</td><td className="py-2.5 pr-4">文本向量化</td><td>1K tokens（按官方输入用量计量）</td></tr>
              <tr><td className="py-2.5 pr-4 font-mono text-xs">gte-rerank-v2</td><td className="py-2.5 pr-4">语义重排序</td><td>1K tokens（按官方输入计费用量）</td></tr>
              <tr><td className="py-2.5 pr-4 font-mono text-xs">seedream-4.0</td><td className="py-2.5 pr-4">图像生成</td><td>张（按实际成功生成张数结算）</td></tr>
              <tr><td className="py-2.5 pr-4 font-mono text-xs">qwen3-vl-embedding</td><td className="py-2.5 pr-4">多模态向量</td><td>1K tokens（区分文本与视觉 Token）</td></tr>
              <tr><td className="py-2.5 pr-4 font-mono text-xs">paddleocr-vl</td><td className="py-2.5 pr-4">文档解析（百度云 API）</td><td>页（按成功解析页数与官方单价结算）</td></tr>
            </tbody>
          </table>
        </div>
      </section>

      <section className="rounded-2xl border border-[#DEE8EC] bg-white p-6 space-y-2">
        <h2 className="text-base font-bold text-[#142B35]">3. 常见业务响应码</h2>
        <ul className="list-disc space-y-1.5 pl-5 text-sm text-[#142B35]/75">
          <li><code className="font-mono text-xs">402 insufficient_credits</code>：积分池不足以覆盖当前请求授权上限（系统直接拒绝，绝不私自截断 <code className="font-mono text-xs">max_tokens</code>）。</li>
          <li><code className="font-mono text-xs">403 app_seat_required</code>：企业账户成员尚未获配该应用的有效席位，或席位已被管理员收回。</li>
          <li><code className="font-mono text-xs">409 member_cycle_limit</code>：企业成员已触达当期订阅周期内设定的个人消费上限。</li>
        </ul>
      </section>
    </div>
  );
}

export function AboutPage() {
  return (
    <div className="mx-auto max-w-4xl space-y-8 px-4 py-10 sm:px-6">
      <div className="space-y-2">
        <h1 className="text-2xl font-bold text-[#142B35]">关于 UniHub 与开源组件声明</h1>
        <p className="text-sm text-[#142B35]/75">UniHub 致力于以透明、可审计、可独立演进的微核架构支撑多应用身份、计费与全云端模型网关接入。</p>
      </div>
      <section className="rounded-2xl border border-[#DEE8EC] bg-white p-6 space-y-3 text-sm text-[#142B35]/80">
        <h2 className="text-base font-bold text-[#142B35]">开源组件与许可证合规说明</h2>
        <p>UniHub 核心后端采用独立业务服务与版本化内网协议设计，底层依赖或按需集成以下开源软件并严格保留其版权声明与许可证：</p>
        <ul className="list-disc space-y-1.5 pl-5">
          <li>身份认证服务基于 <strong>Logto OSS</strong>（MPL-2.0），按部署区独立签发身份。</li>
          <li>模型网关侧基于 <strong>LLMRouter / New API 衍生网关组件</strong>（AGPL-3.0），保留上游 <code className="font-mono text-xs">NOTICE</code> 与署名，面向对应网络用户提供匹配运行版本的完整对应源码。</li>
          <li>企业对账与出账按需适配 <strong>Lago OSS</strong>（AGPL-3.0），通过异步防抖队列隔离运行。</li>
        </ul>
      </section>
    </div>
  );
}

export function BrandPage() {
  const swatches = [
    { name: '主青色 Primary Teal', hex: '#087F8C' },
    { name: '辅薄荷 Mint Accent', hex: '#35C4A3' },
    { name: '深海灰 Dark Ink', hex: '#142B35' },
    { name: '浅冰底 Surface', hex: '#F3F8FA' },
    { name: '边框灰 Border', hex: '#DEE8EC' },
  ];
  return (
    <div className="mx-auto max-w-4xl space-y-8 px-4 py-10 sm:px-6">
      <h1 className="text-2xl font-bold text-[#142B35]">UniHub 品牌与视觉规范</h1>
      <div className="grid gap-6 sm:grid-cols-2">
        <div className="rounded-2xl border border-[#DEE8EC] bg-white p-6 space-y-3">
          <div className="text-xs font-semibold text-[#142B35]/60">横版主标志</div>
          <img src="/assets/unihub-logo-v1.png" alt="UniHub Logo" className="h-12 w-auto" />
        </div>
        <div className="rounded-2xl border border-[#DEE8EC] bg-white p-6 space-y-3">
          <div className="text-xs font-semibold text-[#142B35]/60">方形图标徽标</div>
          <img src="/assets/unihub-icon-v1.png" alt="UniHub Icon" className="h-12 w-12 rounded-xl" />
        </div>
      </div>
      <div className="grid gap-4 sm:grid-cols-5">
        {swatches.map((s) => (
          <div key={s.hex} className="rounded-xl border border-[#DEE8EC] bg-white p-3">
            <div className="mb-2 h-14 rounded-lg border border-[#DEE8EC]" style={{ backgroundColor: s.hex }} />
            <div className="text-xs font-semibold text-[#142B35]">{s.name}</div>
            <div className="font-mono text-xs text-[#142B35]/65">{s.hex}</div>
          </div>
        ))}
      </div>
    </div>
  );
}
