import useSWR from 'swr'
import { TerminalDashboard } from '../components/terminal/TerminalDashboard'
import { adaptPaperState, fetchPaperState } from '../lib/paper'

export function PaperDashboardPage() {
  const { data, error, isLoading } = useSWR('xuanhua-paper-state', fetchPaperState, { refreshInterval: 3000, shouldRetryOnError: false })
  if (error) return <div className="p-8 text-nofx-text-main" role="alert">{error.message} <p>启动命令：在 app/paper 中运行 go run . --mode demo</p></div>
  if (isLoading || !data) return <div className="p-8 text-nofx-text-muted">正在读取模拟账户…</div>
  const view = adaptPaperState(data)
  return <TerminalDashboard selectedTrader={view.trader} traders={[view.trader]} selectedTraderId={view.trader.trader_id}
    onTraderSelect={() => {}} status={view.status} account={view.account} positions={view.positions}
    decisions={view.decisions} paper={view.paper} />
}
