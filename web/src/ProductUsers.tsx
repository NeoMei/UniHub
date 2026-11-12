import { useCallback, useEffect, useState } from "react";
import {
  ChevronRight,
  RefreshCw,
  Search,
  ShieldCheck,
  UserRoundCheck,
  UserRoundX,
  UsersRound,
} from "lucide-react";
import { request } from "./api";
import { useResource } from "./useResource";
import { Modal, Notice, Panel, ResourceState } from "./ui";

export interface ProductAccess {
  issuer: string;
  subject: string;
  product_id: string;
  instance_id: string;
  user: { id: string; name: string; email: string };
  roles: string[];
  permissions: string[];
  active: boolean;
  version: number;
  expires_at: number;
}
export interface ProductManifest {
  product_id: string;
  instance_id: string;
  name: string;
  entry_url: string;
  version: number;
  default_roles: string[];
  roles: { id: string; name: string; permissions: string[]; privileged?: boolean }[];
}
export interface ProductAssignment {
  product_id: string;
  instance_id: string;
  roles: string[];
  active: boolean;
  assignment_version: number;
  manifest_version: number;
  explicit: boolean;
}
interface ManagedUser {
  id: string;
  name: string;
  email: string;
  active: boolean;
  version: number;
}

let unsavedPermissionForms = 0;

export function hasUnsavedPermissionChanges() {
  return unsavedPermissionForms > 0;
}

export function confirmDiscardUnsavedPermissions(next: string) {
  if (!hasUnsavedPermissionChanges()) return true;
  try {
    return window.confirm(`当前还有未保存的产品权限。离开后这些更改会丢失，仍要前往「${next}」吗？`);
  } catch {
    return false;
  }
}

export function ProductApplications() {
  const applications = useResource<
    (ProductAccess & { name: string; entry_url: string })[]
  >("/v1/me/applications");
  return (
    <section>
      <h1>我的应用</h1>
      <p>使用统一身份进入产品，权限由归一管理员管理。</p>
      <ResourceState resource={applications} empty="暂无可用应用">
        {(items) => (
          <div className="product-user-grid">
            {items.map((app) => (
              <Panel key={`${app.product_id}/${app.instance_id}`} title={app.name}>
                <p>{app.instance_id}</p>
                <p>{app.roles.join("、") || "无产品角色"}</p>
                <a className="button" href={app.entry_url} target="_blank" rel="noreferrer">
                  打开 {app.name}
                </a>
              </Panel>
            ))}
          </div>
        )}
      </ResourceState>
    </section>
  );
}

export function ProductUserManagement({ csrf }: { csrf?: string }) {
  const [query, setQuery] = useState("");
  const [search, setSearch] = useState("");
  const [revision, setRevision] = useState(0);
  const [selected, setSelected] = useState<ManagedUser>();
  const [message, setMessage] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [statusConfirm, setStatusConfirm] = useState(false);
  const [pendingUser, setPendingUser] = useState<ManagedUser>();
  const [pendingTransition, setPendingTransition] = useState<"access" | "status">("access");
  const [dirtyForms, setDirtyForms] = useState<Map<string, boolean>>(() => new Map());
  const [directoryOpen, setDirectoryOpen] = useState(true);
  const users = useResource<ManagedUser[]>(
    `/v1/user-management/users?q=${encodeURIComponent(search)}&limit=50`, revision,
  );
  const products = useResource<ProductManifest[]>("/v1/user-management/products", revision);
  const access = useResource<ProductAssignment[]>(
    selected ? `/v1/user-management/users/${encodeURIComponent(selected.id)}/access` : undefined,
    revision,
  );

  useEffect(() => {
    unsavedPermissionForms = [...dirtyForms.values()].filter(Boolean).length;
    return () => {
      unsavedPermissionForms = 0;
    };
  }, [dirtyForms]);

  useEffect(() => {
    if (![...dirtyForms.values()].some(Boolean)) return;
    const warn = (event: BeforeUnloadEvent) => {
      event.preventDefault();
      event.returnValue = "";
    };
    window.addEventListener("beforeunload", warn);
    return () => window.removeEventListener("beforeunload", warn);
  }, [dirtyForms]);

  function selectUser(user: ManagedUser) {
    if (selected?.id === user.id) {
      setDirectoryOpen(false);
      return;
    }
    if ([...dirtyForms.values()].some(Boolean)) {
      setPendingTransition("access");
      setPendingUser(user);
      return;
    }
    setSelected(user);
    setDirectoryOpen(false);
    setMessage("");
    setError("");
  }

  const setFormDirty = useCallback((key: string, dirty: boolean) => {
    setDirtyForms((current) => {
      const next = new Map(current);
      next.set(key, dirty);
      return next;
    });
  }, []);

  async function changeStatus() {
    if (!selected) return;
    setStatusConfirm(false);
    setBusy(true);
    setError("");
    setMessage("");
    try {
      const next = await request<ManagedUser>(
        `/v1/user-management/users/${encodeURIComponent(selected.id)}/status`,
        { method: "PUT", csrf, body: { active: !selected.active, version: selected.version } },
      );
      setSelected(next);
      setRevision((value) => value + 1);
      setMessage("用户状态已保存。");
    } catch (cause) {
      setError((cause as Error).message);
    } finally {
      setBusy(false);
    }
  }

  return (
    <section className="user-admin-page">
      <header className="user-admin-heading">
        <div>
          <h1>用户与产品权限</h1>
          <p>先确认账户状态，再为每个产品分配进入权限与角色。变更会在产品下次验证身份时生效。</p>
        </div>
        <button
          type="button"
          className="secondary user-admin-refresh"
          disabled={busy || users.loading}
          onClick={() => {
            if (!confirmDiscardUnsavedPermissions("刷新后的权限数据")) return;
            setError("");
            setMessage("");
            setRevision((value) => value + 1);
          }}
        >
          <RefreshCw size={15} />刷新用户与权限
        </button>
      </header>
      {message && <Notice>{message}</Notice>}
      {error && <Notice kind="error">{error}</Notice>}
      <div className="user-admin-shell">
        <aside className={`user-directory${directoryOpen ? "" : " is-mobile-collapsed"}`} aria-label="统一用户目录">
          <div className="user-directory-head">
            <div>
              <span className="user-directory-label">统一用户目录</span>
              <strong>
                {search
                  ? `“${search}” 的搜索结果`
                  : (users.data?.length || 0) >= 50
                    ? "最近 50 位用户"
                    : "全部用户"}
                {!users.loading && !users.error ? ` · ${users.data?.length || 0}` : ""}
              </strong>
              {query.trim() !== search && (
                <span className="user-search-hint">
                  当前显示已应用的关键词；按 Enter 查找 “{query.trim()}”
                </span>
              )}
            </div>
            {search && (
              <button
                type="button"
                className="text-button"
                onClick={() => {
                  setQuery("");
                  setSearch("");
                }}
              >
                清除筛选
              </button>
            )}
          </div>
          <form
            className="user-directory-search"
            onSubmit={(event) => {
              event.preventDefault();
              setSearch(query.trim());
            }}
          >
            <label className="sr-only" htmlFor="user-admin-search">搜索统一用户</label>
            <Search size={16} aria-hidden="true" />
            <input
              id="user-admin-search"
              aria-label="搜索用户"
              value={query}
              maxLength={100}
              onChange={(event) => setQuery(event.target.value)}
              onKeyDown={(event) => {
                if (event.key === "Escape") {
                  setQuery("");
                  setSearch("");
                }
              }}
              placeholder="姓名、邮箱或用户 ID"
            />
          </form>
          <div className="user-directory-results">
            <ResourceState resource={users} empty="没有匹配的用户">
              {(items) => (
                <ul className="user-directory-list">
                  {items.map((user) => (
                    <li key={user.id}>
                      <button
                        type="button"
                        className={`user-directory-row${selected?.id === user.id ? " is-selected" : ""}`}
                        disabled={busy}
                        aria-label={`管理 ${user.name || user.id}`}
                        aria-pressed={selected?.id === user.id}
                        onClick={() => selectUser(user)}
                      >
                        <UserAvatar name={user.name || user.email || user.id} id={user.id} />
                        <span className="user-directory-copy">
                          <strong>{user.name || "未命名用户"}</strong>
                          <span>{user.email || "未绑定邮箱"}</span>
                          <span className={`user-account-state ${user.active ? "is-active" : "is-disabled"}`}>
                            <i aria-hidden="true" />{user.active ? "账户可用" : "账户已停用"}
                          </span>
                        </span>
                        <ChevronRight className="user-directory-chevron" size={16} aria-hidden="true" />
                      </button>
                    </li>
                  ))}
                </ul>
              )}
            </ResourceState>
          </div>
          {!users.loading && !users.error && (users.data?.length || 0) >= 50 && (
            <p className="user-directory-limit">当前显示前 50 位用户；请输入关键词缩小范围。</p>
          )}
        </aside>
        <div className="user-workbench">
          <button
            type="button"
            className="secondary user-mobile-directory-toggle"
            onClick={() => setDirectoryOpen((open) => !open)}
          >
            {directoryOpen ? "收起用户列表" : "更换用户"}
          </button>
          {selected ? (
            <>
              <section className="user-profile-card" aria-labelledby="selected-user-heading">
                <div className="user-profile-main">
                  <UserAvatar name={selected.name || selected.email || selected.id} id={selected.id} large />
                  <div className="user-profile-copy">
                    <div className="user-profile-titleline">
                      <h2 id="selected-user-heading">{selected.name || "未命名用户"}</h2>
                      <span className={`user-state-pill ${selected.active ? "is-active" : "is-disabled"}`}>
                        {selected.active ? "账户可用" : "账户已停用"}
                      </span>
                    </div>
                    <p>{selected.email || "未绑定邮箱"}</p>
                  </div>
                  <button
                    type="button"
                    className={selected.active ? "secondary user-status-action is-danger" : "secondary user-status-action"}
                    disabled={busy}
                    onClick={() => {
                      if ([...dirtyForms.values()].some(Boolean)) {
                        setPendingTransition("status");
                        setPendingUser(selected);
                      } else setStatusConfirm(true);
                    }}
                  >
                    {selected.active ? <UserRoundX size={15} /> : <UserRoundCheck size={15} />}
                    {selected.active ? "停用账户" : "启用账户"}
                  </button>
                </div>
                <div className="user-profile-summary">
                  <div>
                    <span>身份来源</span>
                    <strong>{selected.email ? "已验证邮箱" : "后台创建身份"}</strong>
                  </div>
                  <div>
                    <span>账户状态</span>
                    <strong>{selected.active ? "可登录，可授权产品" : "登录与产品授权均已阻断"}</strong>
                  </div>
                  <div>
                    <span>变更生效</span>
                    <strong>产品下次验证身份时</strong>
                  </div>
                </div>
                <details className="user-identity-meta">
                  <summary>身份标识与记录版本</summary>
                  <dl>
                    <div><dt>统一用户 ID</dt><dd className="mono">{selected.id}</dd></div>
                    <div><dt>用户版本</dt><dd>{selected.version}</dd></div>
                  </dl>
                </details>
              </section>
              <div className="user-permission-heading">
                <div>
                  <h2>进入范围与角色</h2>
                </div>
                <p>权限按产品独立保存；一个产品的变更不会影响其他产品。</p>
              </div>
              <ResourceState resource={products} empty="尚未配置可管理产品">
                {(manifests) => (
                  <ResourceState resource={access} empty="该用户暂无产品访问记录">
                    {(assignments) => {
                      const assigned = manifests.flatMap((manifest) => {
                        const assignment = assignments.find((item) =>
                          item.product_id === manifest.product_id && item.instance_id === manifest.instance_id,
                        );
                        return assignment ? [{
                          key: `${selected.id}/${manifest.product_id}/${manifest.instance_id}/${manifest.version}`,
                          manifest,
                          assignment,
                        }] : [];
                      });
                      return assigned.length ? (
                        <div className="user-product-list">
                          {assigned.map(({ key, manifest, assignment }) => (
                            <ProductRoleForm
                              key={key}
                              formKey={key}
                              user={selected.id}
                              manifest={manifest}
                              assignment={assignment}
                              csrf={csrf}
                              onSaved={() => {
                                setMessage("产品权限已保存。");
                              }}
                              onDirtyChange={setFormDirty}
                            />
                          ))}
                        </div>
                      ) : <div className="user-workbench-empty"><strong>没有可编辑的产品权限</strong><p>产品目录中的实例尚未为该用户建立访问记录。</p></div>;
                    }}
                  </ResourceState>
                )}
              </ResourceState>
            </>
          ) : (
            <div className="user-workbench-empty user-workbench-placeholder">
              <UsersRound size={28} />
              <strong>从左侧选择一位用户</strong>
              <p>查看账户状态、失效风险，并为各产品分配角色。</p>
            </div>
          )}
        </div>
      </div>
      {pendingUser && (
        <Modal title="未保存的产品权限" onClose={() => setPendingUser(undefined)}>
          <div className="user-modal-body">
            <p>切换用户会丢弃当前尚未保存的角色和产品访问更改。可以先返回并保存，或明确放弃这些更改。</p>
            <div className="user-status-confirm-target">
              <UserAvatar name={pendingUser.name || pendingUser.email || pendingUser.id} id={pendingUser.id} />
              <div>
                <strong>{pendingUser.name || "未命名用户"}</strong>
                <span>{pendingUser.email || "未绑定邮箱"}</span>
                <span className="mono">{pendingUser.id}</span>
              </div>
            </div>
            <div className="user-status-confirm-actions">
              <button type="button" className="secondary" onClick={() => setPendingUser(undefined)}>返回当前用户</button>
              <button type="button" className="danger-button" onClick={() => {
                const next = pendingUser;
                setPendingUser(undefined);
                setDirtyForms(new Map());
                if (pendingTransition === "status") {
                  setStatusConfirm(true);
                } else {
                  setSelected(next);
                  setDirectoryOpen(false);
                  setMessage("");
                  setError("");
                }
              }}>放弃更改并继续</button>
            </div>
          </div>
        </Modal>
      )}
      {statusConfirm && selected && (
        <Modal
          title={selected.active ? "停用这个账户？" : "重新启用这个账户？"}
          onClose={() => setStatusConfirm(false)}
          busy={busy}
        >
          <div className="user-modal-body user-status-confirm">
            <p>
              {selected.active
                ? "停用后，该用户将无法登录归一，也会失去关联产品的后续身份验证。历史记录与产品权限会保留。"
                : "启用后，该用户可以重新登录归一，并继续使用已分配的产品权限。"}
            </p>
            <div className="user-status-confirm-target">
              <UserAvatar name={selected.name || selected.email || selected.id} id={selected.id} />
              <div>
                <strong>{selected.name || "未命名用户"}</strong>
                <span>{selected.email || "未绑定邮箱"}</span>
                <span className="mono">{selected.id}</span>
              </div>
            </div>
            <div className="user-status-confirm-actions">
              <button type="button" className="secondary" disabled={busy} onClick={() => setStatusConfirm(false)}>取消</button>
              <button type="button" className={selected.active ? "danger-button" : ""} disabled={busy} onClick={changeStatus}>
                {selected.active ? "继续停用" : "继续启用"}
              </button>
            </div>
          </div>
        </Modal>
      )}
    </section>
  );
}

const avatarTones = ["sage", "sky", "sand", "rose"];
function avatarTone(id: string) {
  let value = 0;
  for (const char of id) value = (value * 31 + char.charCodeAt(0)) >>> 0;
  return avatarTones[value % avatarTones.length];
}
function UserAvatar({ name, id, large = false }: { name?: string; id?: string; large?: boolean }) {
  const initial = (name || "").trim().slice(0, 1).toUpperCase() || "U";
  return (
    <span className={`user-avatar tone-${avatarTone(id || "")}${large ? " is-large" : ""}`} aria-hidden="true">
      {initial}
    </span>
  );
}

function sameRoles(left: string[], right: string[]) {
  if (left.length !== right.length) return false;
  const values = new Set(right);
  return left.every((role) => values.has(role));
}

function permissionPreview(permissions: string[]) {
  if (!permissions.length) return "无额外权限";
  if (permissions.length <= 2) return permissions.join(" · ");
  return `${permissions.slice(0, 2).join(" · ")} · +${permissions.length - 2}`;
}

function ProductRoleForm({ formKey, user, manifest, assignment: initialAssignment, csrf, onSaved, onDirtyChange }: {
  formKey: string;
  user: string;
  manifest: ProductManifest;
  assignment: ProductAssignment;
  csrf?: string;
  onSaved: () => void;
  onDirtyChange: (key: string, dirty: boolean) => void;
}) {
  // Each product owns its saved assignment revision and unsaved draft. Saving
  // one product must not refetch/unmount its sibling forms.
  const [assignment, setAssignment] = useState(initialAssignment);
  const [roles, setRoles] = useState(initialAssignment.roles);
  const [active, setActive] = useState(initialAssignment.active);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const dirty = !sameRoles(roles, assignment.roles) || active !== assignment.active;
  useEffect(() => {
    onDirtyChange(formKey, dirty);
  }, [dirty, formKey, onDirtyChange]);
  useEffect(() => () => onDirtyChange(formKey, false), [formKey, onDirtyChange]);
  async function save() {
    setBusy(true);
    setError("");
    try {
      const saved = await request<ProductAssignment>(
        `/v1/user-management/users/${encodeURIComponent(user)}/access/${encodeURIComponent(manifest.product_id)}?instance=${encodeURIComponent(manifest.instance_id)}`,
        { method: "PUT", csrf, body: { roles, active, version: assignment.assignment_version } },
      );
      setAssignment(saved);
      setRoles(saved.roles);
      setActive(saved.active);
      onSaved();
    } catch (cause) {
      setError((cause as Error).message);
    } finally {
      setBusy(false);
    }
  }
  return (
    <article className={`user-product-card${active ? "" : " is-access-off"}`}>
      <header className="user-product-head">
        <div className="user-product-title">
          <span className="user-product-instance">{manifest.instance_id}</span>
          <h3>{manifest.name}</h3>
          <span className={`user-assignment-kind ${assignment.explicit ? "is-explicit" : ""}`}>
            {assignment.explicit ? "显式分配" : "默认角色"}
          </span>
        </div>
        <label className="user-access-toggle">
          <input
            type="checkbox"
            aria-label="允许访问此产品"
            checked={active}
            disabled={busy}
            onChange={(event) => setActive(event.target.checked)}
          />
          <span className="user-access-track" aria-hidden="true"><i /></span>
          <span>{active ? "允许进入" : "禁止进入"}</span>
        </label>
      </header>
      <fieldset disabled={busy} className="user-role-fieldset">
        <legend>产品角色</legend>
        <div className="user-role-grid">
          {manifest.roles.map((role) => {
            const checked = roles.includes(role.id);
            const preview = role.permissions.length > 4
              ? role.permissions.slice(0, 4).join(" · ")
              : permissionPreview(role.permissions);
            return (
              <article key={role.id} className={`user-role-card${checked ? " is-selected" : ""}`}>
                <label className="user-role-choice">
                  <input
                    type="checkbox"
                    aria-label={role.name}
                    checked={checked}
                    onChange={(event) =>
                      setRoles(event.target.checked ? [...roles, role.id] : roles.filter((id) => id !== role.id))
                    }
                  />
                  <span className="user-role-check" aria-hidden="true" />
                  <span className="user-role-copy">
                    <span className="user-role-name">
                      <strong>{role.name}</strong>
                      {role.privileged && <span className="user-role-privilege"><ShieldCheck size={12} />管理</span>}
                    </span>
                    <small>{preview}</small>
                  </span>
                </label>
                <details className="user-role-permissions">
                  <summary>{role.permissions.length} 项权限</summary>
                  <p>{role.permissions.join(" · ") || "无额外权限"}</p>
                </details>
              </article>
            );
          })}
        </div>
      </fieldset>
      {error && <Notice kind="error">{error}</Notice>}
      <footer className="user-product-footer">
        <span className="user-product-version">
          {dirty ? "有未保存更改" : "权限已同步"} · 分配版本 {assignment.assignment_version} · 产品版本 {assignment.manifest_version}
        </span>
        <button disabled={busy} onClick={save}>保存 {manifest.name} 权限</button>
      </footer>
    </article>
  );
}
