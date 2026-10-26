package main

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

var symbolPattern = regexp.MustCompile(`^[A-Z0-9]{2,12}-USD$`)

type Market interface {
	Quote(context.Context, string) (Quote, error)
	Candles(context.Context, string, int) ([]Candle, error)
}
type MarketHTTP struct {
	Base   string
	Client *http.Client
}

func (m MarketHTTP) get(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, "GET", m.Base+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "Xuanhua-Paper/0.1")
	resp, err := m.Client.Do(req)
	if err != nil {
		return errors.New("market request failed (connection or timeout)")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("market HTTP %d", resp.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(out)
}
func (m MarketHTTP) Quote(ctx context.Context, symbol string) (Quote, error) {
	var r struct {
		Time time.Time `json:"time"`
		Bid  string    `json:"bid"`
		Ask  string    `json:"ask"`
	}
	if err := m.get(ctx, "/products/"+url.PathEscape(symbol)+"/ticker", &r); err != nil {
		return Quote{}, err
	}
	b, err := strconv.ParseFloat(r.Bid, 64)
	if err != nil {
		return Quote{}, errors.New("invalid bid")
	}
	a, err := strconv.ParseFloat(r.Ask, 64)
	if err != nil {
		return Quote{}, errors.New("invalid ask")
	}
	return Quote{r.Time, b, a}, nil
}
func (m MarketHTTP) Candles(ctx context.Context, symbol string, seconds int) ([]Candle, error) {
	var rows [][]float64
	if err := m.get(ctx, fmt.Sprintf("/products/%s/candles?granularity=%d", url.PathEscape(symbol), seconds), &rows); err != nil {
		return nil, err
	}
	var out []Candle
	for _, r := range rows {
		if len(r) != 6 {
			return nil, errors.New("unexpected candle schema")
		}
		for _, v := range r {
			if !finite(v) {
				return nil, errors.New("invalid candle number")
			}
		}
		out = append(out, Candle{time.Unix(int64(r[0]), 0).UTC(), r[3], r[2], r[1], r[4], r[5]})
	}
	return out, nil
}

func closedCandles(rows []Candle, seconds int, now time.Time) ([]Candle, error) {
	rows = append([]Candle(nil), rows...)
	sort.Slice(rows, func(i, j int) bool { return rows[i].Time.Before(rows[j].Time) })
	out := []Candle{}
	for _, r := range rows {
		if r.Time.Add(time.Duration(seconds) * time.Second).After(now) {
			continue
		}
		if r.Time.IsZero() || !positive(r.Open) || !positive(r.High) || !positive(r.Low) || !positive(r.Close) || !finite(r.Volume) || r.Volume < 0 || r.Low > r.Open || r.Low > r.Close || r.High < r.Open || r.High < r.Close {
			return nil, errors.New("invalid OHLCV data")
		}
		if len(out) > 0 && !r.Time.After(out[len(out)-1].Time) {
			return nil, errors.New("duplicate candle")
		}
		out = append(out, r)
	}
	if len(out) < 20 {
		return nil, errors.New("need at least 20 completed candles")
	}
	last := out[len(out)-1].Time
	if now.Sub(last.Add(time.Duration(seconds)*time.Second)) > time.Duration(seconds)*time.Second {
		return nil, errors.New("completed candles are stale")
	}
	if len(out) > 60 {
		out = out[len(out)-60:]
	}
	for i := 1; i < len(out); i++ {
		if out[i].Time.Sub(out[i-1].Time) != time.Duration(seconds)*time.Second {
			return nil, errors.New("candle history has gaps")
		}
	}
	return out, nil
}

//go:embed prompt.txt
var systemPrompt string

func userPrompt(s State, q Quote, candles []Candle, signal *Signal) string {
	var sma, rangeSum float64
	for _, c := range candles[len(candles)-20:] {
		sma += c.Close
		rangeSum += c.High - c.Low
	}
	recent := s.Events
	if len(recent) > 5 {
		recent = recent[len(recent)-5:]
	}
	// Do not recursively feed previous prompts into the next prompt.
	summary := []map[string]any{}
	for _, e := range recent {
		summary = append(summary, map[string]any{"time": e.Time, "kind": e.Kind, "decision": e.Decision, "fill": e.Fill, "message": e.Message})
	}
	input := map[string]any{
		"mode": "PAPER_ONLY", "symbol": s.Config.Symbol, "market_source": s.Config.Mode,
		"quote": q, "completed_candles": candles, "sma20": sma / 20, "mean_high_low_range20": rangeSum / 20,
		"units":   "Prices and cash in USD; quantity in base asset; allocation is fraction of equity, not percent or leverage",
		"account": map[string]any{"cash_usd": s.Cash, "equity_usd": s.Equity, "position": s.Position, "realized_pnl_usd": s.RealizedPnL, "fees_usd": s.Fees, "halted": s.Halted},
		"limits":  s.Config, "recent_events": summary, "external_signal_untrusted": signal,
	}
	b, _ := json.MarshalIndent(input, "", "  ")
	return string(b)
}

type ModelResult struct {
	Decision Decision
	Raw      string
	Model    string
}
type Model interface {
	Decide(context.Context, string, string) (ModelResult, error)
}
type CompatibleModel struct {
	Base, Key, Name string
	Client          *http.Client
}

func (m CompatibleModel) Validate() error {
	u, err := url.Parse(m.Base)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("invalid model base URL")
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "127.0.0.1" || u.Hostname() == "localhost" || u.Hostname() == "::1")) {
		return errors.New("model URL must use HTTPS (HTTP allowed only on localhost)")
	}
	if m.Key == "" || m.Name == "" {
		return errors.New("set XUANHUA_API_KEY, XUANHUA_BASE_URL and XUANHUA_MODEL for live AI mode")
	}
	return nil
}
func (m CompatibleModel) Decide(ctx context.Context, system, user string) (ModelResult, error) {
	r := ModelResult{Model: m.Name}
	if err := m.Validate(); err != nil {
		return r, err
	}
	body, _ := json.Marshal(map[string]any{"model": m.Name, "messages": []map[string]string{{"role": "system", "content": system}, {"role": "user", "content": user}}, "stream": false, "response_format": map[string]string{"type": "json_object"}})
	req, err := http.NewRequestWithContext(ctx, "POST", strings.TrimRight(m.Base, "/")+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return r, errors.New("invalid model request")
	}
	req.Header.Set("Authorization", "Bearer "+m.Key)
	req.Header.Set("Content-Type", "application/json")
	resp, err := m.Client.Do(req)
	if err != nil {
		return r, errors.New("model request failed (connection or timeout)")
	}
	defer resp.Body.Close()
	// Do not echo provider error bodies, which may include credentials or headers.
	if resp.StatusCode != 200 {
		return r, fmt.Errorf("model HTTP %d; check endpoint, model, credentials and JSON-mode support", resp.StatusCode)
	}
	var data struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&data); err != nil {
		return r, errors.New("invalid model response envelope")
	}
	if len(data.Choices) != 1 {
		return r, errors.New("expected one model choice")
	}
	r.Raw = data.Choices[0].Message.Content
	dec := json.NewDecoder(strings.NewReader(r.Raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&r.Decision); err != nil {
		return r, errors.New("model decision is not valid strict JSON")
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return r, errors.New("model returned trailing content")
	}
	return r, nil
}
