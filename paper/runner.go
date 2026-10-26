package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"time"
)

type Runner struct {
	Ledger     *Ledger
	Market     Market
	Model      Model
	SignalPath string
	Now        func() time.Time
}

func (r Runner) recordFailure(s State, stage string, err error) error {
	s.add(Event{Time: r.Now(), Kind: "error", Message: stage + ": " + err.Error()})
	return r.Ledger.Commit(s)
}
func (r Runner) Tick(ctx context.Context) error {
	s := r.Ledger.Snapshot()
	q, err := r.Market.Quote(ctx, s.Config.Symbol)
	if err != nil {
		return r.recordFailure(s, "quote", err)
	}
	f, err := s.Protect(q, r.Now())
	if err != nil {
		return r.recordFailure(s, "quote validation", err)
	}
	if f != nil {
		s.add(Event{Time: r.Now(), Kind: "protection", Message: f.Reason, Quote: &q, Fill: f})
		return r.Ledger.Commit(s) // never exit and re-enter on the same poll
	}
	if err = r.Ledger.Commit(s); err != nil {
		return err
	}
	if s.Halted {
		return nil
	}
	rows, err := r.Market.Candles(ctx, s.Config.Symbol, s.Config.Granularity)
	if err != nil {
		return r.recordFailure(s, "candles", err)
	}
	rows, err = closedCandles(rows, s.Config.Granularity, r.Now())
	if err != nil {
		return r.recordFailure(s, "candle validation", err)
	}
	bar := rows[len(rows)-1].Time
	if !bar.After(s.LastCandle) {
		return nil
	}
	var signal *Signal
	if r.SignalPath != "" {
		b, e := os.ReadFile(r.SignalPath)
		if e != nil {
			return r.recordFailure(s, "external signal", errors.New("cannot read signal file"))
		}
		if len(b) > 32768 {
			return r.recordFailure(s, "external signal", errors.New("signal file too large"))
		}
		signal = &Signal{}
		if e = json.Unmarshal(b, signal); e != nil {
			return r.recordFailure(s, "external signal", errors.New("invalid signal JSON"))
		}
		if e = signal.Validate(s.Config.Symbol, r.Now()); e != nil {
			return r.recordFailure(s, "external signal", e)
		}
	}
	user := userPrompt(s, q, rows, signal)
	// Persist the reservation BEFORE the model request. A restart never repeats
	// a decision on this candle. Failed requests wait for the next closed bar.
	s.LastCandle = bar
	s.add(Event{Time: r.Now(), Kind: "decision_requested", Message: "new completed candle; paper only", Quote: &q, SystemPrompt: systemPrompt, UserPrompt: user})
	if err = r.Ledger.Commit(s); err != nil {
		return err
	}
	result, modelErr := r.Model.Decide(ctx, systemPrompt, user)
	// Always refresh the quote and process protection even if the model failed.
	execQuote, quoteErr := r.Market.Quote(ctx, s.Config.Symbol)
	e := Event{Time: r.Now(), Kind: "decision", Model: result.Model, RawResponse: result.Raw, Decision: &result.Decision}
	if quoteErr == nil {
		e.Quote = &execQuote
		f, quoteErr = s.Protect(execQuote, r.Now())
	}
	if f != nil {
		e.Kind = "protection"
		e.Fill = f
		e.Message = f.Reason + "; AI action discarded"
	} else if quoteErr != nil {
		e.Kind = "rejected"
		e.Message = "execution quote unavailable or invalid: " + quoteErr.Error()
	} else if modelErr != nil {
		e.Kind = "model_error"
		e.Message = modelErr.Error()
	} else if signal != nil && signal.Validate(s.Config.Symbol, r.Now()) != nil {
		e.Kind = "rejected"
		e.Message = "external signal expired during model call"
	} else if r.Now().Sub(q.Time) > 2*time.Minute {
		e.Kind = "rejected"
		e.Message = "decision context expired"
	} else {
		e.Fill, err = s.Apply(result.Decision, execQuote, r.Now())
		if err != nil {
			e.Kind = "rejected"
			e.Message = err.Error()
		} else {
			e.Message = result.Decision.Reason
		}
	}
	s.add(e)
	return r.Ledger.Commit(s)
}

// Demo is a deterministic synthetic fixture, never presented as AI or market
// performance. It uses the exact same accounting, risk and persistence path.
type Demo struct {
	Now  time.Time
	Step int
}

func (d *Demo) Quote(_ context.Context, _ string) (Quote, error) {
	prices := []float64{100, 102, 104, 101, 98, 100}
	p := prices[d.Step%len(prices)]
	return Quote{d.Now, p, p + .02}, nil
}
func (d *Demo) Candles(_ context.Context, _ string, seconds int) ([]Candle, error) {
	end := d.Now.Truncate(time.Duration(seconds) * time.Second)
	rows := []Candle{}
	for i := 30; i > 0; i-- {
		p := 98 + float64(30-i)*.05
		rows = append(rows, Candle{end.Add(-time.Duration(i*seconds) * time.Second), p, p + .2, p - .2, p + .1, 100})
	}
	return rows, nil
}
func (d *Demo) Decide(_ context.Context, _, _ string) (ModelResult, error) {
	decision := Decision{Action: "wait", Reason: "离线演示：固定脚本，不是 AI 决策。"}
	if d.Step%6 == 0 {
		decision = Decision{"buy", .2, 98, 110, "离线演示：脚本买入，验证仓位和成本。"}
	}
	if d.Step%6 == 1 {
		decision.Action = "hold"
	}
	if d.Step%6 == 2 {
		decision.Action = "sell"
		decision.Reason = "离线演示：脚本卖出，验证已实现盈亏。"
	}
	b, _ := json.Marshal(decision)
	return ModelResult{decision, string(b), "DEMO_SCRIPT_NOT_AI"}, nil
}
