# 可组合产品收费配置

用户已明确要求独立产品仅配置收费方式，即可组合订阅、周期赠送额度、加量包和预付按量计费；并确认预付直接扣人民币充值余额。本文记录该批准范围及兼容裁定，不重复索取相同批准。

## 领域与配置

收费配置属于 product，而非 OIDC client；同一产品所有客户端共享一份版本化 ProductBillingProfile。变更由受信运营或部署配置执行，产品 token 仅可读取获准产品配置，不能自行改价或扩大权限。

组件：subscription.enabled；included_credits.enabled；addons.enabled；usage.enabled、require_subscription、funding_modes（credits/prepaid_cash）、default_funding_mode。提供 hybrid、prepaid、subscription 三个预设，预设只是组件组合。也允许订阅+预付现金或无会员的独立额度包等有效组合。included_credits 要求订阅及 credits 资金模式；addons 要求 credits 模式。订阅价格/周期/等级/赠送额度仍是不可变 Offer 的条款，不在身份注册表复制一套。

缺省配置为版本0的现有 hybrid（订阅+月度额度+加量包，模型调用须会员）。新配置按产品追加版本保存并记录操作者，乐观版本控制防覆盖；不能重写历史版本。独立 JSON 部署配置提供三个模板和自定义组合，不触碰身份 gate。配置文件只含公共商业元数据。

## 三个预设

- hybrid：会员订阅、每月赠送额度、加量包、credits 使用；额度用尽拒绝，不自动扣现金。年付仍逐月发放。
- prepaid：不要求会员，充值到账 CNY/6 后按可信计量消耗 llmrouter 产品钱包现金。无赠送额度、无透支、不会扣来源产品现金或统一钱包。
- subscription：仅售月/年有效期及功能权益，monthly_credit=0，不产生额度批次、零数量账务命令或无限模型权限。新纯订阅不允许根据未消费额度自动全额退款，退款转人工核实。

允许明确组合两种 funding_modes；凭证签发可以选择获准模式，省略时使用配置缺省。一次调用只有一种资产，不隐式把 CREDIT 当人民币或自动从额度转扣现金。授予 credits 和现金的费率分别发布、分别标明资产，旧费率缺省 CREDIT/6。

## 调用与现金安全

模型仍由 llmrouter 提供，所有预付模型现金从对应付费账户的 llmrouter 钱包扣除。短期模型凭证固化 funding_mode/asset，发放和预占检查当前产品配置、成员关系及可选会员门槛；旧凭证缺省 credits。预占再核对可信凭证、费率和资产，不允许产品请求指定现金钱包。

现金路径使用账户锁和既有 durable command：llmrouter钱包→调用专用预占账户→收入；失败释放与未用余额退回原钱包。结算消费不超过已确认预占；usage未知保持待核实，网络不明不换操作重扣。额度路径保留现有批次到期、冻结与分配规则。统一钱包划拨及资金授权不因此扩大。

Rate新增asset（历史空=CREDIT/6）；Call固化charge_asset、funding_mode、funding_product_id、billing_profile_version。金额、数量仍是规范十进制整数字符串；CNY/6充值需整分，模型消费可到微元。已创建订单、付费年订阅逐月发放、在途调用与UNKNOWN恢复使用原条款、费率、资产及账务载荷，不按新配置重新计价。关闭组件只影响新的销售/预占，不能撤销已付款权益。

llmrouter 网关需一次通用升级：按可信凭证资产选择对应模型费率，旧空资产视为CREDIT；持久化调用资产，恢复/收据核对资产；各来源产品不增加独立计费实现。网关不使用本地钱包重复扣款。

## 查询、接入与验证

配置读取/更新 API、SDK、OpenAPI、示例配置和本地验收工具同步。resources 返回产品配置、允许模式及可用性；普通成员不获得财务字段。调用明细返回真实资产；汇总按资产分别汇总，不把 CNY 与 CREDIT 相加。单资产保持原 asset/charged 兼容；混合资产省略单一总金额并提供 charges_by_asset。签名游标绑定新增资产筛选。

示例用户中心按组件展示纯订阅/额度/余额，接入产品依旧完全拥有 UI 与功能判断。三预设真实PG/Formance闭环、现金不足/预占/释放/响应丢失、旧CREDIT无现金兜底、纯订阅零发放/续费/退款、跨账户/产品/角色、混合资产报表、网关双资产选择与恢复都须验证。真实支付商户及真实供应商不在此变更中，现有 sandbox 边界不放宽。
