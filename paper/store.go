package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"syscall"
)

// A single atomic document commits balances and the corresponding events
// together. OS locking prevents two paper runners from spending the same cash.
// Supported runtime: macOS/Linux.
type Ledger struct {
	mu    sync.RWMutex
	path  string
	lock  *os.File
	state State
}

func openLedger(path string, c Config) (*Ledger, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, errors.New("ledger already in use by another process")
	}
	l := &Ledger{path: path, lock: f, state: newState(c)}
	b, err := os.ReadFile(path)
	if err == nil {
		if err = json.Unmarshal(b, &l.state); err != nil {
			l.Close()
			return nil, fmt.Errorf("invalid ledger: %w", err)
		}
		if l.state.Version != 1 || l.state.Config != c {
			l.Close()
			return nil, errors.New("ledger mode/symbol/risk config differs; use another ledger path")
		}
		if err = validateState(l.state); err != nil {
			l.Close()
			return nil, err
		}
	} else if !os.IsNotExist(err) {
		l.Close()
		return nil, err
	}
	if err = l.Commit(l.state); err != nil {
		l.Close()
		return nil, err
	}
	return l, nil
}
func (l *Ledger) Close() {
	if l.lock != nil {
		syscall.Flock(int(l.lock.Fd()), syscall.LOCK_UN)
		l.lock.Close()
	}
}
func (l *Ledger) Snapshot() State { l.mu.RLock(); defer l.mu.RUnlock(); return cloneState(l.state) }
func validateState(s State) error {
	if err := s.Config.Validate(); err != nil {
		return err
	}
	if !finite(s.Cash) || s.Cash < -1e-6 || !finite(s.RealizedPnL) || !finite(s.Fees) || s.Fees < 0 || !finite(s.Equity) || s.Equity < 0 || !positive(s.PeakEquity) {
		return errors.New("invalid ledger balances")
	}
	expected := s.Config.InitialCash + s.RealizedPnL
	if p := s.Position; p != nil {
		if !positive(p.Quantity) || !positive(p.Entry) || !positive(p.StopLoss) || p.StopLoss >= p.Entry || p.TakeProfit <= p.Entry || !finite(p.TakeProfit) || !finite(p.EntryFee) || p.EntryFee < 0 {
			return errors.New("invalid persisted position")
		}
		expected -= p.Quantity*p.Entry + p.EntryFee
	}
	if diff := s.Cash - expected; diff > 1e-5 || diff < -1e-5 {
		return errors.New("ledger cash does not reconcile")
	}
	for i, e := range s.Events {
		if e.ID != i+1 {
			return errors.New("ledger event sequence is invalid")
		}
	}
	return nil
}
func (l *Ledger) Commit(s State) error {
	if err := validateState(s); err != nil {
		return err
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	f, err := os.CreateTemp(filepath.Dir(l.path), ".paper-*.tmp")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if _, err = f.Write(b); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err = os.Rename(name, l.path); err != nil {
		return err
	}
	// Once rename succeeds, publish the same state in memory. Return sync errors
	// to stop the runner rather than risk continuing on uncertain persistence.
	l.state = cloneState(s)
	dir, err := os.Open(filepath.Dir(l.path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
