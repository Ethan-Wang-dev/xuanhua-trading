import { describe, it, expect } from 'vitest'
import { adaptPaperState, type PaperState } from './paper'

function state(): PaperState {
  return { version: 1, config: { mode: 'demo', symbol: 'BTC-USD', initial_cash_usd: 10000, candle_seconds: 300 },
    cash_usd: 8000, equity_usd: 10020, realized_pnl_usd: 0, fees_usd: 2, max_drawdown: .01, halted: false,
    last_quote: { time: new Date().toISOString(), bid_usd: 101, ask_usd: 101.1 },
    position: { quantity: 20, entry_usd: 100, entry_fee_usd: 2, opened_at: '2026-10-26T12:00:00Z' }, events: [] }
}

describe('paper ledger → original NOFX dashboard', () => {
  it('keeps cash, equity, spot position and demo identity distinct', () => {
    const view = adaptPaperState(state())
    expect(view.account.total_equity).toBe(10020)
    expect(view.account.available_balance).toBe(8000)
    expect(view.account.margin_used).toBe(0)
    expect(view.paper.drawdownPct).toBe(1)
    expect(view.paper.realizedPnL).toBe(0)
    expect(view.positions[0].unrealized_pnl).toBe(20)
    expect(view.positions[0].leverage).toBe(1)
    expect(view.paper.label).toContain('非 AI 决策')
    expect(view.paper.candles).toEqual([])
  })
  it('pairs actual fills into history and preserves rejected decisions', () => {
    const s = state()
    s.events = [
      { id: 1, time: '2026-10-26T12:00:00Z', kind: 'decision_requested', message: '', equity_usd: 10000, system_prompt: 'rules', user_prompt: '{}' },
      { id: 2, time: '2026-10-26T12:00:01Z', kind: 'decision', message: 'buy', equity_usd: 9998,
        fill: { action: 'buy', time: '2026-10-26T12:00:01Z', price_usd: 100, quantity: 20, fee_usd: 2, realized_pnl_usd: 0, reason: 'entry' } },
      { id: 3, time: '2026-10-26T12:05:00Z', kind: 'rejected', message: 'risk limit', equity_usd: 10000 },
      { id: 4, time: '2026-10-26T12:10:00Z', kind: 'protection', message: 'stop', equity_usd: 9956.04,
        fill: { action: 'sell', time: '2026-10-26T12:10:00Z', price_usd: 98, quantity: 20, fee_usd: 1.96, realized_pnl_usd: -43.96, reason: 'stop' } },
    ]
    const view = adaptPaperState(s)
    expect(view.paper.history.positions[0].realized_pnl).toBe(-43.96)
    expect(view.paper.history.positions[0].fee).toBe(3.96)
    expect(view.decisions[0].decisions[0].action).toBe('close_long')
    expect(view.decisions[1].success).toBe(false)
    expect(view.decisions[2].system_prompt).toBe('rules')
  })
  it('uses recorded candles without falling back to showcase prices', () => {
    const s = state()
    s.events = [{ id: 1, time: '2026-10-26T12:00:00Z', kind: 'decision_requested', message: '', equity_usd: 10000,
      user_prompt: JSON.stringify({ completed_candles: [{ open_time: '2026-10-26T11:55:00Z', open: 100, high: 102, low: 99, close: 101, volume: 5 }] }) }]
    expect(adaptPaperState(s).paper.candles[0].close).toBe(101)
    s.events[0].user_prompt = 'broken input'
    expect(adaptPaperState(s).paper.candles).toEqual([])
  })
  it('does not label stale real quotes as running', () => {
    const s = state(); s.config.mode = 'live'; s.last_quote!.time = '2020-01-01T00:00:00Z'
    expect(adaptPaperState(s).status.is_running).toBe(false)
    expect(adaptPaperState(s).paper.label).toContain('模拟成交')
  })
})
