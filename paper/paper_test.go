package main

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var testTime = time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)

func quote(price float64) Quote { return Quote{testTime, price, price + .02} }
func buy() Decision             { return Decision{"buy", .2, 98, 110, "test evidence"} }
func near(t *testing.T, a, b float64) {
	t.Helper()
	if math.Abs(a-b) > 1e-6 {
		t.Fatalf("got %.10f want %.10f", a, b)
	}
}

func TestRoundTripAccounting(t *testing.T) {
	s := newState(defaultConfig("demo", "BTC-USD"))
	f, err := s.Apply(buy(), quote(100), testTime)
	if err != nil {
		t.Fatal(err)
	}
	if f.Quantity <= 0 || f.Price <= 100.02 || s.Cash >= 10000 {
		t.Fatal("buy did not account for ask/slippage")
	}
	entry := *f
	near(t, s.Cash, 10000-entry.Quantity*entry.Price-entry.Fee)
	f, err = s.Apply(Decision{Action: "sell", Reason: "exit"}, quote(104), testTime)
	if err != nil {
		t.Fatal(err)
	}
	near(t, s.Cash, 10000+s.RealizedPnL)
	near(t, s.Fees, entry.Fee+f.Fee)
	near(t, f.RealizedPnL, (f.Price-entry.Price)*entry.Quantity-entry.Fee-f.Fee)
	if s.Position != nil {
		t.Fatal("position not closed")
	}
	if err = validateState(s); err != nil {
		t.Fatal(err)
	}
}
func TestRiskBudgetIncludesFeesAndStopSlippage(t *testing.T) {
	c := defaultConfig("demo", "BTC-USD")
	c.RiskFraction = .001
	s := newState(c)
	f, err := s.Apply(buy(), quote(100), testTime)
	if err != nil {
		t.Fatal(err)
	}
	stop := 98 * (1 - c.SlippageBPS/10000)
	loss := f.Quantity * (f.Price*(1+c.FeeBPS/10000) - stop*(1-c.FeeBPS/10000))
	if loss > c.InitialCash*c.RiskFraction+1e-6 {
		t.Fatalf("risk exceeded: %v", loss)
	}
}
func TestRewardRiskUsesActualEntry(t *testing.T) {
	s := newState(defaultConfig("demo", "BTC-USD"))
	d := buy()
	d.StopLoss = 90
	d.TakeProfit = 105
	if _, err := s.Apply(d, quote(100), testTime); err == nil {
		t.Fatal("bad reward/risk accepted")
	}
	near(t, s.Cash, 10000)
}
func TestRejectedActionsDoNotSpendCash(t *testing.T) {
	for _, d := range []Decision{{Action: "open_short", Reason: "x"}, {Action: "sell", Reason: "x"}, {Action: "buy", Allocation: .9, StopLoss: 98, TakeProfit: 110, Reason: "x"}, {Action: "buy", Allocation: .2, StopLoss: 101, TakeProfit: 110, Reason: "x"}, {Action: "buy", Allocation: math.NaN(), Reason: "x"}} {
		s := newState(defaultConfig("demo", "BTC-USD"))
		if _, err := s.Apply(d, quote(100), testTime); err == nil {
			t.Fatalf("accepted %+v", d)
		}
		near(t, s.Cash, 10000)
	}
}
func TestNoPyramiding(t *testing.T) {
	s := newState(defaultConfig("demo", "BTC-USD"))
	s.Apply(buy(), quote(100), testTime)
	cash := s.Cash
	if _, err := s.Apply(buy(), quote(100), testTime); err == nil {
		t.Fatal("duplicate buy")
	}
	near(t, s.Cash, cash)
}
func TestGapStopFillsAtObservedPrice(t *testing.T) {
	s := newState(defaultConfig("demo", "BTC-USD"))
	s.Apply(buy(), quote(100), testTime)
	f, err := s.Protect(quote(90), testTime)
	if err != nil || f == nil {
		t.Fatalf("%v %v", f, err)
	}
	near(t, f.Price, 90*(1-s.Config.SlippageBPS/10000))
	if f.Reason != "stop_loss" {
		t.Fatal(f.Reason)
	}
}
func TestTakeProfitAndDrawdown(t *testing.T) {
	for _, tc := range []struct {
		p, limit float64
		reason   string
	}{{111, .1, "take_profit"}, {90, .001, "account_drawdown_limit"}} {
		c := defaultConfig("demo", "BTC-USD")
		c.MaxDrawdown = tc.limit
		s := newState(c)
		s.Apply(buy(), quote(100), testTime)
		f, err := s.Protect(quote(tc.p), testTime)
		if err != nil || f == nil || f.Reason != tc.reason {
			t.Fatalf("%+v %v", f, err)
		}
		if tc.reason == "account_drawdown_limit" && !s.Halted {
			t.Fatal("not halted")
		}
	}
}
func TestStaleAndFutureQuotesRejected(t *testing.T) {
	for _, dt := range []time.Duration{-time.Minute, 10 * time.Second} {
		q := quote(100)
		q.Time = q.Time.Add(dt)
		s := newState(defaultConfig("demo", "BTC-USD"))
		if _, err := s.Apply(buy(), q, testTime); err == nil {
			t.Fatal("invalid time accepted")
		}
	}
}
func TestLedgerResumeLockAndMismatch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ledger.json")
	c := defaultConfig("demo", "BTC-USD")
	l, err := openLedger(path, c)
	if err != nil {
		t.Fatal(err)
	}
	if other, e := openLedger(path, c); e == nil {
		other.Close()
		t.Fatal("concurrent writer allowed")
	}
	s := l.Snapshot()
	s.Apply(buy(), quote(100), testTime)
	if err = l.Commit(s); err != nil {
		t.Fatal(err)
	}
	l.Close()
	l, err = openLedger(path, c)
	if err != nil {
		t.Fatal(err)
	}
	if l.Snapshot().Position == nil {
		t.Fatal("lost position")
	}
	l.Close()
	c.Mode = "live"
	if other, e := openLedger(path, c); e == nil {
		other.Close()
		t.Fatal("demo/live ledger mixing allowed")
	}
}
func TestCorruptLedgerNotReset(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ledger.json")
	os.WriteFile(path, []byte(`{"cash_usd":999}`), 0600)
	if l, e := openLedger(path, defaultConfig("demo", "BTC-USD")); e == nil {
		l.Close()
		t.Fatal("corruption silently reset")
	}
}
func TestFailedCommitDoesNotPublish(t *testing.T) {
	l, _ := openLedger(filepath.Join(t.TempDir(), "ledger.json"), defaultConfig("demo", "BTC-USD"))
	defer l.Close()
	s := l.Snapshot()
	s.Cash = 123
	if e := l.Commit(s); e == nil {
		t.Fatal("bad accounting accepted")
	}
	near(t, l.Snapshot().Cash, 10000)
}

func TestClosedCandlesExcludeFutureAndRequireContinuity(t *testing.T) {
	d := Demo{Now: testTime}
	rows, _ := d.Candles(context.Background(), "BTC-USD", 300)
	rows = append(rows, Candle{testTime, 100, 101, 99, 100, 1})
	valid, err := closedCandles(rows, 300, testTime)
	if err != nil || len(valid) != 30 {
		t.Fatalf("len %d: %v", len(valid), err)
	}
	rows = append(rows[:10], rows[11:]...)
	if _, err = closedCandles(rows, 300, testTime); err == nil {
		t.Fatal("gap accepted")
	}
}
func TestExternalSignalExpiryAndObject(t *testing.T) {
	s := Signal{"BTC-USD", testTime.Add(-time.Minute), testTime.Add(time.Minute), "manual test", "wait", "test"}
	if e := s.Validate("BTC-USD", testTime); e != nil {
		t.Fatal(e)
	}
	if s.Validate("ETH-USD", testTime) == nil || s.Validate("BTC-USD", testTime.Add(2*time.Minute)) == nil {
		t.Fatal("invalid signal accepted")
	}
}

type countModel struct {
	count int
	err   error
}

func (m *countModel) Decide(_ context.Context, _, _ string) (ModelResult, error) {
	m.count++
	return ModelResult{Decision: buy(), Model: "test"}, m.err
}
func setupRunner(t *testing.T) (Runner, *Demo, *countModel) {
	t.Helper()
	l, err := openLedger(filepath.Join(t.TempDir(), "ledger.json"), defaultConfig("demo", "BTC-USD"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(l.Close)
	d := &Demo{Now: testTime}
	m := &countModel{}
	return Runner{Ledger: l, Market: d, Model: m, Now: func() time.Time { return d.Now }}, d, m
}
func TestRunnerDeduplicatesCandleAcrossRestart(t *testing.T) {
	r, _, m := setupRunner(t)
	if e := r.Tick(context.Background()); e != nil {
		t.Fatal(e)
	}
	if e := r.Tick(context.Background()); e != nil {
		t.Fatal(e)
	}
	if m.count != 1 {
		t.Fatal(m.count)
	}
	s := r.Ledger.Snapshot()
	if s.Position == nil {
		t.Fatal("missing fill")
	}
	path := r.Ledger.path
	r.Ledger.Close()
	l, e := openLedger(path, s.Config)
	if e != nil {
		t.Fatal(e)
	}
	defer l.Close()
	r.Ledger = l
	r.Tick(context.Background())
	if m.count != 1 {
		t.Fatal("restart replays decision")
	}
}
func TestModelFailureAndProtection(t *testing.T) {
	r, d, m := setupRunner(t)
	m.err = errors.New("unavailable")
	r.Tick(context.Background())
	if r.Ledger.Snapshot().Position != nil {
		t.Fatal("failed model traded")
	}
	if m.count != 1 {
		t.Fatal(m.count)
	}
	s := r.Ledger.Snapshot()
	s.Apply(buy(), quote(100), testTime)
	r.Ledger.Commit(s)
	d.Step = 4
	d.Now = d.Now.Add(5 * time.Minute)
	r.Tick(context.Background())
	s = r.Ledger.Snapshot()
	if s.Position != nil {
		t.Fatal("stop depends on model availability")
	}
	if m.count != 1 {
		t.Fatal("unnecessary model call after stop")
	}
}

func TestCompatibleAPIAndFullLoop(t *testing.T) {
	r, _, _ := setupRunner(t)
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		calls++
		if req.URL.Path != "/v1/chat/completions" || req.Header.Get("Authorization") != "Bearer test-only" {
			t.Error("incorrect request")
		}
		var body map[string]any
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if !strings.Contains(stringMustJSON(body), "completed_candles") {
			t.Error("missing context")
		}
		b, _ := json.Marshal(buy())
		json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": string(b)}}}})
	}))
	defer srv.Close()
	r.Model = CompatibleModel{srv.URL + "/v1", "test-only", "test-model", srv.Client()}
	if err := r.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	s := r.Ledger.Snapshot()
	if calls != 1 || s.Position == nil || s.Events[len(s.Events)-1].Fill == nil {
		t.Fatalf("loop incomplete %+v", s)
	}
	if s.Events[0].UserPrompt == "" || s.Events[len(s.Events)-1].RawResponse == "" {
		t.Fatal("audit context missing")
	}
}
func stringMustJSON(v any) string { b, _ := json.Marshal(v); return string(b) }
func TestModelRejectsMalformedOutputAndRedactsHTTPError(t *testing.T) {
	for _, raw := range []string{`not json`, `{"action":"buy","secret_field":1}`, `{"action":"wait","reason":"x"} extra`} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": raw}}}})
		}))
		m := CompatibleModel{srv.URL, "key", "model", srv.Client()}
		if _, e := m.Decide(context.Background(), "s", "u"); e == nil {
			t.Fatalf("accepted %s", raw)
		}
		srv.Close()
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(401); w.Write([]byte("secret-key")) }))
	defer srv.Close()
	m := CompatibleModel{srv.URL, "secret-key", "model", srv.Client()}
	_, e := m.Decide(context.Background(), "s", "u")
	if e == nil || strings.Contains(e.Error(), "secret-key") {
		t.Fatal("error leaked or missing")
	}
}
func TestHTTPMarketParsing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "ticker") {
			json.NewEncoder(w).Encode(map[string]any{"time": testTime, "bid": "100", "ask": "100.02"})
		} else {
			json.NewEncoder(w).Encode([][]float64{{float64(testTime.Unix()), 99, 102, 100, 101, 12}})
		}
	}))
	defer srv.Close()
	m := MarketHTTP{srv.URL, srv.Client()}
	q, e := m.Quote(context.Background(), "BTC-USD")
	if e != nil || q.Bid != 100 {
		t.Fatal(q, e)
	}
	cs, e := m.Candles(context.Background(), "BTC-USD", 300)
	if e != nil || cs[0].Open != 100 || cs[0].Low != 99 || cs[0].Close != 101 {
		t.Fatal(cs, e)
	}
}
func TestDashboardReadOnly(t *testing.T) {
	r, _, _ := setupRunner(t)
	h := handler(r.Ledger)
	for _, path := range []string{"/api/state", "/health"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != 200 {
			t.Fatal(path, w.Code)
		}
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("POST", "/api/state", nil))
	if w.Code != 405 {
		t.Fatal("dashboard allows writes")
	}
}

type movingMarket struct {
	demo     *Demo
	calls    int
	endPrice float64
}

func (m *movingMarket) Quote(_ context.Context, _ string) (Quote, error) {
	m.calls++
	p := 100.0
	if m.calls > 1 {
		p = m.endPrice
	}
	return Quote{m.demo.Now, p, p + .02}, nil
}
func (m *movingMarket) Candles(ctx context.Context, s string, n int) ([]Candle, error) {
	return m.demo.Candles(ctx, s, n)
}
func TestFreshExecutionPriceCanRejectOldPlan(t *testing.T) {
	r, d, _ := setupRunner(t)
	r.Market = &movingMarket{demo: d, endPrice: 109}
	if err := r.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	s := r.Ledger.Snapshot()
	if s.Position != nil {
		t.Fatal("old entry used despite price move")
	}
	if s.Events[len(s.Events)-1].Kind != "rejected" {
		t.Fatal("missing rejection record")
	}
}

type slowModel struct{ demo *Demo }

func (m slowModel) Decide(_ context.Context, _, _ string) (ModelResult, error) {
	m.demo.Now = m.demo.Now.Add(3 * time.Minute)
	return ModelResult{Decision: buy(), Model: "slow-test"}, nil
}
func TestSlowDecisionExpires(t *testing.T) {
	r, d, _ := setupRunner(t)
	r.Model = slowModel{d}
	r.Tick(context.Background())
	s := r.Ledger.Snapshot()
	if s.Position != nil || s.Events[len(s.Events)-1].Message != "decision context expired" {
		t.Fatal("expired decision accepted")
	}
}
func TestPromptPreservesUnitsAndDoesNotNestAudit(t *testing.T) {
	s := newState(defaultConfig("demo", "BTC-USD"))
	s.Events = []Event{{ID: 1, UserPrompt: "DO_NOT_RECURSE"}}
	d := Demo{Now: testTime}
	rows, _ := d.Candles(context.Background(), "BTC-USD", 300)
	p := userPrompt(s, quote(100), rows, nil)
	if strings.Contains(p, "DO_NOT_RECURSE") || !strings.Contains(p, "mean_high_low_range20") || !strings.Contains(p, "USD") {
		t.Fatal("incorrect prompt context")
	}
}
func TestPersistedDrawdownHaltBlocksOpening(t *testing.T) {
	r, _, _ := setupRunner(t)
	s := r.Ledger.Snapshot()
	s.Halted = true
	r.Ledger.Commit(s)
	if _, err := s.Apply(buy(), quote(100), testTime); err == nil {
		t.Fatal("halt ignored")
	}
	r.Tick(context.Background())
	if r.Ledger.Snapshot().Position != nil {
		t.Fatal("runner ignored halt")
	}
}

func TestRootRedirectsToOriginalFrontend(t *testing.T) {
	r, _, _ := setupRunner(t)
	w := httptest.NewRecorder()
	handler(r.Ledger).ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
	if w.Code != 307 || w.Header().Get("Location") != "http://127.0.0.1:3000/paper" {
		t.Fatal("expected NOFX frontend redirect")
	}
}
