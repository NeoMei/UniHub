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
    hub: '打开「应用接入与管理 → 新建接入」，填写字段并创建草稿。打开详情，记下 UniHub 来源和客户端 ID。点击「签发服务凭据」，将明文保存到产品服务端的密钥配置，然后点击「已保存凭据」。',
    result: '管理页显示草稿和凭据已配置。凭据丢失时可在草稿或暂停状态重新签发。确认重新签发后，旧凭据立即失效，产品后端需要切换到新凭据。',
  },
  {
    title: '配置产品服务端，启用试点',
    app: '将 UniHub 来源、客户端 ID、回调地址和服务凭据配置到后端。只向选定的原账号开放试点。回调校验浏览器状态和 PKCE，通过后端换取会话，再按原用户 ID 加载原有权限。',
    hub: '检查详情里的客户端 ID、回调和凭据状态，点击「启用试点」。试点从详情里的原账号登录地址进入。',
    result: 'UniHub 显示试点中。旧有用户仍可从应用原入口登录，试点用户可以开始身份交接。',
  },
  {
    title: '完成双向登录并核对原权限',
    app: '用独立的浏览器配置，经试点入口以原账号进入 UniHub。若需关联已有 UniHub 账号，由本人登录并确认关联。完成关联后，再用一份没有 UniHub 登录态的浏览器配置，从原账号入口重新登录一次，以便 UniHub 签发会话。保留该 UniHub 会话，通过浏览器站点数据工具仅清除原应用站点的 Cookie 和本地登录存储，再从「我的应用」打开原应用。产品的「退出登录」会触发服务端注销，应单独验证。',
    hub: '使用超管会话刷新应用详情，分别确认「原产品账号 → UniHub」与「UniHub 我的应用 → 原产品账号」已验证。两项分别记录 UniHub 会话签发和回调会话交换，原账号和原权限由验收人员在应用内核对。',
    result: '两条路径均已验证。回跳进入同一个原账号，原有工作区、角色和 Key 仍可使用。',
  },
  {
    title: '正式发布，按需暂停',
    app: '按试点结果扩大应用端允许范围。将原账号锁定、删除、密码变更和会话注销同步给 UniHub。',
    hub: '两项验证通过后点击「正式发布」。如需暂停，在详情点击「暂停接入」。暂停期间可编辑接入配置并保留产品和实例标识。修改产品来源或精确回调地址会使旧凭据失效，需重新签发并更新产品后端。恢复时重新启用试点并验收两个方向。',
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
        <p>以下运行时接口由应用后端发送 JSON POST 请求，路径前缀为 <code>/internal/v1/product-runtime/identity/</code>，请求头为 <code>Authorization: Bearer &lt;服务凭据&gt;</code>。<code>/auth/product/</code> 下的 GET 跳转由浏览器访问。原密码和原有资源权限由接入系统校验及管理。</p>
        <p>以下认证事件包含 <code>local_user_id</code>、<code>authentication_id</code>、<code>method</code>、<code>authenticated_at</code>（Unix 秒）和 <code>auth_version</code>。每次新的认证使用新的事件 ID。</p>
        <Table headers={['环节', '调用顺序']}>
          <tr><th scope="row">接入原账号</th><td>验证原账号后调用 <code>enroll</code>，传入 <code>client_id</code>、<code>authentication</code> 认证事件、<code>display_name</code> 和 <code>email</code>。需绑定已有 UniHub 账号时，执行下一行的确认流程。</td></tr>
          <tr><th scope="row">关联已有账号</th><td>
            调用 <code>link/start</code>，传入 <code>client_id</code>、<code>state</code>、<code>nonce</code>、<code>code_challenge</code> 和 <code>authentication</code>（须为原账号主动认证）。保留 <code>link_id</code> 并让浏览器打开返回的 <code>authorize_url</code>。用户在 UniHub 验证身份后，回调收到 <code>link=1</code>、<code>code</code> 和 <code>state</code>。
            校验浏览器状态，用 <code>client_id</code>、<code>link_id</code>、<code>code</code>、<code>redirect_uri</code>、<code>nonce</code>、<code>code_verifier</code> 和 <code>authentication</code> 调用 <code>link/preview</code>，向用户显示返回的目标账号。用户确认后，将该账号的 <code>central_user_id</code> 和同组交换参数提交给 <code>link/complete</code>。
          </td></tr>
          <tr><th scope="row">应用登录 UniHub</th><td>
            原账号登录后，浏览器带 <code>client_id</code> 和 <code>state</code> 打开 <code>/auth/product/start</code>。UniHub 将 <code>handoff</code> 和 <code>state</code> 送到登记的应用回调。
            应用校验状态和原会话，向 <code>authenticate</code> 提交认证事件。用返回的 <code>actor_token</code> 设置 <code>X-UniHub-Actor-Authorization: Bearer &lt;actor_token&gt;</code>，调用 <code>browser/approve</code>，提交 <code>client_id</code>、<code>handoff_id</code>、<code>state</code>、<code>native_session_id</code>、<code>native_session_expires_at</code>（Unix 秒）和 <code>native_handoff_generation</code>（平台管理接入使用 <code>1</code>）。
            让同一浏览器打开返回的 <code>finish_url</code>，再校验应用回调中的 <code>handoff_done=1</code> 和 <code>state</code>。
          </td></tr>
          <tr><th scope="row">从 UniHub 进入应用</th><td>
            应用启动地址将浏览器送至 <code>/auth/product/authorize</code>，传入 <code>client_id</code>、登记的 <code>redirect_uri</code>、<code>state</code>、<code>nonce</code>、<code>code_challenge</code> 和 <code>code_challenge_method=S256</code>。
            校验回调的 <code>broker=1</code> 和 <code>state</code> 后，用 <code>client_id</code>、<code>redirect_uri</code>、<code>code</code>、<code>nonce</code> 和 <code>code_verifier</code> 调用 <code>session/exchange</code>。按返回的 <code>identity.binding.local_user_id</code> 建立应用会话，并在后端保留 <code>source_proof_token</code>、<code>source_proof_expires_at</code> 及 <code>identity.binding.user_id</code>。
          </td></tr>
          <tr><th scope="row">账号和会话状态</th><td>
            用 <code>source-lifecycle</code> 同步 <code>local_user_id</code>、<code>operation_id</code>、<code>previous_auth_version</code>、<code>next_auth_version</code> 及可选的 <code>state</code>（<code>active</code>、<code>locked</code> 或 <code>deleted</code>）。
            原生会话注销时调用 <code>source-session/revoke</code>，传入 <code>local_user_id</code>、<code>native_session_id</code> 和 <code>expires_at</code>（Unix 秒）。
            用 <code>source_proof_token</code> 调用 <code>session/verify</code>，将 <code>identity.binding.user_id</code> 作为 <code>central_user_id</code> 传入。校验成功返回 HTTP 204。
          </td></tr>
        </Table>
        <p className="application-hint">核对接入参数时，可在应用详情展开「AgentWiki 兼容适配器配置示例」。使用中心 OIDC、模型或账务能力的应用可从这里继续准备客户端和权限。 <a href="/admin/onboarding">三方接入与计费准备</a></p>
      </Panel>
      <Panel title="接入遇到问题时">
        <ul>
          <li>回调失败时，在应用详情逐字比对客户端 ID、站点来源和回调路径，确认产品服务端读取了当前凭据。</li>
          <li>原产品账号方向未核验时，用已关联的原账号在全新的浏览器配置中重试。UniHub 签发会话后，在管理页刷新状态。</li>
          <li>用户看不到应用卡片时，先核对原账号是否已关联，再核对 UniHub 应用访问权限和应用当前状态。</li>
        </ul>
      </Panel>
    </div>
  );
}
