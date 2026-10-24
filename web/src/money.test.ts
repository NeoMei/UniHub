import { describe, expect, it } from "vitest";
import { activeSubscription, financeRole, fixed, parseCash, sum, spendable } from "./money";
describe("fixed financial amounts", () => {
  it("keeps integers beyond IEEE 754 precision intact", () =>
    expect(fixed("9007199254740993123456", 6)).toBe(
      "9,007,199,254,740,993.123456",
    ));
  it("preserves sub-cent prorated cash instead of truncating it", () =>
    expect(fixed("1000001")).toBe("1.000001"));
  it("uses six decimal places for credits without converting to cash", () =>
    expect(fixed("1000001", 6)).toBe("1.000001"));
  it("never invents zero for missing or invalid values", () => {
    expect(fixed(undefined)).toBe("—");
    expect(fixed("1e6")).toBe("—");
  });
  it("parses cents with exact integer arithmetic", () => {
    expect(parseCash("9007199254.99")).toBe("9007199254990000");
    expect(parseCash("0.01")).toBe("10000");
  });
  it.each(["0", "-1", "1e3", "1.001", "NaN", "1,000", "0.00"])(
    "rejects unsafe cash input %s",
    (value) => expect(() => parseCash(value)).toThrow(),
  );
  it("sums exact batch amounts", () =>
    expect(sum(["9007199254740993000000", "1000001"])).toBe(
      "9007199254740994000001",
    ));
  it("limits finances to owner and billing_admin", () => {
    expect(financeRole("owner")).toBe(true);
    expect(financeRole("billing_admin")).toBe(true);
    expect(financeRole("admin")).toBe(false);
    expect(financeRole("member")).toBe(false);
  });
});

it("excludes frozen, future and expired batches from spendable credit", () => {
  const fields = {
    available: "1000000",
    frozen: false,
    starts_at: "2026-10-01T00:00:00Z",
    expires_at: "2026-11-01T00:00:00Z",
  };
  expect(
    spendable(
      [
        fields,
        { ...fields, frozen: true },
        { ...fields, starts_at: "2026-12-01T00:00:00Z" },
        { ...fields, expires_at: "2026-10-24T00:00:00Z" },
      ],
      new Date("2026-10-24T00:00:00Z"),
    ),
  ).toBe("1000000");
});

it('treats an active database row as expired once paid-through is reached',()=>{const now=new Date('2026-10-24T00:00:00Z');expect(activeSubscription({status:'active',paid_through:'2026-10-23T23:59:59Z'},now)).toBe(false);expect(activeSubscription({status:'active',paid_through:'2026-10-24T00:00:00Z'},now)).toBe(false);expect(activeSubscription({status:'active',paid_through:'2026-10-24T00:00:01Z'},now)).toBe(true);expect(activeSubscription({status:'canceled',paid_through:'2027-01-01T00:00:00Z'},now)).toBe(false);});
