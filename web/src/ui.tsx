import { AlertCircle, Inbox, LoaderCircle, X } from "lucide-react";
import { useEffect, useRef } from "react";
import type { ReactNode } from "react";
import type { Resource } from "./useResource";
export function Notice({
  children,
  kind = "info",
}: {
  children: ReactNode;
  kind?: string;
}) {
  return (
    <div
      className={`notice ${kind}`}
      role={kind === "error" ? "alert" : "status"}
    >
      <AlertCircle size={17} />
      <div>{children}</div>
    </div>
  );
}
export function Empty({
  title = "暂无记录",
  children,
  icon,
}: {
  title?: string;
  children?: ReactNode;
  icon?: ReactNode;
}) {
  return (
    <div className="empty">
      {icon || <Inbox size={28} />}
      <strong>{title}</strong>
      {children && <div>{children}</div>}
    </div>
  );
}
export function ResourceState<T>({
  resource,
  children,
  empty = "暂无记录",
  emptyHeaders,
  emptyIcon,
  emptyDescription,
}: {
  resource: Resource<T[]>;
  children: (data: T[]) => ReactNode;
  empty?: string;
  emptyHeaders?: string[];
  emptyIcon?: ReactNode;
  emptyDescription?: string;
}) {
  if (resource.loading)
    return (
      <div className="loading" role="status">
        <LoaderCircle className="spin" size={18} />
        正在加载…
      </div>
    );
  if (resource.error)
    return (
      <Notice kind="error">
        {resource.error}
        {resource.error.includes("登录") && <a href="/auth/login">重新登录</a>}
      </Notice>
    );
  const values = resource.data || [];
  return values.length ? (
    children(values)
  ) : emptyHeaders ? (
    <Table headers={emptyHeaders}>
      <tr>
        <td colSpan={emptyHeaders.length}>
          <Empty title={empty} icon={emptyIcon}>
            {emptyDescription}
          </Empty>
        </td>
      </tr>
    </Table>
  ) : (
    <Empty title={empty} icon={emptyIcon}>
      {emptyDescription}
    </Empty>
  );
}
export function Panel({
  title,
  action,
  children,
  className = "",
}: {
  title?: string;
  action?: ReactNode;
  children: ReactNode;
  className?: string;
}) {
  return (
    <section className={`panel ${className}`}>
      {title && (
        <div className="panel-head">
          <h2>{title}</h2>
          {action}
        </div>
      )}
      {children}
    </section>
  );
}
export function Status({ value }: { value?: string }) {
  const labels: Record<string, string> = {
    active: "生效中",
    ACTIVE: "生效中",
    PAID: "已付款",
    FULFILLED: "已发放",
    APPLIED: "已记账",
    completed: "已完成",
    pending: "处理中",
    PENDING: "处理中",
    CREATED: "待处理",
    SUBMITTING: "提交中",
    UNKNOWN: "待核实",
    REJECTED: "未完成",
    CANCELED: "已取消",
    REFUNDED: "已退款",
    succeeded: "已完成",
    expired: "已到期",
    SUCCEEDED: "已完成",
    refunded: "已退款",
    FROZEN: "退款处理中",
    MANUAL_REVIEW: "需要人工核实",
    FAILED: "未完成",
    REVERSED: "已撤回",
  };
  return (
    <span
      className={`status ${["APPLIED", "FULFILLED", "PAID", "active", "ACTIVE", "SUCCEEDED"].includes(value || "") ? "good" : ["UNKNOWN", "REJECTED"].includes(value || "") ? "warn" : ""}`}
    >
      {labels[value || ""] || value || "—"}
    </span>
  );
}
export function Table({
  headers,
  children,
}: {
  headers: string[];
  children: ReactNode;
}) {
  return (
    <div className="table-scroll">
      <table>
        <thead>
          <tr>
            {headers.map((h, i) => (
              <th key={i}>{h}</th>
            ))}
          </tr>
        </thead>
        <tbody>{children}</tbody>
      </table>
    </div>
  );
}
export function Modal({
  title,
  children,
  onClose,
  busy = false,
}: {
  title: string;
  children: ReactNode;
  onClose: () => void;
  busy?: boolean;
}) {
  const ref = useRef<HTMLDialogElement>(null);
  useEffect(() => {
    const dialog = ref.current!;
    dialog.showModal();
    return () => dialog.close();
  }, []);
  return (
    <dialog
      ref={ref}
      className="modal"
      onCancel={(event) => {
        event.preventDefault();
        if (!busy) onClose();
      }}
    >
      <div className="modal-head">
        <h2>{title}</h2>
        <button
          aria-label="关闭"
          className="icon-button"
          onClick={onClose}
          disabled={busy}
        >
          <X size={19} />
        </button>
      </div>
      {children}
    </dialog>
  );
}
export function Detail({
  label,
  children,
}: {
  label: string;
  children: ReactNode;
}) {
  return (
    <div className="detail">
      <dt>{label}</dt>
      <dd>{children}</dd>
    </div>
  );
}
