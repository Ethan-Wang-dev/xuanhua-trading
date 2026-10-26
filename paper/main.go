package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
)

func handler(l *Ledger) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if r.Method != "GET" {
			w.WriteHeader(405)
			return
		}
		switch r.URL.Path {
		case "/":
			http.Redirect(w, r, "http://127.0.0.1:3000/paper", http.StatusTemporaryRedirect)
		case "/api/state":
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(l.Snapshot())
		case "/health":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"service":"xuanhua-paper","execution":"simulation-only"}`)
		default:
			http.NotFound(w, r)
		}
	})
	return mux
}

func run() error {
	mode := flag.String("mode", "demo", "demo (synthetic/scripted) or live (real market + AI, still virtual money)")
	symbol := flag.String("symbol", "BTC-USD", "one USD spot pair")
	path := flag.String("ledger", "", "ledger path; defaults to data/paper-MODE.json")
	listen := flag.String("listen", "127.0.0.1:8787", "read-only local dashboard; empty disables")
	interval := flag.Duration("interval", 15*time.Second, "quote/protection polling interval; decisions only on new completed candles")
	cycles := flag.Int("cycles", 0, "number of polls then exit; 0 runs until interrupted")
	granularity := flag.Int("candle-seconds", 300, "completed candle length: 60/300/900/3600; not solar-time scheduling")
	signalPath := flag.String("signal", "", "optional external interpretation JSON file")
	checkMarket := flag.Bool("check-market", false, "check public real-market data without calling a model or creating a ledger")
	flag.Parse()
	if *interval < time.Second || *cycles < 0 {
		return errors.New("interval must be >=1s, cycles must be >=0")
	}
	c := defaultConfig(*mode, *symbol)
	c.Granularity = *granularity
	if err := c.Validate(); err != nil {
		return err
	}
	if *checkMarket {
		m := MarketHTTP{"https://api.exchange.coinbase.com", &http.Client{Timeout: 15 * time.Second}}
		q, err := m.Quote(context.Background(), c.Symbol)
		if err != nil {
			return err
		}
		if err = q.Validate(time.Now().UTC()); err != nil {
			return err
		}
		rows, err := m.Candles(context.Background(), c.Symbol, c.Granularity)
		if err != nil {
			return err
		}
		rows, err = closedCandles(rows, c.Granularity, time.Now().UTC())
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(map[string]any{"source": "coinbase-public", "symbol": c.Symbol, "quote": q, "completed_candles": len(rows), "last_completed_candle": rows[len(rows)-1]})
	}
	client := &http.Client{Timeout: 60 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return errors.New("redirects disabled") }}
	var market Market
	var model Model
	var demo *Demo
	now := func() time.Time { return time.Now().UTC() }
	if *mode == "live" {
		m := CompatibleModel{Base: os.Getenv("XUANHUA_BASE_URL"), Key: os.Getenv("XUANHUA_API_KEY"), Name: os.Getenv("XUANHUA_MODEL"), Client: client}
		if err := m.Validate(); err != nil {
			return err
		}
		model = m
		market = MarketHTTP{"https://api.exchange.coinbase.com", &http.Client{Timeout: 15 * time.Second}}
	} else {
		demo = &Demo{Now: time.Now().UTC()}
		market = demo
		model = demo
		now = func() time.Time { return demo.Now }
	}
	if *path == "" {
		*path = filepath.Join("data", "paper-"+*mode+".json")
	}
	l, err := openLedger(*path, c)
	if err != nil {
		return err
	}
	defer l.Close()
	if demo != nil {
		s := l.Snapshot()
		demo.Step = int(len(s.Events) / 2)
		if !s.LastCandle.IsZero() {
			demo.Now = s.LastCandle.Add(time.Duration(2*c.Granularity) * time.Second)
		}
		if s.LastQuote != nil && demo.Now.Before(s.LastQuote.Time) {
			demo.Now = s.LastQuote.Time.Add(time.Duration(c.Granularity) * time.Second)
		}
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if *listen != "" {
		host, _, err := net.SplitHostPort(*listen)
		if err != nil || host != "127.0.0.1" {
			return errors.New("dashboard must bind to 127.0.0.1:PORT")
		}
		ln, err := net.Listen("tcp", *listen)
		if err != nil {
			return err
		}
		server := &http.Server{Handler: handler(l), ReadHeaderTimeout: 5 * time.Second, WriteTimeout: 10 * time.Second}
		defer server.Close()
		go func() {
			if e := server.Serve(ln); e != nil && e != http.ErrServerClosed {
				log.Printf("dashboard stopped: %v", e)
				cancel()
			}
		}()
		log.Printf("玄化交易模拟看板: http://%s", ln.Addr())
	}
	log.Printf("mode=%s; SIMULATED FUNDS ONLY; symbol=%s; ledger=%s", *mode, c.Symbol, *path)
	if demo != nil {
		log.Print("离线合成行情 + 固定脚本；不是 AI，不代表策略收益。")
	}
	r := Runner{l, market, model, *signalPath, now}
	for i := 0; *cycles == 0 || i < *cycles; i++ {
		if err := r.Tick(ctx); err != nil {
			return fmt.Errorf("ledger/runner stopped: %w", err)
		}
		s := l.Snapshot()
		status := "ready"
		if len(s.Events) > 0 {
			e := s.Events[len(s.Events)-1]
			status = e.Kind + ": " + e.Message
		}
		log.Printf("cash=%.2f equity=%.2f realized=%.2f fees=%.2f | %s", s.Cash, s.Equity, s.RealizedPnL, s.Fees, status)
		if *cycles > 0 && i+1 >= *cycles {
			break
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(*interval):
		}
		if demo != nil {
			demo.Step++
			demo.Now = demo.Now.Add(time.Duration(c.Granularity) * time.Second)
		}
	}
	return nil
}
func main() {
	if err := run(); err != nil {
		log.Print(err)
		os.Exit(1)
	}
}
