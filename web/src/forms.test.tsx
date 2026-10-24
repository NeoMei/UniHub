// @vitest-environment jsdom
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { SettingsPage } from "./forms";
afterEach(cleanup);
it("requires explicit unchecked authorization opt-in and does not auto-enable funding when a product changes", () => {
  const act = vi.fn();
  render(
    <SettingsPage
      account={{ id: "A", name: "账户 A", kind: "personal", role: "owner" }}
      session={{ authenticated: true, sandbox: true, csrf_token: "csrf" }}
      products={[{ id: "product-a", name: "产品 A" }]}
      refresh={() => {}}
      act={act}
      settings={{ data: [], loading: false }}
    />,
  );
  const checkbox = screen.getByRole("checkbox") as HTMLInputElement;
  expect(checkbox.checked).toBe(false);
  fireEvent.change(screen.getByLabelText("产品"), {
    target: { value: "product-a" },
  });
  expect(checkbox.checked).toBe(false);
  fireEvent.click(screen.getByText("核对并保存"));
  expect(act).toHaveBeenCalledOnce();
  expect(act.mock.calls[0][0].body).toMatchObject({
    enabled: false,
    product_id: "product-a",
    purpose: "funding",
    version: 0,
  });
});
