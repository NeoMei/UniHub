# UniHub 扩展路线与第三方项目调研

日期：2026-10-24。范围：记录用户确认的优先级与候选项目证据；不代表已实施或已选定依赖。

## 已确认的顺序

| 阶段 | 目标 | 交付范围 |
| --- | --- | --- |
| 第一阶段：自有产品 | 先做好用户管理与充值管理 | 统一注册登录、个人/企业团队、产品账户、统一钱包、充值订单、会员续费、月度模型额度、加量包、管理后台与账务恢复 |
| 第二阶段：第三方账户 | 让用户和企业统一管理外部账户，供获准 Agent 使用 | 账户连接、多账号选择、授权范围、连接生命周期、按 Agent 委托、撤销及审计；具体供应商待选 |
| 第三阶段：第三方 MCP/API | 复用连接和授权能力接入更多工具与服务 | MCP/API 接入登记、调用权限和审计；两者的内部先后顺序与收费方式待后续确定 |

第一阶段先完成 SuperWagie 的真实闭环，再依序接入 AgentWiki、llmrouter 自身产品入口，随后璇玑、灵犀、虾说。llmrouter 的平台模型服务从 SuperWagie 阶段起使用。

年付模型额度逐月发放、月度重置；所有产品的模型调用统一消耗对应付费账户的 llmrouter 模型额度。耗尽后通过升级或加量包扩容。未来第三方工具接入不自动改变这些已确认规则。

## 高 Star 项目核查

用户尚未指定记忆中项目的名字；以下是匹配程度较高的候选，不能认定其中任一就是原项目。Star 为当日查询值，衡量关注度，不能单独证明成熟度。

| 项目 | 当日 Star | 用途与参考价值 | 边界 |
| --- | ---: | --- | --- |
| [Composio](https://github.com/ComposioHQ/composio) | 30,716 | Agent 工具、第三方账户、会话内工具权限和 MCP 接入 | 公开仓库是 MIT SDK/CLI；托管凭据与执行服务是独立平台，完整自托管需 Enterprise 安排 |
| [Nango](https://github.com/NangoHQ/nango) | 12,965 | OAuth/API key、连接管理、令牌刷新、认证请求代理 | ELv2；免费自托管以 Auth/Proxy 为主，不能假定包含完整 Agent/MCP 能力 |
| [JustAuth](https://github.com/justauth/JustAuth) | 17,528 | Java 第三方登录适配、统一身份字段和供应商差异处理 | MIT 登录库，不能直接承担 Agent 连接授权及平台账务 |

### Composio

官方区分用户、认证配置、已连接账户和会话；同一用户可以连接多个同类账户。可从中借鉴按会话选择账户、限定工具、通过后端执行而不向模型提供供应商令牌的流程。[账户概念](https://docs.composio.dev/reference/glossary)、[会话配置](https://docs.composio.dev/docs/configuring-sessions)

其默认 Cloud 部署由 Composio 保管凭据并执行请求；私有网络、自托管及客户管理密钥分别有不同部署安排。自带 OAuth 应用不改变 Cloud 的凭据保管方，不能把 SDK 的 MIT 许可证理解为完整服务可免费自行部署。[凭据与部署边界](https://docs.composio.dev/docs/security/token-custody)、[仓库说明](https://github.com/ComposioHQ/composio)

共享连接目前标注 Experimental，有逐连接 ACL，并要求在会话中显式指定。该能力不能替代 UniHub 自己的团队成员与 Agent 授权模型。[共享连接](https://docs.composio.dev/docs/extending-sessions/shared-connections)

### Nango

官方要求集成应用自行保存连接与用户/组织的业务归属；其连接 ID、自动刷新和认证代理很适合作为第二阶段的接口参考。[Auth guide](https://nango.dev/docs/guides/auth/auth-guide)、[令牌刷新](https://nango.dev/docs/guides/auth/token-refreshing)

免费自托管版主要包含 Auth、Proxy 和对应观测，不包含预制工具、同步、工具执行、Webhook、MCP、RBAC 或审计等完整能力。控制台按单个可信组织使用，不能直接当作 UniHub 面向客户的多租户后台。[官方自托管矩阵](https://nango.dev/docs/guides/platform/self-hosting/self-hosting#free-self-hosting)

仓库采用 ELv2，含托管/受管服务限制；是否适合 UniHub 对外提供连接能力，需要按实际产品范围核对授权，当前不作许可已满足的结论。[许可证](https://github.com/NangoHQ/nango/blob/master/LICENSE)

### JustAuth 与已有身份方向

JustAuth 对多平台登录有参考价值；当前 Casdoor 身份方向已有大量 OAuth/OIDC 供应商支持。第一阶段保持统一身份边界，不因候选项目多而新增一套认证底座。[JustAuth](https://github.com/justauth/JustAuth)、[Casdoor 供应商](https://casdoor.ai/docs/provider/oauth/overview/)

## 当前决策

优先交付自有产品闭环。第三方能力保留可替换适配器方向，暂不选择 Composio 或 Nango 作为生产依赖，不安装或连接真实账户。

到第二阶段再确定首批供应商、部署与凭据保管方式、商业授权、团队共享边界。建模时分别处理登录身份、外部连接和 Agent 委托；拥有会员或账务权限不自动获得第三方账户操作权。第三方 MCP/API 调用可能具有自己的成本与副作用，届时单独定义授权和恢复规则。

以上核查来自官方仓库与文档，未完成自托管实测、真实授权流程或调用验收。
