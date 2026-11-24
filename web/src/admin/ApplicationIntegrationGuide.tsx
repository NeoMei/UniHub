import { Panel, Table } from '../ui';
import { go } from './types';
import './application-integrations.css';

const registryFields = [
  ['产品标识 / 实例', '固定标识。生产和测试使用各自的实例。', 'agentwiki / default'],
  ['应用名称', '显示在 UniHub 登录页和「我的应用」。', 'AgentWiki'],
  ['产品来源（Origin）', '站点的协议、域名和可选端口。生产使用 HTTPS。', 'https://agentwiki.quukk.com'],
  ['应用启动地址', '从「我的应用」点击后，用于发起 UniHub 单点登录。', 'https://agentwiki.quukk.com/api/auth/unihub/start?returnTo=%2Fdashboard'],
  ['精确回调地址', '接收浏览器携带的授权结果。填写完整路径，回调地址使用同一来源且不含查询参数。', 'https://agentwiki.quukk.com/api/auth/unihub/callback'],
  ['原账号登录地址', '应用登录后发起将原账号交接给 UniHub。', 'https://agentwiki.quukk.com/unihub?entry=hub'],
];

const steps = [
  {
    title: '准备应用端接口和验收账号',
    app: '提供站点来源、启动地址、回调地址和原账号登录地址。准备一个有实际应用权限的试点账号。确认应用能用原用户 ID 找到原账号，并处理登录、锁定和注销。',
    hub: '超管对照本页字段准备表，确认走原账号接入。使用中心 OIDC 或接入模型与账务的应用，可以按「三方接入与计费准备」配置对应能力。',
    result: '各地址用途明确。应用团队知道如何维护回调和试点账号名单。',
  },
  {
    title: '创建草稿，签发服务凭据',
    app: '向超管提供已确认的六组字段。通过应用服务端的密钥管理接收凭据。',
    hub: '打开「应用接入与管理 → 新建接入」，填写字段并创建草稿。打开详情，记下 UniHub 来源和客户端 ID，然后点击「签发服务凭据」并保存本次显示的明文。',
    result: '管理页显示草稿和凭据已配置。凭据明文仅在签发后可见，丢失时可在草稿或暂停状态重新签发。',
  },
  {
    title: '配置产品服务端，启用试点',
    app: '将 UniHub 来源、客户端 ID、回调地址和服务凭据配置到后端。只向选定的原账号开放试点。回调校验浏览器状态和 PKCE，通过后端换取会话，再按原用户 ID 加载原有权限。',
    hub: '检查详情里的客户端 ID、回调和凭据状态，点击「启用试点」。试点从详情里的原账号登录地址进入。',
    result: 'UniHub 显示试点中。旧有用户仍可从应用原入口登录，试点用户可以开始身份交接。',
  },
  {
    title: '完成双向登录并核对原权限',
    app: '在没有 UniHub 登录态的浏览器窗口中，先用原账号经试点入口进入 UniHub。已有 UniHub 账号时，由本人验证并确认绑定。保持 UniHub 登录态，清除原应用站点的本地登录态，再从「我的应用」打开原应用。',
    hub: '刷新应用详情，分别确认「原产品账号 → UniHub」与「UniHub 我的应用 → 原产品账号」已验证。',
    result: '两条路径均已验证。回跳进入同一个原账号，原有工作区、角色和 Key 仍可使用。',
  },
  {
    title: '正式发布，按需暂停',
    app: '按试点结果扩大应用端允许范围。将原账号锁定、删除、密码变更和会话注销同步给 UniHub。',
    hub: '两项验证通过后点击「正式发布」。如需暂停，在详情点击「暂停接入」。恢复时重新启用试点并验收两个方向。',
    result: '已发布应用的原账号入口可在 UniHub 登录页使用。具有应用访问权限的用户可从「我的应用」进入。',
  },
];

export function ApplicationIntegrationGuide() {
  return (
    <div className="application-integrations application-guide">
      <div className="admin-page-heading">
        <div>
          <h1>应用接入手册</h1>
          <p>适用于已有用户和登录体系、希望在 UniHub 提供统一入口的应用。</p>
        </div>
        <a href="/admin/applications" onClick={(event) => { event.preventDefault(); go('/admin/applications'); }}>返回应用接入与管理</a>
      </div>

      <Panel title="接入前准备">
        <div className="application-detail-grid">
          <section>
            <h3>接入系统提供</h3>
            <ul>
              <li>可访问的站点来源、应用启动地址、精确回调地址和原账号登录地址。</li>
              <li>稳定的原用户 ID、后端会话校验和认证版本，以及账号与会话的注销信号。</li>
              <li>服务端密钥存储、试点用户名单，以及用于核对原有权限的账号或工作区。</li>
            </ul>
          </section>
          <section>
            <h3>UniHub 提供</h3>
            <ul>
              <li>应用登记、客户端 ID、精确回调配置和一次性显示的服务凭据。</li>
              <li>原账号关联和浏览器会话交接、应用访问权限、统一登录入口及「我的应用」。</li>
              <li>双向登录的服务端核验，以及草稿、试点、发布、暂停状态和变更审计。</li>
            </ul>
          </section>
        </div>
        <p>登记、发凭据、发布和暂停由平台超管使用 <a href="/auth/login?method=central">UniHub 账号登录</a> 后执行。用户在自己的会话中验证和关联账号，通过「我的应用」使用已获授权的应用。</p>
      </Panel>

      <Panel title="登记字段 · AgentWiki 示例">
        <Table headers={['登记字段', '填写说明', 'AgentWiki 当前示例']}>
          {registryFields.map(([field, description, value]) => (
            <tr key={field}><th scope="row">{field}</th><td>{description}</td><td><code>{value}</code></td></tr>
          ))}
        </Table>
        <p className="application-hint">登记自己的应用时，请使用其实际域名和接口路径。应用需先部署这些接口，UniHub 根据登记值校验和跳转。</p>
      </Panel>

      <Panel title="按步骤接入">
        <ol className="application-guide-steps">
          {steps.map((step) => (
            <li key={step.title}>
              <h3>{step.title}</h3>
              <dl>
                <dt>接入系统操作</dt><dd>{step.app}</dd>
                <dt>UniHub 超管操作</dt><dd>{step.hub}</dd>
                <dt>完成标志</dt><dd>{step.result}</dd>
              </dl>
            </li>
          ))}
        </ol>
      </Panel>

      <Panel title="应用开发者对接">
        <p>应用后端使用 <code>Authorization: Bearer &lt;服务凭据&gt;</code> 调用 <code>/internal/v1/product-runtime/identity/</code> 下列接口。浏览器使用 UniHub 提供的跳转地址。原密码和原有资源权限由接入系统校验及管理。</p>
        <Table headers={['环节', '调用顺序']}>
          <tr><th scope="row">关联已验证的原账号</th><td><code>enroll</code>，或由用户确认后顺序使用 <code>link/start</code>、<code>link/preview</code>、<code>link/complete</code>。</td></tr>
          <tr><th scope="row">应用登录 UniHub</th><td><code>/auth/product/start</code> → 应用验证原会话 → <code>authenticate</code> → <code>browser/approve</code>（携带 <code>X-UniHub-Actor-Authorization: Bearer &lt;用户证明&gt;</code>）→ UniHub 完成交接。</td></tr>
          <tr><th scope="row">从 UniHub 进入应用</th><td><code>/auth/product/authorize</code> → 应用回调 → <code>session/exchange</code> → 按原用户 ID 建立应用会话。</td></tr>
          <tr><th scope="row">账号和会话状态</th><td><code>source-lifecycle</code>、<code>source-session/revoke</code>、<code>session/verify</code>。</td></tr>
        </Table>
        <p className="application-hint">核对接入参数时，可在应用详情展开「AgentWiki 兼容适配器配置示例」。使用中心 OIDC、模型或账务能力的应用可从这里继续准备客户端和权限。 <a href="/admin/onboarding">三方接入与计费准备</a></p>
      </Panel>
      <Panel title="接入遇到问题时">
        <ul>
          <li>回调失败时，在应用详情逐字比对客户端 ID、站点来源和回调路径，确认产品服务端读取了当前凭据。</li>
          <li>试点未核验时，在没有 UniHub 登录态的浏览器窗口重试原账号登录。完成回跳后，在管理页刷新状态。</li>
          <li>用户看不到应用卡片时，先核对原账号是否已关联，再核对 UniHub 应用访问权限和应用当前状态。</li>
        </ul>
      </Panel>
    </div>
  );
}
