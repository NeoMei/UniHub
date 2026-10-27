import { useState } from "react";
import { request } from "./api";
import { useResource } from "./useResource";
import { Notice, Panel, ResourceState } from "./ui";

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
  const users = useResource<ManagedUser[]>(
    `/v1/user-management/users?q=${encodeURIComponent(search)}&limit=50`, revision,
  );
  const products = useResource<ProductManifest[]>("/v1/user-management/products");
  const access = useResource<ProductAssignment[]>(
    selected ? `/v1/user-management/users/${encodeURIComponent(selected.id)}/access` : undefined,
  );

  async function changeStatus() {
    if (!selected) return;
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
    <section>
      <h1>用户管理</h1>
      <p>管理统一身份的全局状态和各产品角色。角色变更会在产品下次验证身份时生效。</p>
      {message && <Notice>{message}</Notice>}
      {error && <Notice kind="error">{error}</Notice>}
      <form className="product-user-search" onSubmit={(event) => {
        event.preventDefault();
        setSearch(query);
      }}>
        <label>搜索用户
          <input value={query} maxLength={100} onChange={(event) => setQuery(event.target.value)} placeholder="名称、邮箱或用户 ID" />
        </label>
        <button type="submit">搜索</button>
        <button type="button" disabled={busy} onClick={() => {setSelected(undefined); setError(""); setMessage(""); setRevision((value) => value + 1);}}>刷新用户</button>
      </form>
      <div className="product-user-layout">
        <Panel title="统一用户">
          <ResourceState resource={users} empty="没有匹配的用户">
            {(items) => (
              <ul className="product-user-list">
                {items.map((user) => (
                  <li key={user.id}>
                    <div><strong>{user.name || user.id}</strong><p>{user.email || user.id} · {user.active ? "启用" : "停用"}</p></div>
                    <button disabled={busy} aria-label={`管理 ${user.name || user.id}`} onClick={() => {
                      setSelected(user);
                      setMessage("");
                      setError("");
                    }}>管理</button>
                  </li>
                ))}
              </ul>
            )}
          </ResourceState>
        </Panel>
        <div>
          {selected ? (
            <>
              <Panel title={selected.name || selected.id}>
                <dl>
                  <dt>统一用户 ID</dt><dd>{selected.id}</dd>
                  <dt>用户状态</dt><dd>{selected.active ? "启用" : "停用"} · 版本 {selected.version}</dd>
                </dl>
                <button disabled={busy} onClick={changeStatus}>{selected.active ? "停用用户" : "启用用户"}</button>
              </Panel>
              <ResourceState resource={products} empty="尚未配置产品">
                {(manifests) => (
                  <ResourceState resource={access} empty="暂无产品访问记录">
                    {(assignments) => manifests.map((manifest) => {
                      const assignment = assignments.find((item) =>
                        item.product_id === manifest.product_id && item.instance_id === manifest.instance_id,
                      );
                      return assignment && (
                        <ProductRoleForm
                          key={`${selected.id}/${manifest.product_id}/${manifest.instance_id}/${manifest.version}`}
                          user={selected.id}
                          manifest={manifest}
                          assignment={assignment}
                          csrf={csrf}
                          onSaved={() => {
                            setMessage("产品权限已保存。");
                          }}
                        />
                      );
                    })}
                  </ResourceState>
                )}
              </ResourceState>
            </>
          ) : <Panel title="产品访问权限"><p>选择一个用户查看并调整产品角色。</p></Panel>}
        </div>
      </div>
    </section>
  );
}

function ProductRoleForm({ user, manifest, assignment: initialAssignment, csrf, onSaved }: {
  user: string;
  manifest: ProductManifest;
  assignment: ProductAssignment;
  csrf?: string;
  onSaved: () => void;
}) {
  // Each product owns its saved assignment revision and unsaved draft. Saving
  // one product must not refetch/unmount its sibling forms.
  const [assignment, setAssignment] = useState(initialAssignment);
  const [roles, setRoles] = useState(initialAssignment.roles);
  const [active, setActive] = useState(initialAssignment.active);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
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
    <Panel title={`${manifest.name} · ${manifest.instance_id}`}>
      <p>{assignment.explicit ? "显式分配" : "使用默认角色"} · 分配版本 {assignment.assignment_version} · 产品版本 {assignment.manifest_version}</p>
      <fieldset disabled={busy} className="product-role-fields">
        <legend>产品角色</legend>
        {manifest.roles.map((role) => (
          <label key={role.id}>
            <input type="checkbox" aria-label={role.name} checked={roles.includes(role.id)} onChange={(event) =>
              setRoles(event.target.checked ? [...roles, role.id] : roles.filter((id) => id !== role.id))
            } />
            {role.name}{role.privileged ? "（管理权限）" : ""}
            <small>{role.permissions.join("、")}</small>
          </label>
        ))}
        <label><input type="checkbox" checked={active} onChange={(event) => setActive(event.target.checked)} />允许访问此产品</label>
      </fieldset>
      {error && <Notice kind="error">{error}</Notice>}
      <button disabled={busy} onClick={save}>保存 {manifest.name} 权限</button>
    </Panel>
  );
}
