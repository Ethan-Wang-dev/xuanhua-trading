# 玄化交易：模拟交易 Agent

这是 fork 内新增的模拟交易运行入口。它参考 NOFX 的“账户与行情上下文 → 提示词 → 结构化决策 → 执行记录”设计，独立于上游真实下单适配器。**此模块没有真实订单接口，不接受交易所密钥。**

当前范围：单标的 USD 现货、买入／持有／全部卖出／等待、无杠杆、一个持仓。默认 BTC-USD。术数 Agent 尚未实现；可以通过独立 JSON 文件接入外部判断。NOFX 原版 Web Dashboard 已通过 `/paper` 只读入口接入这个模拟引擎；Autopilot 的实盘配置流程仍独立。

## 环境

macOS 或 Linux，Go 1.23+。本目录为独立 Go module，只有标准库依赖，不需要安装上游交易所 SDK、Docker 或数据库。不要在仓库根目录运行下面的 Go 命令。

```bash
cd '/Users/jamshed/orca/projects/玄化交易/app/paper'
go test -race ./...
```

## 1. 离线演示：无需密钥

```bash
go run . --mode demo --interval 2s
```

另开终端，在 `app/web` 中运行 `npm ci --ignore-scripts`（首次）和 `npm run dev:paper`，打开 http://127.0.0.1:3000/paper 。按 Ctrl+C 分别停止前后端。`8787` 为模拟 API 端口，浏览器访问根路径会跳转到原版前端。

合成行情与固定脚本使用真实的模拟记账代码，但**不是 AI 决策，也不代表策略盈利能力**。每轮演示推进一个模拟 K 线周期；界面与日志明确标注数据来源。默认账本 `data/paper-demo.json`，重启后继续使用。若需要全新演示，使用新的 `--ledger data/demo-2.json`，不要覆盖旧记录。

运行一次有限的买入—持有—卖出验证：

```bash
go run . --mode demo --cycles 3 --interval 1s --listen '' --ledger data/three-step.json
```

## 2. 检查真实行情：无需密钥

```bash
go run . --check-market --symbol BTC-USD
```

使用 Coinbase Exchange 的公开 ticker 与 candles 接口。只读取行情，不创建账本，不调用模型。网络或地区限制、行情过期、K 线缺失会报错，不会退回到合成行情冒充实时行情。

## 3. 真实行情 + OpenAI 兼容模型 + 模拟资金

在本地 shell 配置以下环境变量（不会自动加载 `.env`）：

```bash
export XUANHUA_BASE_URL='https://你的服务地址/v1'
export XUANHUA_MODEL='你的模型名称'
# 将 XUANHUA_API_KEY 设置为你的密钥；不要提交到仓库或写进命令截图。
go run . --mode live
```

`live` 仅表示真实行情与真实 AI，**所有成交和资金仍然模拟**。默认账本 `data/paper-live.json`，不可与演示账本混用。若密钥未配置，启动直接失败，不会悄悄使用脚本代替模型。

接口地址必须是 HTTPS，只有本机 localhost 允许 HTTP。请求发往 `BASE_URL/chat/completions`，使用 Bearer token、`response_format: {"type":"json_object"}`，不启用 streaming。服务须支持这一协议和 JSON mode。模型请求超时 60 秒，不自动无限重试；HTTP 错误不回显供应商响应体中的潜在敏感信息。

默认每 15 秒检查报价和持仓保护条件，每根新完成的 5 分钟 K 线最多调用模型一次。失败的模型调用不会在同一根 K 线上重复尝试。可用 `--candle-seconds 3600` 改为小时判断；不同周期必须使用独立账本。这是普通时间周期，**不是已实现的真太阳时时辰调度**。

## 数据、提示词与规则

- `providers.go`：公开行情、按时间排序且只保留完成的 K 线、缺失／过期校验、最近 60 根 OHLCV、SMA20、平均高低价差；模型输入包含当前账户、持仓和最近事件。
- `prompt.txt`：完整可读的系统提示词。外部术数结论作为数据；没有结论时禁止编造卦象。
- `engine.go`：按买一／卖一及滑点模拟成交；按真实模拟入场价与费用计算盈亏比、仓位和风险。
- `runner.go`：每次行情轮询先处理保护条件；模型返回后再次获取报价与检查保护条件。不会用分析前的价格假装成交。
- `store.go`：余额、持仓和事件以单个 JSON 文件原子提交；进程锁阻止并发使用同一账户。重启后不会重复执行同一根 K 线的决策。保留原始模型响应、输入与执行结果。
- 前端复用 `web/src/components/terminal/TerminalDashboard.tsx`；`web/src/lib/paper.ts` 仅负责账本数据适配。旧的自写 HTML 看板已删除。模拟入口禁用实盘操作、付费市场请求与隐藏演示开关；未采集的数据明确显示未接入。

默认实验参数：虚拟本金 10,000 USD；手续费单边 10 bps；额外滑点单边 5 bps；单次投入含费用至多净值的 20%；按预设止损估算的损失预算至多 1%；扣除预估成本后的盈亏比至少 3；账户回撤达到 10% 后退出持仓并暂停新开仓。这些是可检查的工程默认值，不是已验证的最佳策略。修改配置后需使用新账本。

## 外部判断接口

使用 `--signal /本地路径/signal.json`。格式如下，时间需要替换为实际有效时间：

```json
{
  "symbol": "BTC-USD",
  "as_of": "2026-10-26T12:00:00Z",
  "expires_at": "2026-10-26T14:00:00Z",
  "source": "人工输入／尚未实现梅花 Agent",
  "conclusion": "这里填写实际判断，不用示例充当术数结论",
  "evidence": "原始依据与出处"
}
```

对象不一致、来源／结论缺失、时间过期会阻止新的 AI 决策。模型返回后再次检查有效期。这个接口目前让模型综合理解结论，没有实现“术数方向强制约束”或两个 Agent 互相追问。

## 已知边界

- 本地模拟，不是交易所测试网撮合；不模拟完整盘口、排队或交易所最小订单规则。
- 止损止盈在运行期间的轮询点以及模型返回时检查。进程停止、网络中断或模型请求期间可能错过盘中触发；无法保证损失恰好停在预算线。跳价使用下一次观察到的价格退出，不补造理想成交。
- 账本是本地原子快照，有顺序与余额校验，不是防篡改审计系统；长期运行会增长，后续需迁移数据库／归档。
- 余额使用浮点数与数量截断，适用于此模拟版本；不宣称达到实盘交易所资金精度要求。
- 不对术数体系、全球市场覆盖或获利能力作已完成声明。
- 本地自动测试使用 HTTP 协议替身；另外已使用用户本地配置成功验证一次 DeepSeek 真实调用，见验证记录。不同模型和供应商仍需单独检查。

公共行情协议来源：[Coinbase ticker](https://docs.cdp.coinbase.com/api-reference/exchange-api/rest-api/products/get-product-ticker)、[Coinbase candles](https://docs.cdp.coinbase.com/api-reference/exchange-api/rest-api/products/get-product-candles)。

## 本地 DeepSeek 配置

本次已把用户提供的配置保存到本地 `paper/.env`（权限 0600，Git 忽略）。文件不会自动读取；重启时：

```bash
cd '/Users/jamshed/orca/projects/玄化交易/app/paper'
set -a
source .env
set +a
go run . --mode live --ledger data/deepseek-validation.json
```

当前模型 `deepseek-flash`，地址 `https://api.deepseek.com`。只运行本地模拟账户。模型按新完成的 K 线调用，会消耗供应商 API 额度；Ctrl+C 可停止。

## 原版前端的构建范围

`npm run dev:paper` / `npm run build:paper` 使用原项目已有的 Dashboard 和 Candles 组件，不需要缺失的 `web/vendor/charting` 子模块。原版 Frontier 高级图表依然依赖该子模块，不能把模拟页面构建成功视为该图表也已恢复。Vite 开发代理将 `/api/paper/*` 转发到 `127.0.0.1:8787/api/*`。生产部署需配置同样的反向代理。
