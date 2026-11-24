import { useEffect, useId, useRef, useState } from 'react';
import type { FormEvent } from 'react';
import { request } from '../api';
import { go } from './types';
import { Modal, Notice, Panel, ResourceState, Table } from '../ui';
import type { Resource } from '../useResource';
import './application-integrations.css';

const endpoint = '/admin/v1/application-integrations';
const nativeOperations = [
  'identity:link',
  'identity:session',
  'identity:source-lifecycle',
  'identity:browser',
  'identity:authenticate',
  'identity:lookup',
];
const stateLabels = {
  draft: '草稿',
  pilot: '试点中',
  active: '正式运行',
  paused: '已暂停',
  configured: '已配置',
};
type ApplicationState = keyof typeof stateLabels;
type TargetState = 'pilot' | 'active' | 'paused';

const urlFields = [
  { name: 'origin', label: '产品来源', example: 'https://product.example.com' },
  { name: 'entry_url', label: '应用启动地址', example: 'https://product.example.com/unihub/start' },
  { name: 'callback_url', label: '精确回调地址', example: 'https://product.example.com/unihub/callback' },
  { name: 'native_login_url', label: '原生登录地址', example: 'https://product.example.com/login' },
] as const;
type URLField = (typeof urlFields)[number]['name'];
type ApplicationFields = Record<URLField | 'name', string>;
type Application = ApplicationFields & {
  product_id: string;
  instance_id: string;
  client_id: string;
  source: 'managed' | 'deployment';
  state: ApplicationState;
  version: number;
  credential_configured: boolean;
  browser_sso: boolean;
  runtime_configured: boolean;
  inbound_verified: boolean;
  outbound_verified: boolean;
  hub_origin: string;
};
type Editor = { application?: Application };
type CredentialFlow = { step: 'confirm' | 'issuing' } | { step: 'reveal'; token: string };

const applicationPath = ({ product_id, instance_id }: Application) =>
  `${endpoint}/${encodeURIComponent(product_id)}/${encodeURIComponent(instance_id)}`;

function StateBadge({ state }: { state: ApplicationState }) {
  return <span className={`status ${state === 'active' ? 'good' : state === 'pilot' ? 'warn' : ''}`}>{stateLabels[state]}</span>;
}

function absoluteURL(value: string) {
  const authority = /^https?:\/\/([^/?#]+)/.exec(value)?.[1];
  if (!authority || authority.includes('@') || authority.endsWith(':') || /[\s\p{Cc}\\#]/u.test(value))
    return undefined;
  try {
    const url = new URL(value);
    if (url.protocol === 'http:' && !/^(localhost|127\.[\d.]+|\[::1\])$/.test(url.hostname)) return undefined;
    return url;
  } catch {
    return undefined;
  }
}

function IntegrationForm({ application, busy, cannotSave, stale, error, onCancel, onSave }: {
  application?: Application;
  busy: boolean;
  cannotSave: boolean;
  stale: boolean;
  error: string;
  onCancel: () => void;
  onSave: (fields: ApplicationFields, productId: string, instanceId: string) => void;
}) {
  const headingId = useId();
  const errorId = useId();
  const hintId = useId();
  const urlHintId = useId();
  const [invalid, setInvalid] = useState<{ name: string; message: string }>();
  const paused = application?.state === 'paused';

  function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (cannotSave || stale) return;
    const form = event.currentTarget;
    const data = new FormData(form);
    const read = (name: string) => String(data.get(name) ?? '').trim();
    const fields = {
      name: read('name'),
      origin: read('origin'),
      entry_url: read('entry_url'),
      callback_url: read('callback_url'),
      native_login_url: read('native_login_url'),
    };
    const productId = read('product_id');
    const instanceId = read('instance_id') || 'default';
    const identifier = /^[a-z][a-z0-9_-]{0,63}$/;

    function reject(name: string, message: string) {
      setInvalid({ name, message });
      const input = form.elements.namedItem(name);
      if (input instanceof HTMLInputElement) input.focus();
    }

    if (!application && !identifier.test(productId))
      return reject('product_id', '请输入有效的产品标识。');
    if (!application && !identifier.test(instanceId))
      return reject('instance_id', '请输入有效的实例标识。');
    // The server includes the name in a 100-byte native sign-in label.
    if (!fields.name || /\p{Cc}/u.test(fields.name) || new TextEncoder().encode(`使用 ${fields.name} 账号登录`).length > 100)
      return reject('name', '请输入应用名称。应用名称最多 80 个 UTF-8 字节。');
    if (!absoluteURL(fields.origin) || !/^https?:\/\/[^/?#]+$/.test(fields.origin))
      return reject('origin', '产品来源须为完整 origin，包含协议、域名和可选端口。');
    for (const { name, label } of urlFields.slice(1)) {
      const value = fields[name];
      const url = absoluteURL(value);
      if (!url || (value !== fields.origin && !value.startsWith(fields.origin + '/') && !value.startsWith(fields.origin + '?')))
        return reject(name, `${label}须使用与产品来源完全一致的协议、域名和端口。`);
      if (name === 'callback_url' && (url.pathname === '/' || value.includes('?')))
        return reject(name, '精确回调地址须包含回调路径，且不得包含查询参数。');
    }

    setInvalid(undefined);
    onSave(fields, productId, instanceId);
  }

  const validationProps = (name: string, hint?: string) => ({
    'aria-invalid': invalid?.name === name ? true : undefined,
    'aria-describedby': invalid?.name === name ? [hint, errorId].filter(Boolean).join(' ') : hint,
  });

  return (
    <Panel className="application-editor">
      <div className="panel-head">
        <h2 id={headingId}>{paused ? '编辑接入配置' : application ? '编辑应用草稿' : '新建原生身份接入'}</h2>
      </div>
      <form
        className="admin-form"
        aria-labelledby={headingId}
        noValidate
        onSubmit={submit}
        onInput={(event) => {
          if (event.target instanceof HTMLInputElement && event.target.name === invalid?.name) setInvalid(undefined);
        }}
      >
        {application ? (
          <p className="application-hint">产品 / 实例 <code>{application.product_id} / {application.instance_id}</code></p>
        ) : null}
        {paused ? (
          <p className="application-hint">修改产品来源或精确回调地址会使现有服务凭据失效。修改接入地址后，请更新产品端配置，恢复试点并重新核验双向登录。</p>
        ) : null}
        <fieldset disabled={busy} className="application-form-grid">
          {application ? null : <>
            <label>
              产品标识
              <input name="product_id" required maxLength={64} placeholder="例如 wiki" autoFocus {...validationProps('product_id', hintId)} />
            </label>
            <label>
              实例标识
              <input name="instance_id" maxLength={64} placeholder="留空使用 default" {...validationProps('instance_id', hintId)} />
            </label>
            <p className="application-form-note" id={hintId}>标识以小写字母开头，可使用小写字母、数字、下划线和连字符。创建后固定。</p>
          </>}
          <label className="application-form-note">
            应用名称
            <input name="name" required maxLength={100} defaultValue={application?.name} autoFocus={Boolean(application)} {...validationProps('name')} />
          </label>
          {urlFields.map(({ name, label, example }) => (
            <label key={name}>
              {label}
              <input name={name} type="url" required maxLength={1024} placeholder={example} defaultValue={application?.[name]} {...validationProps(name, urlHintId)} />
            </label>
          ))}
        </fieldset>
        <p className="application-hint" id={urlHintId}>请填写完整 HTTPS 地址。产品来源只填协议、域名和端口，三个地址均使用该来源。精确回调地址须包含完整路径，且不带查询参数。</p>
        {invalid ? <Notice kind="error"><span id={errorId}>{invalid.message}</span></Notice> : null}
        {stale ? <Notice kind="error">这条记录已更新，请取消后重新打开详情，核对最新配置。</Notice> : null}
        {error ? <Notice kind="error">{error}</Notice> : null}
        <div className="admin-actions">
          <button className="primary" type="submit" disabled={cannotSave || stale}>{paused ? '保存接入配置' : application ? '保存草稿' : '创建草稿'}</button>
          <button type="button" disabled={busy} onClick={onCancel}>取消</button>
        </div>
      </form>
    </Panel>
  );
}

function CredentialReveal({ token, onAcknowledge }: { token: string; onAcknowledge: () => void }) {
  const [feedback, setFeedback] = useState('');
  async function copy() {
    try {
      await navigator.clipboard.writeText(token);
      setFeedback('凭据已复制。');
    } catch {
      setFeedback('浏览器未能复制，请选中凭据手动复制。');
    }
  }

  return (
    <div className="admin-form">
      <label>
        新签发的服务凭据
        <input value={token} readOnly autoFocus autoComplete="off" spellCheck={false} />
      </label>
      <p className="application-hint">明文仅显示一次。请先将凭据保存到产品服务端的密钥配置，再确认已保存。确认后无法重新查看明文。</p>
      <div className="admin-actions">
        <button type="button" onClick={copy}>复制凭据</button>
        <button className="primary" type="button" onClick={onAcknowledge}>已保存凭据</button>
        <span role="status">{feedback}</span>
      </div>
    </div>
  );
}

function Verification({ label, verified }: { label: string; verified: boolean }) {
  return (
    <div>
      <dt>{label}</dt>
      <dd><span className={`status ${verified ? 'good' : 'warn'}`}>{verified ? '已验证' : '未验证'}</span></dd>
    </div>
  );
}

function NativeAdapterExample({ application }: { application: Application }) {
  const options = [
    ['UNIHUB_IDENTITY_ENABLED', 'true'],
    ['UNIHUB_IDENTITY_API_ORIGIN', application.hub_origin],
    ['UNIHUB_IDENTITY_PUBLIC_ORIGIN', application.hub_origin],
    ['UNIHUB_IDENTITY_CALLBACK_URL', application.callback_url],
    ['UNIHUB_IDENTITY_CLIENT_ID', application.client_id],
    ['UNIHUB_IDENTITY_RUNTIME_TOKEN', '<通过密钥配置注入>'],
    ['UNIHUB_IDENTITY_SESSION_GENERATION', '1'],
    ['UNIHUB_IDENTITY_PILOT_USER_IDS', '<原产品试点用户 ID>'],
  ];
  return (
    <details className="application-scope">
      <summary>AgentWiki 兼容适配器配置示例</summary>
      <pre className="application-config">{options.map(([name, value]) => name + '=' + value).join('\n')}</pre>
      <p className="application-hint">适配 AgentWiki 的应用可使用这组参数。其他应用可对应配置来源、回调校验和后端凭据。</p>
    </details>
  );
}


function ApplicationDetail({ application, disabled, pending, refreshFailed, error, onClose, onEdit, onIssue, onState }: {
  application: Application;
  disabled: boolean;
  pending: boolean;
  refreshFailed: boolean;
  error: string;
  onClose: () => void;
  onEdit: () => void;
  onIssue: () => void;
  onState: (state: TargetState) => void;
}) {
  const heading = useRef<HTMLHeadingElement>(null);
  const verificationHintId = useId();
  const credentialHintId = useId();
  const { state, source, inbound_verified, outbound_verified, credential_configured } = application;
  const managed = source === 'managed';
  const mayIssue = managed && (state === 'draft' || state === 'paused');
  const verified = inbound_verified === true && outbound_verified === true;

  useEffect(() => { heading.current?.focus(); }, []);

  return (
    <Panel className="application-detail">
      <div className="panel-head application-detail-header">
        <div>
          <h2 ref={heading} tabIndex={-1}>{application.name || application.product_id}</h2>
          <p className="application-hint">{application.product_id} / {application.instance_id} <StateBadge state={state} /></p>
        </div>
        <div className="admin-actions">
          {mayIssue ? <button disabled={disabled} onClick={onEdit}>{state === 'paused' ? '编辑接入配置' : '编辑草稿'}</button> : null}
          <button disabled={pending} onClick={onClose}>收起详情</button>
        </div>
      </div>
      {error ? <Notice kind="error">{error}</Notice> : null}
      <div className="application-detail-grid">
        <section>
          <h3>接入参数</h3>
          <dl className="application-info admin-info">
            <div><dt>UniHub 来源</dt><dd><code>{application.hub_origin || '—'}</code></dd></div>
            <div><dt>客户端 ID</dt><dd><code>{application.client_id || '—'}</code></dd></div>
            {urlFields.map(({ name, label }) => (
              <div className="application-url" key={name}><dt>{label}</dt><dd><code>{application[name] || '—'}</code></dd></div>
            ))}
          </dl>
          <h3>权限范围</h3>
          {managed ? (
            <>
              <p>身份桥接仅适用于当前产品实例。完成原生身份接入或明确关联后，UniHub 应用成员权限为 <code>product:use</code>。原产品继续管理现有角色、权限和 API Key。</p>
              <dl className="application-info admin-info">
                <div><dt>公开客户端 API 授权</dt><dd>无</dd></div>
                <div><dt>默认应用角色</dt><dd>无</dd></div>
              </dl>
              <details className="application-scope">
                <summary>服务凭据权限</summary>
                <ul>{nativeOperations.map((operation) => <li key={operation}><code>{operation}</code></li>)}</ul>
              </details>
            </>
          ) : (
            <p>该应用由部署配置管理。客户端权限、角色和服务凭据以对应部署为准。</p>
          )}
        </section>
        <section>
          <h3>服务端配置与验证</h3>
          <dl className="application-info admin-info">
            <div><dt>浏览器单点登录</dt><dd>{application.browser_sso ? '已配置' : '未配置'}</dd></div>
            <div><dt>服务端接入（Runtime）</dt><dd>{application.runtime_configured ? '已配置' : '未配置'}</dd></div>
            <div><dt>服务凭据</dt><dd>{credential_configured ? '已配置' : '未配置'}</dd></div>
          </dl>
          <dl className="application-info admin-info" aria-live="polite">
            <Verification label="原产品账号 → UniHub" verified={inbound_verified} />
            <Verification label="UniHub 我的应用 → 原产品账号" verified={outbound_verified} />
          </dl>
          <p id={verificationHintId}>
            {refreshFailed ? '刷新失败，当前显示上次读取的验证状态。' : '完成真实登录后，点击「刷新状态」读取 UniHub 服务端的验证结果。'}
          </p>
          {mayIssue ? (
            <>
              <button aria-describedby={credentialHintId} disabled={disabled} onClick={onIssue}>
                {credential_configured ? '重新签发服务凭据' : '签发服务凭据'}
              </button>
              <p id={credentialHintId}>{credential_configured ? '重新签发会替换现有凭据，请同步更新产品服务端。' : '签发后将凭据配置到产品服务端，再启用试点。'}</p>
            </>
          ) : null}
          {managed ? (
            <div className="admin-actions application-state-actions">
              {mayIssue ? (
                <button className="primary" disabled={disabled || !credential_configured} onClick={() => onState('pilot')}>
                  {state === 'paused' ? '恢复试点' : '启用试点'}
                </button>
              ) : null}
              {state === 'pilot' ? (
                <button className="primary" aria-describedby={verificationHintId} disabled={disabled || !verified} onClick={() => onState('active')}>正式发布</button>
              ) : null}
              {state === 'pilot' || state === 'active' ? (
                <button disabled={disabled} onClick={() => onState('paused')}>暂停接入</button>
              ) : null}
            </div>
          ) : null}
          {managed && state === 'pilot' && !verified ? <p>两条登录路径均验证通过后可正式发布。</p> : null}
          {managed && state === 'paused' ? <p>恢复试点后，请重新完成两条登录路径的验证。</p> : null}
        </section>
      </div>
      {managed ? (
        <section className="application-steps">
          <h3>产品侧接入步骤</h3>
          <ol>
            <li>在产品服务端填入 UniHub 来源、产品和实例标识、客户端 ID 与服务凭据。核对启动、回调和原生登录地址，按上方参数配置来源和回调允许列表。</li>
            <li>在产品侧仅向明确选定的试点用户开放身份桥接，并保留原生登录入口。未匹配到 UniHub 账号的用户可使用原产品身份接入。匹配到已有账号时，由用户验证已有账号并明确确认关联。同邮箱仅用于提示匹配。</li>
            <li>启用试点后，在无 UniHub 登录态的独立浏览器配置中，通过<a href={application.native_login_url} target="_blank" rel="noreferrer">原账号登录地址</a>进入 UniHub。如先关联了已有 UniHub 账号，请在另一份无 UniHub 登录态的浏览器配置中，用已关联的原账号重新走一遍此入口，确认 UniHub 签发了会话。</li>
            <li>保留该 UniHub 会话。用浏览器站点数据工具只清除原产品站点的本地登录凭据（如 Cookie 和站点存储）。随后从「我的应用」打开产品，核对原账号、角色、权限和 Key。产品的「退出登录」会触发服务端注销，请单独验证该流程。</li>
            <li>刷新并确认双向验证通过后正式发布。根据实际需要在产品侧逐步扩大试点允许名单。</li>
          </ol>
          <NativeAdapterExample application={application} />
        </section>
      ) : null}
    </Panel>
  );
}

const tableHeaders = ['应用', '配置来源', '状态', '操作'];

export function ApplicationIntegrations({ csrf }: { csrf?: string }) {
  const [resource, setResource] = useState<Resource<Application[]>>({ loading: true });
  const [revision, setRevision] = useState(0);
  const [selectedPath, setSelectedPath] = useState('');
  const [editor, setEditor] = useState<Editor>();
  const [pending, setPending] = useState(false);
  const [error, setError] = useState('');
  const [credentialFlow, setCredentialFlow] = useState<CredentialFlow>();
  const pageHeading = useRef<HTMLHeadingElement>(null);
  const detailTrigger = useRef<HTMLButtonElement>(null);
  const selected = resource.data?.find((application) => applicationPath(application) === selectedPath);
  const original = editor?.application;
  const outdated = Boolean(original && (selected?.version !== original.version || selected?.state !== original.state));
  const credentialLocked = credentialFlow?.step === 'issuing' || credentialFlow?.step === 'reveal';
  const busy = pending || resource.loading || credentialLocked;
  const unavailable = busy || Boolean(resource.error);

  useEffect(() => {
    const controller = new AbortController();
    request<Application[]>(endpoint, { signal: controller.signal }).then((data) => {
      if (!controller.signal.aborted) setResource({ loading: false, data });
    }).catch((cause: Error) => {
      if (!controller.signal.aborted)
        setResource((current) => ({ ...current, loading: false, error: cause.message }));
    });
    return () => controller.abort();
  }, [revision]);

  useEffect(() => {
    if (!credentialLocked) return;
    const currentURL = window.location.href;
    const navigation = (window as Window & { navigation?: EventTarget }).navigation;
    function confirmBeforeLeaving(event: BeforeUnloadEvent) {
      event.preventDefault();
      event.returnValue = '';
    }
    function preventRouteChange(event: Event) {
      const { destination } = event as Event & { destination: { sameDocument: boolean; url: string } };
      if (destination.sameDocument && destination.url !== currentURL) event.preventDefault();
    }
    window.addEventListener('beforeunload', confirmBeforeLeaving);
    navigation?.addEventListener('navigate', preventRouteChange);
    return () => {
      window.removeEventListener('beforeunload', confirmBeforeLeaving);
      navigation?.removeEventListener('navigate', preventRouteChange);
    };
  }, [credentialLocked]);

  function refresh() {
    setError('');
    setResource((current) => ({ ...current, loading: true, error: undefined }));
    setRevision((previous) => previous + 1);
  }

  function select(path: string) {
    setSelectedPath(path);
    setError('');
  }

  function openEditor(application?: Application) {
    setEditor({ application });
    setError('');
  }

  function commit(application: Application) {
    const path = applicationPath(application);
    setResource((current) => {
      const items = current.data ?? [];
      const data = items.some((item) => applicationPath(item) === path)
        ? items.map((item) => applicationPath(item) === path ? application : item)
        : [...items, application];
      return { loading: false, data };
    });
  }

  async function mutate(operation: () => Promise<void>) {
    if (unavailable) return;
    setPending(true);
    setError('');
    try {
      await operation();
    } catch (cause) {
      setError((cause as Error).message);
    } finally {
      setPending(false);
    }
  }

  function saveDraft(fields: ApplicationFields, productId: string, instanceId: string) {
    void mutate(async () => {
      const application = original
        ? await request<Application>(applicationPath(original), { method: 'PUT', csrf, body: { ...fields, version: original.version } })
        : await request<Application>(endpoint, { method: 'POST', csrf, body: { product_id: productId, instance_id: instanceId, ...fields } });
      commit(application);
      if (!original) detailTrigger.current = null;
      select(applicationPath(application));
      setEditor(undefined);
    });
  }

  function issueCredential(application: Application) {
    void mutate(async () => {
      setCredentialFlow({ step: 'issuing' });
      try {
        const issued = await request<{ application: Application; token: string }>(`${applicationPath(application)}/credential`, {
          method: 'POST', csrf, body: { version: application.version },
        });
        commit(issued.application);
        setCredentialFlow({ step: 'reveal', token: issued.token });
      } catch (cause) {
        setCredentialFlow(undefined);
        throw cause;
      }
    });
  }

  function changeState(application: Application, state: TargetState) {
    void mutate(async () => {
      const updated = await request<Application>(`${applicationPath(application)}/state`, {
        method: 'POST', csrf, body: { version: application.version, state },
      });
      commit(updated);
    });
  }

  return (
    <div className="application-integrations">
      <div className="admin-page-heading">
        <div>
          <h1 ref={pageHeading} tabIndex={-1}>应用接入与管理</h1>
          <p>配置原生身份桥接，查看部署状态并验证双向登录。</p>
          <a className="button-link" href="/admin/applications/guide" onClick={(event) => { event.preventDefault(); if (!credentialLocked) go('/admin/applications/guide'); }}>接入手册</a>
        </div>
        <div className="admin-actions">
          <button type="button" disabled={busy} onClick={refresh}>刷新状态</button>
          <button className="primary" type="button" disabled={unavailable || Boolean(editor)} onClick={() => openEditor()}>新建接入</button>
        </div>
      </div>
      {editor ? (
        <IntegrationForm
          application={original}
          busy={pending}
          cannotSave={unavailable}
          stale={outdated}
          error={error}
          onSave={saveDraft}
          onCancel={() => {
            setEditor(undefined);
            setError('');
            if (!selected) pageHeading.current?.focus();
          }}
        />
      ) : selected ? (
        <ApplicationDetail
          key={selectedPath}
          application={selected}
          disabled={unavailable}
          pending={pending || credentialLocked}
          refreshFailed={Boolean(resource.error)}
          error={error}
          onClose={() => {
            select('');
            const trigger = detailTrigger.current;
            (trigger?.isConnected ? trigger : pageHeading.current)?.focus();
          }}
          onEdit={() => openEditor(selected)}
          onIssue={() => selected.credential_configured ? setCredentialFlow({ step: 'confirm' }) : issueCredential(selected)}
          onState={(state) => changeState(selected, state)}
        />
      ) : null}
      {selected && credentialFlow ? (
        <Modal
          title={credentialFlow.step === 'confirm' ? '重新签发服务凭据' : '服务凭据'}
          busy={credentialLocked}
          onClose={() => setCredentialFlow(undefined)}
        >
          {credentialFlow.step === 'confirm' ? (
            <>
              <div className="modal-body">
                <p>确认重新签发后，旧凭据立即失效。依赖旧凭据的账号状态同步和会话校验会中断，直至产品服务端切换到新凭据。请先确认产品团队已准备好更新密钥。</p>
              </div>
              <div className="modal-actions">
                <button type="button" onClick={() => setCredentialFlow(undefined)}>取消</button>
                <button className="primary" type="button" onClick={() => issueCredential(selected)}>确认重新签发</button>
              </div>
            </>
          ) : credentialFlow.step === 'reveal' ? (
            <div className="modal-body"><CredentialReveal token={credentialFlow.token} onAcknowledge={() => setCredentialFlow(undefined)} /></div>
          ) : (
            <div className="modal-body" role="status">正在签发服务凭据…</div>
          )}
        </Modal>
      ) : null}
      <ResourceState resource={resource} empty="暂无应用接入记录" emptyHeaders={tableHeaders}>
        {(applications) => (
          <Table headers={tableHeaders}>
            {applications.map((application) => {
              const path = applicationPath(application);
              const name = application.name || application.product_id;
              return (
                <tr key={path}>
                  <td><strong>{name}</strong><small className="application-instance">{application.product_id} / {application.instance_id}</small></td>
                  <td>{application.source === 'deployment' ? '部署管理' : '平台管理'}</td>
                  <td><StateBadge state={application.state} /></td>
                  <td>
                    <button
                      className="text-button"
                      aria-label={`查看 ${name}（${application.product_id} / ${application.instance_id}）详情`}
                      aria-expanded={selectedPath === path && !editor}
                      disabled={pending || credentialLocked || Boolean(editor)}
                      onClick={(event) => {
                        detailTrigger.current = event.currentTarget;
                        select(selectedPath === path ? '' : path);
                      }}
                    >查看详情</button>
                  </td>
                </tr>
              );
            })}
          </Table>
        )}
      </ResourceState>
    </div>
  );
}
