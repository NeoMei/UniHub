# UniHub 用户中心

React + TypeScript + Vite 的中文用户控制台。构建产物由同源 UniHub BFF 托管；浏览器仅使用 HttpOnly 会话 Cookie，不接触或保存 access token。请求使用同源 Cookie，业务变更带 CSRF 与幂等键。

```sh
npm ci
npm test
npm run build
```

开发热更新可运行 `npm run dev` 作只读样式预览；`/v1` 与 `/auth` 代理到 `http://localhost:8088`。Vite origin 与 BFF 不同，浏览器变更会被 BFF 的 Origin/CSRF 检查拒绝；开发代理不重写安全来源。需要登录及变更时先 `npm run build`，再通过 Go 服务打开。OIDC 必须从已登记的 BFF origin 完成，正式验收使用 `http://localhost:8088/`，不通过 Vite 登录。

设计参考在 `design/console-concept.png`，可复用约束在 `design/system.md`。`public/assets/` 保留已确认 PNG 的原始纵横比。页面均绑定真实 API；未加载、无数据、无权限、网络失败及未知记账状态各自显示。

金额与额度使用 BigInt 格式化。CNY/6 以元展示，CREDIT/6 以模型额度展示；有微分金额时保留精度，不用 Number 参与金融转换。账户变更会隔离请求与局部表单，未完成的业务提交固定原账户并临时锁定切换。测试通过模拟传输完成顺序覆盖取消无效时的旧响应竞争。

“套餐与用量”是一级导航，显示当前会员的实际已付期限、账户共享 CREDIT、统一钱包现金与托管模型产品钱包现金，分别记账。原生共享现金调用和托管模型调用的资金归属应以实际消费记录为准。目录和概览统一按发布状态、账户类型、测试资格及产品筛选；未知产品链接保持未核验，不自动切换到其他产品。

概览的月付/年付卡片只携带商品与产品选择进入现有目录，不创建订单或自动打开购买确认。订单仍须现有确认；已有会员沿用升级报价及接受报价流程。切换付费账户或角色会重建目录和确认上下文。权益展示只翻译已识别标签（席位数、工作区）或已有中文标签，未知技术键不展示。

The admin billing-profile editor constructs the `BillingProfile` mutation body from its public fields. List metadata such as `actor_id`, `created_at`, `status`, and the implicit legacy `configuration_source` stays outside the mutation body. Unchanged model policy preserves the distinction between `null` and an empty allowlist. Native credit conversion changes require the existing explicit update or remove action. The request and profile both retain the displayed version for the server's compare-and-swap check.
