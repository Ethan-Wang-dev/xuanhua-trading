package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"
)

// This module deliberately has no exchange execution client or credentials.
// All balances and fills exist only in the local paper ledger.
type Config struct {
	Symbol        string  `json:"symbol"`
	Mode          string  `json:"mode"`
	InitialCash   float64 `json:"initial_cash_usd"`
	FeeBPS        float64 `json:"fee_bps"`
	SlippageBPS   float64 `json:"slippage_bps"`
	MaxAllocation float64 `json:"max_allocation"`
	RiskFraction  float64 `json:"risk_fraction"`
	MinRewardRisk float64 `json:"min_reward_risk"`
	MaxDrawdown   float64 `json:"max_drawdown"`
	Granularity   int     `json:"candle_seconds"`
}

func defaultConfig(mode, symbol string) Config {
	return Config{symbol, mode, 10000, 10, 5, .20, .01, 3, .10, 300}
}

func (c Config) Validate() error {
	if c.Mode != "demo" && c.Mode != "live" {
		return errors.New("mode must be demo or live (paper only)")
	}
	if !positive(c.InitialCash) || !finite(c.FeeBPS) || c.FeeBPS < 0 || c.FeeBPS > 100 || !finite(c.SlippageBPS) || c.SlippageBPS < 0 || c.SlippageBPS > 100 {
		return errors.New("invalid cash or transaction costs")
	}
	for _, v := range []float64{c.MaxAllocation, c.RiskFraction, c.MaxDrawdown} {
		if !positive(v) || v > 1 {
			return errors.New("risk fractions must be in (0,1]")
		}
	}
	if !positive(c.MinRewardRisk) || c.MinRewardRisk < 1 {
		return errors.New("invalid minimum reward/risk")
	}
	if c.Granularity != 60 && c.Granularity != 300 && c.Granularity != 900 && c.Granularity != 3600 {
		return errors.New("candle seconds must be 60, 300, 900 or 3600")
	}
	if !symbolPattern.MatchString(c.Symbol) {
		return errors.New("symbol must be a USD spot pair, e.g. BTC-USD")
	}
	return nil
}

type Quote struct {
	Time time.Time `json:"time"`
	Bid  float64   `json:"bid_usd"`
	Ask  float64   `json:"ask_usd"`
}

func (q Quote) Validate(now time.Time) error {
	if !positive(q.Bid) || !positive(q.Ask) || q.Bid > q.Ask {
		return errors.New("invalid bid/ask")
	}
	if q.Time.IsZero() || now.Sub(q.Time) > 45*time.Second || q.Time.After(now.Add(5*time.Second)) {
		return errors.New("quote is stale or in the future")
	}
	return nil
}

type Candle struct {
	Time   time.Time `json:"open_time"`
	Open   float64   `json:"open"`
	High   float64   `json:"high"`
	Low    float64   `json:"low"`
	Close  float64   `json:"close"`
	Volume float64   `json:"volume"`
}

type Signal struct {
	Symbol     string    `json:"symbol"`
	AsOf       time.Time `json:"as_of"`
	ExpiresAt  time.Time `json:"expires_at"`
	Source     string    `json:"source"`
	Conclusion string    `json:"conclusion"`
	Evidence   string    `json:"evidence"`
}

func (s Signal) Validate(symbol string, now time.Time) error {
	if s.Symbol != symbol || s.AsOf.IsZero() || s.AsOf.After(now) || !s.ExpiresAt.After(now) || !s.ExpiresAt.After(s.AsOf) || s.Source == "" || s.Conclusion == "" {
		return errors.New("external signal has wrong symbol, missing fields or expired timestamps")
	}
	return nil
}

type Decision struct {
	Action     string  `json:"action"`
	Allocation float64 `json:"allocation"`
	StopLoss   float64 `json:"stop_loss"`
	TakeProfit float64 `json:"take_profit"`
	Reason     string  `json:"reason"`
}
type Position struct {
	Quantity   float64   `json:"quantity"`
	Entry      float64   `json:"entry_usd"`
	EntryFee   float64   `json:"entry_fee_usd"`
	StopLoss   float64   `json:"stop_loss"`
	TakeProfit float64   `json:"take_profit"`
	OpenedAt   time.Time `json:"opened_at"`
}
type Fill struct {
	Action      string    `json:"action"`
	Time        time.Time `json:"time"`
	Price       float64   `json:"price_usd"`
	Quantity    float64   `json:"quantity"`
	Fee         float64   `json:"fee_usd"`
	RealizedPnL float64   `json:"realized_pnl_usd"`
	Reason      string    `json:"reason"`
}
type Event struct {
	ID           int       `json:"id"`
	Time         time.Time `json:"time"`
	Kind         string    `json:"kind"`
	Message      string    `json:"message"`
	Equity       float64   `json:"equity_usd"`
	Quote        *Quote    `json:"quote,omitempty"`
	Decision     *Decision `json:"decision,omitempty"`
	Fill         *Fill     `json:"fill,omitempty"`
	SystemPrompt string    `json:"system_prompt,omitempty"`
	UserPrompt   string    `json:"user_prompt,omitempty"`
	RawResponse  string    `json:"raw_response,omitempty"`
	Model        string    `json:"model,omitempty"`
}
type State struct {
	Version     int       `json:"version"`
	Config      Config    `json:"config"`
	Cash        float64   `json:"cash_usd"`
	Position    *Position `json:"position,omitempty"`
	RealizedPnL float64   `json:"realized_pnl_usd"`
	Fees        float64   `json:"fees_usd"`
	Equity      float64   `json:"equity_usd"`
	PeakEquity  float64   `json:"peak_equity_usd"`
	MaxDrawdown float64   `json:"max_drawdown"`
	Halted      bool      `json:"halted"`
	LastCandle  time.Time `json:"last_decision_candle"`
	LastQuote   *Quote    `json:"last_quote,omitempty"`
	Events      []Event   `json:"events"`
}

func newState(c Config) State {
	return State{Version: 1, Config: c, Cash: c.InitialCash, Equity: c.InitialCash, PeakEquity: c.InitialCash, Events: []Event{}}
}
func finite(v float64) bool   { return !math.IsNaN(v) && !math.IsInf(v, 0) }
func positive(v float64) bool { return finite(v) && v > 0 }
func cloneState(s State) State {
	b, _ := json.Marshal(s)
	var out State
	_ = json.Unmarshal(b, &out)
	return out
}

func (s *State) mark(q Quote) {
	s.LastQuote = &q
	s.Equity = s.Cash
	if s.Position != nil {
		s.Equity += s.Position.Quantity * q.Bid
	}
	s.PeakEquity = math.Max(s.PeakEquity, s.Equity)
	dd := (s.PeakEquity - s.Equity) / s.PeakEquity
	s.MaxDrawdown = math.Max(s.MaxDrawdown, dd)
	if dd >= s.Config.MaxDrawdown {
		s.Halted = true
	}
}
func (s *State) add(e Event) {
	e.ID = len(s.Events) + 1
	e.Equity = s.Equity
	s.Events = append(s.Events, e)
}

func (s *State) sell(q Quote, reason string) Fill {
	p := s.Position
	price := q.Bid * (1 - s.Config.SlippageBPS/10000)
	fee := price * p.Quantity * s.Config.FeeBPS / 10000
	pnl := (price-p.Entry)*p.Quantity - p.EntryFee - fee
	s.Cash += price*p.Quantity - fee
	s.RealizedPnL += pnl
	s.Fees += fee
	s.Position = nil
	s.mark(q)
	return Fill{"sell", q.Time, price, p.Quantity, fee, pnl, reason}
}

// Protect runs on every poll, independent of LLM availability. Gaps fill at
// the observed bid less slippage, not magically at the stop price.
func (s *State) Protect(q Quote, now time.Time) (*Fill, error) {
	if err := q.Validate(now); err != nil {
		return nil, err
	}
	if s.LastQuote != nil && q.Time.Before(s.LastQuote.Time) {
		return nil, errors.New("out-of-order quote")
	}
	s.mark(q)
	if s.Position == nil {
		return nil, nil
	}
	reason := ""
	if s.Halted {
		reason = "account_drawdown_limit"
	} else if q.Bid <= s.Position.StopLoss {
		reason = "stop_loss"
	} else if q.Bid >= s.Position.TakeProfit {
		reason = "take_profit"
	}
	if reason == "" {
		return nil, nil
	}
	f := s.sell(q, reason)
	return &f, nil
}

func (s *State) Apply(d Decision, q Quote, now time.Time) (*Fill, error) {
	if err := q.Validate(now); err != nil {
		return nil, err
	}
	if s.LastQuote != nil && q.Time.Before(s.LastQuote.Time) {
		return nil, errors.New("out-of-order execution quote")
	}
	if d.Reason == "" || len(d.Reason) > 4000 {
		return nil, errors.New("decision requires a concise reason")
	}
	for _, v := range []float64{d.Allocation, d.StopLoss, d.TakeProfit} {
		if !finite(v) {
			return nil, errors.New("non-finite decision")
		}
	}
	if d.Action == "hold" || d.Action == "wait" {
		s.mark(q)
		return nil, nil
	}
	if d.Action == "sell" {
		if s.Position == nil {
			return nil, errors.New("cannot sell without a position")
		}
		f := s.sell(q, d.Reason)
		return &f, nil
	}
	if d.Action != "buy" {
		return nil, errors.New("only buy/sell/hold/wait supported; no shorting or leverage")
	}
	if s.Halted {
		return nil, errors.New("account halted by drawdown limit")
	}
	if s.Position != nil {
		return nil, errors.New("already holding; pyramiding disabled")
	}
	if d.Allocation <= 0 || d.Allocation > s.Config.MaxAllocation {
		return nil, errors.New("allocation exceeds configured limit or is non-positive")
	}
	entry := q.Ask * (1 + s.Config.SlippageBPS/10000)
	if !positive(d.StopLoss) || d.StopLoss >= entry || d.TakeProfit <= entry {
		return nil, errors.New("require stop_loss < actual simulated entry < take_profit")
	}
	feeRate := s.Config.FeeBPS / 10000
	stopFill := d.StopLoss * (1 - s.Config.SlippageBPS/10000)
	targetFill := d.TakeProfit * (1 - s.Config.SlippageBPS/10000)
	riskPerUnit := entry*(1+feeRate) - stopFill*(1-feeRate)
	rewardPerUnit := targetFill*(1-feeRate) - entry*(1+feeRate)
	if rewardPerUnit/riskPerUnit < s.Config.MinRewardRisk {
		return nil, fmt.Errorf("net reward/risk %.2f below %.2f", rewardPerUnit/riskPerUnit, s.Config.MinRewardRisk)
	}
	qty := math.Min(s.Cash*d.Allocation/(entry*(1+feeRate)), s.Cash*s.Config.RiskFraction/riskPerUnit)
	qty = math.Floor(qty*1e8) / 1e8
	if !positive(qty) {
		return nil, errors.New("order rounds to zero")
	}
	fee := qty * entry * feeRate
	s.Cash -= qty*entry + fee
	s.Fees += fee
	s.Position = &Position{qty, entry, fee, d.StopLoss, d.TakeProfit, q.Time}
	s.mark(q)
	f := Fill{"buy", q.Time, entry, qty, fee, 0, d.Reason}
	return &f, nil
}
