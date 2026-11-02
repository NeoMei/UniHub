// @vitest-environment jsdom
import { fireEvent, render, screen } from "@testing-library/react";
import { expect, it } from "vitest";
import { Onboarding } from "./Onboarding";

it("guides a least-privilege third-party integration and exports a prepare command", () => {
  render(<Onboarding sandbox={true} />);
  expect(screen.getByRole("heading", { name: "三方接入工作台" })).toBeDefined();
  expect(screen.getByText(/不会在浏览器保存客户端密钥/)).toBeDefined();
  expect((screen.getByRole("checkbox", { name: "openid" }) as HTMLInputElement).checked).toBe(true);
  const product = screen.getByLabelText("产品标识");
  const callback = screen.getByLabelText("回调地址");
  fireEvent.change(product, { target: { value: "notes" } });
  fireEvent.change(callback, { target: { value: "https://notes.example.com/auth/callback" } });
  expect(screen.getByText(/unihubctl integration prepare/)).toBeDefined();
  expect(screen.getByRole("button", { name: "生成接入计划" })).toBeDefined();
  expect(screen.getAllByText(/应用需要在服务端完成/).length).toBeGreaterThan(0);
});
