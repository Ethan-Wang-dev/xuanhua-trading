# 玄化交易 · Xuanhua Trading

基于 [NOFX](https://github.com/NoFxAiOS/nofx) 的术数与 AI 交易实验项目。

我们希望让两个专业 Agent 协作：梅花易数 Agent 根据用户认可的铁书生体系理解问题、起卦和解卦；交易 Agent 结合术数判断、行情与账户状态，提出可执行的交易计划。后续再将大六壬、奇门遁甲作为独立策略进行竞争。

**当前状态：fork 初始化。术数 Agent、真太阳时调度与专用模拟交易流程均尚未实现。现有交易功能来自上游，不代表本项目已验证有效。**

## 开发入口

- 项目计划与边界：[docs/xuanhua/PROJECT.md](docs/xuanhua/PROJECT.md)
- 上游文档与安装说明：[README.upstream.md](README.upstream.md)
- 前端：`web/`；交易决策：`kernel/`；行情：`market/`；执行：`trader/`
- 开发分支：`xuanhua/main`
- 本项目：https://github.com/Ethan-Wang-dev/xuanhua-trading

本地源码启动方式后续单独核验。上游 README 的快捷安装脚本与部分 Compose 配置可能拉取上游镜像，不会自动包含本 fork 的修改；不要将下载上游镜像当作运行本项目。

## 方法论资料

教材尚待收集。工作区的 `../materials/` 用于本地资料，不包含在这个公开仓库内。尚未确定的起卦、解卦、应期及交易映射规则不使用臆造内容填充。

## 来源与许可

本项目派生自 NOFX，保留原始版权声明与 [AGPL-3.0 许可证](LICENSE)。初始化基于上游提交 `4a5e9ac2a13e6696a276440a3243b2055206512c`。原始说明保存在 `README.upstream.md`。

这是娱乐性实验项目。未来展示时应区分模拟与实盘、历史结果与实时结果；当前 fork 继承了上游真实下单能力，本次初始化没有启动任何交易服务或接入账户。
