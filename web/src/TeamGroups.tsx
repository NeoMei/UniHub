import { useId, useState } from "react";
import { accountPath } from "./api";
import type { ActionSpec } from "./App";
import type { Member, TeamGroup, TeamGroupDirectory, TeamGroupMembership } from "./types";
import { Notice, Panel, ResourceState } from "./ui";
import type { Resource } from "./useResource";

type Props = {
  accountId: string;
  directory: Resource<TeamGroupDirectory>;
  groupMemberships: Resource<TeamGroupMembership[]>;
  groupsByMember: ReadonlyMap<string, string[]>;
  members: Resource<Member[]>;
  canManage: boolean;
  act: (spec: ActionSpec) => void;
};

function memberName(member: Member) {
  return member.name || member.email || member.user_id;
}

function GroupNameForm({ label, value, currentName, action, onChange, onSave }: {
  label: string;
  value: string;
  currentName?: string;
  action: string;
  onChange: (value: string) => void;
  onSave: (name: string) => void;
}) {
  const name = value.trim();
  const tooLong = [...name].length > 80;
  const valid = Boolean(name) && !tooLong && name !== currentName;
  const limitId = useId();
  return (
    <form className="team-group-form" onSubmit={event => {
      event.preventDefault();
      if (valid) onSave(name);
    }}>
      <div className="team-group-form-field">
        <label>
          {label}
          <input
            value={value}
            onChange={event => onChange(event.target.value)}
            aria-invalid={tooLong || undefined}
            aria-describedby={tooLong ? limitId : undefined}
          />
        </label>
        {tooLong && <small id={limitId}>小组名称最多 80 个字。</small>}
      </div>
      <button type="submit" disabled={!valid}>{action}</button>
    </form>
  );
}

export function TeamGroups({ accountId, directory, groupMemberships, groupsByMember, members, canManage, act }: Props) {
  const [newName, setNewName] = useState("");
  const [selectedId, setSelectedId] = useState("");
  const groupPath = accountPath(accountId, "groups");
  const ownIds = new Set(directory.data?.group_ids);
  const teamAction = (spec: ActionSpec) => act({...spec, accountLabel: "团队账户"});

  return (
    <Panel title="团队小组" className="team-groups">
      <p className="muted">成员可加入多个小组。角色、访问权限和费用按团队账户管理。</p>
      {canManage && (
        <GroupNameForm
          label="小组名称"
          value={newName}
          action="创建小组"
          onChange={setNewName}
          onSave={name => teamAction({
            title: "创建小组",
            path: groupPath,
            body: {name},
            content: <p>在当前团队创建「{name}」。</p>,
            onResult: data => {
              setNewName("");
              if (data && typeof data === "object" && "id" in data && typeof data.id === "string") setSelectedId(data.id);
            },
          })}
        />
      )}
      <ResourceState resource={{...directory, data: directory.data?.groups}} empty="暂无小组">
        {groups => {
          const selected = groups.find(group => group.id === selectedId)
            || groups.find(group => ownIds.has(group.id))
            || groups[0];
          const ownNames = groups.filter(group => ownIds.has(group.id)).map(group => group.name);
          return (
            <>
              <p className="team-group-own muted">我的小组：{ownNames.length ? ownNames.join("、") : "未分组"}</p>
              <div className="team-groups-layout">
                <ul className="team-group-picker" aria-label="团队小组列表">
                  {groups.map(group => (
                    <li key={group.id}>
                      <button
                        type="button"
                        className={group.id === selected.id ? "" : "secondary"}
                        aria-pressed={group.id === selected.id}
                        onClick={() => setSelectedId(group.id)}
                      >
                        {group.name}
                      </button>
                    </li>
                  ))}
                </ul>
                <GroupDetail
                  key={selected.id}
                  group={selected}
                  path={`${groupPath}/${encodeURIComponent(selected.id)}`}
                  members={members}
                  membershipsLoading={groupMemberships.loading}
                  membershipsError={groupMemberships.error}
                  groupsByMember={groupsByMember}
                  canManage={canManage}
                  act={teamAction}
                />
              </div>
            </>
          );
        }}
      </ResourceState>
    </Panel>
  );
}

function GroupDetail({ group, path, members, membershipsLoading, membershipsError, groupsByMember, canManage, act }: {
  group: TeamGroup;
  path: string;
  members: Resource<Member[]>;
  membershipsLoading: boolean;
  membershipsError?: string;
  groupsByMember: ReadonlyMap<string, string[]>;
  canManage: boolean;
  act: (spec: ActionSpec) => void;
}) {
  const [memberId, setMemberId] = useState("");
  const [newName, setNewName] = useState(group.name);
  const groupMembers: Member[] = [];
  const available: Member[] = [];
  for (const member of members.data || []) {
    const assigned = groupsByMember.get(member.user_id)?.includes(group.id);
    (assigned ? groupMembers : available).push(member);
  }
  const selectedMember = available.find(member => member.user_id === memberId);

  function updateMembership(member: Member, add: boolean) {
    act({
      title: add ? "成员加入小组" : "成员移出小组",
      method: add ? "PUT" : "DELETE",
      path: `${path}/members/${encodeURIComponent(member.user_id)}`,
      content: <p>将 {memberName(member)} {add ? "加入" : "移出"}「{group.name}」。</p>,
      onResult: () => setMemberId(""),
    });
  }

  return (
    <div className="team-group-detail">
      <h3>{group.name}</h3>
      {canManage && (
        <div className="team-group-tools">
          <GroupNameForm
            label="小组新名称"
            value={newName}
            currentName={group.name}
            action="保存名称"
            onChange={setNewName}
            onSave={name => act({
              title: "修改小组名称",
              method: "PATCH",
              path,
              body: {name},
              content: <p>将「{group.name}」改名为「{name}」。</p>,
            })}
          />
          <button className="text-button danger" onClick={() => act({
            title: "删除小组",
            method: "DELETE",
            path,
            content: <p>删除「{group.name}」。该组成员将退出小组，并继续保有原有的团队成员资格和角色。</p>,
          })}>删除小组</button>
        </div>
      )}
      <h4>小组成员</h4>
      {members.loading || membershipsLoading ? (
        <p className="loading" role="status">正在加载小组成员…</p>
      ) : members.error || membershipsError ? (
        <Notice kind="error">{members.error || membershipsError}</Notice>
      ) : (
        <>
          {groupMembers.length ? (
            <ul className="team-group-roster">
              {groupMembers.map(member => (
                <li key={member.user_id}>
                  <span>
                    <strong>{memberName(member)}</strong>
                    <small>{member.email || member.user_id}</small>
                  </span>
                  {canManage && <button className="text-button danger" aria-label={`将 ${memberName(member)} 移出${group.name}`} onClick={() => updateMembership(member, false)}>移出</button>}
                </li>
              ))}
            </ul>
          ) : <p className="muted">暂无小组成员</p>}
          {canManage && available.length > 0 && (
            <form className="team-group-form" onSubmit={event => {
              event.preventDefault();
              if (selectedMember) updateMembership(selectedMember, true);
            }}>
              <label>
                选择要加入的成员
                <select value={selectedMember?.user_id || ""} onChange={event => setMemberId(event.target.value)}>
                  <option value="">选择团队成员</option>
                  {available.map(member => (
                    <option key={member.user_id} value={member.user_id}>{memberName(member)} · {member.email || member.user_id}</option>
                  ))}
                </select>
              </label>
              <button type="submit" disabled={!selectedMember}>加入小组</button>
            </form>
          )}
        </>
      )}
    </div>
  );
}
