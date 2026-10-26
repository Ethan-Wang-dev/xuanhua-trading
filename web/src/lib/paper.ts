import type { AccountInfo, DecisionRecord, HistoricalPosition, Position, PositionHistoryResponse, SystemStatus, TraderInfo } from '../types'
import type { Kline } from './api/data'

export interface PaperFill {
  action: string; time: string; price_usd: number; quantity: number
  fee_usd: number; realized_pnl_usd: number; reason: string
}
export interface PaperEvent {
  id: number; time: string; kind: string; message: string; equity_usd: number
  decision?: { action: string; reason: string; stop_loss: number; take_profit: number }
  fill?: PaperFill; system_prompt?: string; user_prompt?: string; raw_response?: string; model?: string
}
export interface PaperState {
  version: number
  config: { mode: 'demo' | 'live'; symbol: string; initial_cash_usd: number; candle_seconds: number }
  cash_usd: number; equity_usd: number; realized_pnl_usd: number; fees_usd: number; max_drawdown: number; halted: boolean
  last_quote?: { time: string; bid_usd: number; ask_usd: number }
  position?: { quantity: number; entry_usd: number; entry_fee_usd: number; opened_at: string }
  events: PaperEvent[]
}

export async function fetchPaperState(): Promise<PaperState> {
  const response = await fetch('/api/paper/state')
  if (!response.ok) throw new Error('模拟交易服务未连接，请先启动 paper 后端。')
  const state = await response.json() as PaperState
  if (state.version !== 1 || !Array.isArray(state.events) || !state.config || !['demo', 'live'].includes(state.config.mode)) {
    throw new Error('模拟账本格式不受支持。')
  }
  return state
}

// Adapt persisted paper facts to the ORIGINAL NOFX dashboard contracts.
// Never invent order-book, liquidation, flow or confidence data.
export function adaptPaperState(s: PaperState) {
  const id = 'xuanhua-paper'
  const symbol = s.config.symbol.replace(/-/g, '')
  const demo = s.config.mode === 'demo'
  const model = [...s.events].reverse().find(e => e.model)?.model || (demo ? 'DEMO_SCRIPT_NOT_AI' : '等待模型')
  const label = demo
    ? '离线演示 · 合成行情 + 固定脚本 · 非 AI 决策 · 虚拟资金'
    : '真实行情 + AI 决策 · 模拟成交 · 虚拟资金'
  const trader: TraderInfo = { trader_id: id, trader_name: '玄化交易 · 模拟账户', ai_model: model, strategy_name: demo ? '脚本演示' : 'AI 模拟交易' }
  const quoteTime = s.last_quote?.time || ''
  const quoteAge = Date.now() - Date.parse(quoteTime)
  const fresh = quoteTime !== '' && (demo || (quoteAge >= -5000 && quoteAge <= 45000))
  const status: SystemStatus = {
    trader_id: id, trader_name: trader.trader_name, ai_model: model, ai_provider: demo ? 'script' : 'compatible',
    is_running: fresh && !s.halted, start_time: s.events[0]?.time || quoteTime, runtime_minutes: 0,
    call_count: s.events.filter(e => e.kind === 'decision_requested').length,
    initial_balance: s.config.initial_cash_usd, scan_interval: `${s.config.candle_seconds / 60}m`,
    stop_until: '', last_reset_time: '', trading_blocked: s.halted,
    trading_error: s.halted ? '账户回撤限制已触发，暂停新开仓' : undefined,
  }
  const pnl = s.equity_usd - s.config.initial_cash_usd
  const unrealized = s.position && s.last_quote ? (s.last_quote.bid_usd - s.position.entry_usd) * s.position.quantity : 0
  const account: AccountInfo = {
    total_equity: s.equity_usd, wallet_balance: s.cash_usd, available_balance: s.cash_usd,
    unrealized_profit: unrealized, total_pnl: pnl, total_pnl_pct: pnl / s.config.initial_cash_usd * 100,
    initial_balance: s.config.initial_cash_usd, daily_pnl: 0, position_count: s.position ? 1 : 0,
    margin_used: 0, margin_used_pct: 0, // Spot funds are not leveraged margin.
  }
  const positions: Position[] = s.position && s.last_quote ? [{
    symbol: symbol, side: 'long', entry_price: s.position.entry_usd, mark_price: s.last_quote.bid_usd,
    quantity: s.position.quantity, leverage: 1, unrealized_pnl: unrealized,
    unrealized_pnl_pct: unrealized / (s.position.entry_usd * s.position.quantity) * 100,
    liquidation_price: 0, margin_used: 0,
  }] : []
  let request: PaperEvent | undefined
  const decisions: DecisionRecord[] = []
  let entry: PaperFill | undefined
  const closed: HistoricalPosition[] = []
  for (const event of s.events) {
    if (event.kind === 'decision_requested') { request = event; continue }
    const fill = event.fill
    if (fill?.action === 'buy') entry = fill
    if (fill?.action === 'sell' && entry) {
      closed.push({ id: event.id, trader_id: id, exchange_id: 'paper', exchange_type: 'paper', symbol: symbol,
        side: 'long', quantity: fill.quantity, entry_quantity: entry.quantity, entry_price: entry.price_usd,
        entry_order_id: '', entry_time: Date.parse(entry.time), exit_price: fill.price_usd, exit_order_id: '',
        exit_time: Date.parse(fill.time), realized_pnl: fill.realized_pnl_usd, fee: fill.fee_usd + entry.fee_usd,
        leverage: 1, status: 'CLOSED', close_reason: fill.reason, created_at: entry.time, updated_at: fill.time })
      entry = undefined
    }
    const success = event.kind === 'decision' || event.kind === 'protection'
    const action = fill?.action || event.decision?.action || 'wait'
    decisions.push({ timestamp: event.time, cycle_number: event.id,
      system_prompt: request?.system_prompt || '', input_prompt: request?.user_prompt || '',
      cot_trace: event.decision?.reason || event.message, decision_json: event.raw_response || '',
      account_state: { total_balance: event.equity_usd, available_balance: 0, total_unrealized_profit: 0, position_count: 0, margin_used_pct: 0 },
      positions: [], candidate_coins: [symbol],
      decisions: [{ action: action === 'buy' ? 'open_long' : action === 'sell' ? 'close_long' : action,
        symbol: symbol, quantity: fill?.quantity || 0, leverage: 1, price: fill?.price_usd || 0,
        stop_loss: event.decision?.stop_loss, take_profit: event.decision?.take_profit,
        reasoning: event.message, order_id: event.id, timestamp: event.time, success,
        error: success ? undefined : event.message }], execution_log: [event.message], success,
      error_message: success ? undefined : event.message })
    request = undefined
  }
  const history: PositionHistoryResponse = { positions: closed.reverse(), stats: null, symbol_stats: [], direction_stats: [] }
  let candles: Kline[] = []
  const input = [...s.events].reverse().find(e => e.user_prompt)?.user_prompt
  if (input) {
    try {
      const parsed = JSON.parse(input)
      if (Array.isArray(parsed.completed_candles)) candles = parsed.completed_candles.map((c: { open_time: string; open: number; high: number; low: number; close: number; volume: number }) => ({
        openTime: Date.parse(c.open_time), closeTime: Date.parse(c.open_time) + s.config.candle_seconds * 1000,
        open: c.open, high: c.high, low: c.low, close: c.close, volume: c.volume,
      }))
    } catch { /* Missing historical input means no chart, not invented candles. */ }
  }
  return { trader, status, account, positions, decisions: decisions.reverse().slice(0, 30), paper: { label, candles, history, realizedPnL: s.realized_pnl_usd, drawdownPct: s.max_drawdown * 100, demo } }
}
