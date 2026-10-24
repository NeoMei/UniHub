const INTEGER = /^-?\d+$/;
export function fixed(value: string | undefined, places = 2): string {
  if (value === undefined || !INTEGER.test(value)) return "—";
  const n = BigInt(value),
    negative = n < 0n,
    absolute = negative ? -n : n;
  const whole = (absolute / 1_000_000n)
    .toString()
    .replace(/\B(?=(\d{3})+(?!\d))/g, ",");
  const rawFraction = (absolute % 1_000_000n).toString().padStart(6, "0");
  // CNY can contain sub-cent prorated amounts; never silently hide those.
  const fraction =
    places === 2
      ? rawFraction.replace(/0+$/, "").padEnd(2, "0")
      : rawFraction.slice(0, places);
  return `${negative ? "−" : ""}${whole}${places ? "." + fraction : ""}`;
}
export function parseCash(value: string): string {
  const clean = value.trim();
  if (!/^\d+(\.\d{1,2})?$/.test(clean))
    throw new Error("请输入大于 0 的金额，最多两位小数。");
  const [whole, fraction = ""] = clean.split(".");
  const n = BigInt(whole) * 1_000_000n + BigInt(fraction.padEnd(6, "0"));
  if (n <= 0n) throw new Error("金额必须大于 0。");
  return n.toString();
}
export function sum(values: string[]): string {
  return values.reduce((a, b) => a + BigInt(b), 0n).toString();
}
export function date(value: string | undefined): string {
  if (!value || value.startsWith("0001")) return "—";
  const parsed = new Date(value);
  return Number.isNaN(parsed.getTime())
    ? "—"
    : new Intl.DateTimeFormat("zh-CN", {
        year: "numeric",
        month: "2-digit",
        day: "2-digit",
      }).format(parsed);
}
export const financeRole = (role: string) =>
  role === "owner" || role === "billing_admin";
export const managementRole = (role: string) =>
  role === "owner" || role === "admin";

/** A ledger-available quantity may still be frozen or outside its validity. */
export function spendable(
  batches: {
    available: string;
    frozen: boolean;
    starts_at: string;
    expires_at: string;
  }[],
  now = new Date(),
): string {
  return sum(
    batches
      .filter(
        (b) =>
          !b.frozen &&
          new Date(b.starts_at) <= now &&
          now < new Date(b.expires_at),
      )
      .map((b) => b.available),
  );
}

export function activeSubscription(subscription: { status:string; paid_through:string }, now = new Date()): boolean {
  return subscription.status.toLowerCase() === 'active' && now < new Date(subscription.paid_through);
}
