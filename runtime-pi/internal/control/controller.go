// SPDX-License-Identifier: Apache-2.0
package control

import (
	"errors"
	"os"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

type ControllerSnapshot struct {
	BandC      float64
	MinChangeS int
	MaxOpen    int
}
type ControlMode string

const (
	ModeFixed   ControlMode = "fixed"   // manual target (also uses hysteresis)
	ModeProfile ControlMode = "profile" // profile sets TargetC, hysteresis applies
	ModeValve   ControlMode = "valve"   // manual valve (no hysteresis)
)

type Config struct {
	HTTP struct {
		Addr string `yaml:"addr"`
	} `yaml:"http"`
	Serial struct {
		Device string `yaml:"device"`
		Baud   int    `yaml:"baud"`
	} `yaml:"serial"`
	Fermenters []struct {
		ID      string  `yaml:"id"`
		Name    string  `yaml:"name"`
		TargetC float64 `yaml:"target_c"`
	} `yaml:"fermenters"`
	Control struct {
		BandC         float64 `yaml:"band_c"`
		MinChangeS    int     `yaml:"min_change_s"`
		MaxOpenValves int     `yaml:"max_open_valves"`
	} `yaml:"control"`
	Alarms struct {
		HighMarginC float64 `yaml:"high_margin_c"` // alarm if BeerC > TargetC + HighMarginC
		LowMarginC  float64 `yaml:"low_margin_c"`  // alarm if BeerC < TargetC - LowMarginC
		DebounceS   int     `yaml:"debounce_s"`    // seconds condition must persist before opening/clearing
	} `yaml:"alarms"`
}

func LoadConfig(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		b, err = os.ReadFile("config.phb.yaml")
		if err != nil {
			return nil, err
		}
	}
	var c Config
	if err := yaml.Unmarshal(b, &c); err != nil {
		return nil, err
	}
	if c.Serial.Baud == 0 {
		c.Serial.Baud = 115200
	}
	if c.HTTP.Addr == "" {
		c.HTTP.Addr = ":8080"
	}

	// NEW: alarm defaults
	if c.Alarms.HighMarginC == 0 {
		c.Alarms.HighMarginC = 1.5
	}
	if c.Alarms.LowMarginC == 0 {
		c.Alarms.LowMarginC = 1.5
	}
	if c.Alarms.DebounceS == 0 {
		c.Alarms.DebounceS = 30
	}

	return &c, nil
}

type Fermenter struct {
	ID, Name       string
	TargetC        float64
	BeerC          float64
	Valve          string // "open"/"closed"
	BandC          float64
	MinChange      time.Duration
	lastChange     time.Time
	lastUpdate     time.Time
	allowImmediate bool
	Mode           ControlMode
	OverrideUntil  time.Time
	Override       ValveOverride
}

type OnTargetChangeFunc func(id string, newT float64, name string)

type SystemState struct {
	mu             sync.Mutex
	FV             []*Fermenter
	MaxOpen        int
	OnTargetChange OnTargetChangeFunc
}

// Safe iteration for snapshots
func (s *SystemState) ForEach(fn func(*Fermenter)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, f := range s.FV {
		fn(f)
	}
}

func (s *SystemState) SetTarget(id string, t float64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, x := range s.FV {
		if x.ID == id {
			if x.TargetC != t {
				x.TargetC = t
				x.allowImmediate = true // allow immediate valve action once
			}
			if s.OnTargetChange != nil {
				s.OnTargetChange(x.ID, x.TargetC, x.Name)
			}
			return nil
		}
	}
	return errors.New("fermenter not found")
}

func NewState(cfg *Config) *SystemState {
	s := &SystemState{MaxOpen: cfg.Control.MaxOpenValves}
	for _, f := range cfg.Fermenters {
		s.FV = append(s.FV, &Fermenter{
			ID:         f.ID,
			Name:       f.Name,
			TargetC:    f.TargetC,
			Valve:      "closed",
			BandC:      cfg.Control.BandC,
			MinChange:  time.Duration(cfg.Control.MinChangeS) * time.Second,
			lastUpdate: time.Now(),
			Mode:       ModeFixed,
		})
	}
	return s
}

// Export(): include ageS to drive row-highlighting client-side
func (s *SystemState) Export() map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	fv := make([]map[string]any, 0, len(s.FV))
	now := time.Now()
	for _, x := range s.FV {
		var ov any
		if x.Override.State != "" && (x.Override.Until.IsZero() || now.Before(x.Override.Until)) {
			exp := int64(0)
			if !x.Override.Until.IsZero() {
				exp = x.Override.Until.Unix()
			}
			ov = map[string]any{"state": x.Override.State, "expires": exp}
		}

		ageS := int(now.Sub(x.lastUpdate).Seconds())
		if ageS < 0 {
			ageS = 0
		}

		m := x.Mode
		if m == "" {
			m = "auto"
		}

		fv = append(fv, map[string]any{
			"id":       x.ID,
			"name":     x.Name,
			"beerC":    x.BeerC,
			"targetC":  x.TargetC,
			"valve":    x.Valve,
			"ageS":     ageS,
			"mode":     m,
			"override": ov,
			"overrideUntilS": func() int64 {
				if x.OverrideUntil.IsZero() {
					return 0
				}
				return x.OverrideUntil.Unix()
			}(),
		})
	}
	return map[string]any{"fv": fv}
}

// Fermenter: you already have lastUpdate time.Time
// Add helper to mark updates:
func (s *SystemState) UpdateBeerTemp(id string, c float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, x := range s.FV {
		if x.ID == id {
			x.BeerC = c
			x.lastUpdate = time.Now()
			return
		}
	}
}

// ValveDriver lets the controller open/close a given fermenter's valve.
type ValveDriver interface {
	SetValve(fvID string, open bool) error
}

// NoopDriver is the default (simulator) when no Arduino is connected.
type NoopDriver struct{}

func (NoopDriver) SetValve(string, bool) error { return nil }

func RunController(fv *Fermenter, sys *SystemState, period time.Duration, drv ValveDriver) {
	tk := time.NewTicker(period)
	defer tk.Stop()

	const staleLimit = 2 * time.Minute

	for range tk.C {
		sys.mu.Lock()
		now := time.Now()

		// Expire timed override
		if fv.Override.State != "" && !fv.Override.Until.IsZero() && now.After(fv.Override.Until) {
			fv.Override = ValveOverride{}
		}

		// Fail-safe on stale sensor: close valve
		if !fv.lastUpdate.IsZero() && now.Sub(fv.lastUpdate) > staleLimit && fv.Valve != "closed" {
			sys.mu.Unlock()
			_ = drv.SetValve(fv.ID, false)
			sys.mu.Lock()
			fv.Valve = "closed"
			fv.lastChange = now
			fv.allowImmediate = false
			sys.mu.Unlock()
			continue
		}

		// Manual valve mode: honor override if set; else keep current state
		if fv.Mode == ModeValve {
			if st := fv.Override.State; st == "open" || st == "closed" {
				wantOpen := (st == "open")
				if (wantOpen && fv.Valve != "open") || (!wantOpen && fv.Valve != "closed") {
					sys.mu.Unlock()
					_ = drv.SetValve(fv.ID, wantOpen)
					sys.mu.Lock()
					if wantOpen {
						fv.Valve = "open"
					} else {
						fv.Valve = "closed"
					}
					fv.lastChange = now
					fv.allowImmediate = false
				}
			}
			sys.mu.Unlock()
			continue
		}

		// Global override (applies in Auto/Fixed/Profile)
		if fv.Override.State != "" {
			wantOpen := fv.Override.State == "open"
			if (wantOpen && fv.Valve != "open") || (!wantOpen && fv.Valve != "closed") {
				sys.mu.Unlock()
				_ = drv.SetValve(fv.ID, wantOpen)
				sys.mu.Lock()
				if wantOpen {
					fv.Valve = "open"
				} else {
					fv.Valve = "closed"
				}
				fv.lastChange = now
				fv.allowImmediate = false
			}
			sys.mu.Unlock()
			continue
		}

		// Hysteresis for Auto / Fixed / Profile (all use TargetC)
		beer := fv.BeerC
		needOpen := beer > fv.TargetC+fv.BandC
		needClose := beer < fv.TargetC-fv.BandC
		elapsed := now.Sub(fv.lastChange)

		// No global limit if MaxOpen <= 0
		openCount := 0
		for _, x := range sys.FV {
			if x.Valve == "open" {
				openCount++
			}
		}
		canOpenMore := (sys.MaxOpen <= 0) || (openCount < sys.MaxOpen)

		ready := fv.allowImmediate || (elapsed > fv.MinChange)

		var changeTo string
		if ready && needOpen && fv.Valve != "open" && canOpenMore {
			changeTo = "open"
		}
		if ready && needClose && fv.Valve != "closed" {
			changeTo = "closed"
		}

		if changeTo != "" {
			sys.mu.Unlock()
			if err := drv.SetValve(fv.ID, changeTo == "open"); err == nil {
				sys.mu.Lock()
				fv.Valve = changeTo
				fv.lastChange = now
				fv.allowImmediate = false
				sys.mu.Unlock()
				continue
			}
			sys.mu.Lock()
			// on IO error, keep state
		}

		// Simulation drift if Noop driver (handy on dev)
		if _, isNoop := drv.(NoopDriver); isNoop {
			if fv.Valve == "open" {
				fv.BeerC -= 0.05
			} else {
				fv.BeerC += 0.02
			}
			if fv.BeerC < -5 {
				fv.BeerC = -5
			}
			if fv.BeerC > 30 {
				fv.BeerC = 30
			}
		}

		sys.mu.Unlock()
	}
}

type ValveOverride struct {
	State string    // "open" or "closed"
	Until time.Time // zero => indefinite
}

func (s *SystemState) SetMode(id string, m ControlMode) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, x := range s.FV {
		if x.ID == id {
			x.Mode = m
			// If leaving valve mode and there was an indefinite override, clear it.
			if m != ModeValve && (x.Override.State != "" && x.Override.Until.IsZero()) {
				x.Override = ValveOverride{}
			}
			// allow controller to react promptly
			x.allowImmediate = true
			return nil
		}
	}
	return errors.New("fermenter not found")
}

func (s *SystemState) ForceValve(id string, state string, ttl time.Duration) error {
	if state != "open" && state != "closed" {
		return errors.New("bad state")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, x := range s.FV {
		if x.ID == id {
			until := time.Time{}
			if ttl > 0 {
				until = time.Now().Add(ttl)
			}
			x.Override = ValveOverride{State: state, Until: until}
			x.allowImmediate = true
			return nil
		}
	}
	return errors.New("fermenter not found")
}

func (s *SystemState) ClearOverride(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, x := range s.FV {
		if x.ID == id {
			x.Override = ValveOverride{}
			x.allowImmediate = true
			return
		}
	}
}

// Sets an override with a specific until time.
// state should be "open" or "closed". until.IsZero() => indefinite.
func (s *SystemState) SetValveOverrideUntil(id, state string, until time.Time) error {
	if state != "open" && state != "closed" {
		return errors.New("invalid override state")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, x := range s.FV {
		if x.ID == id {
			x.Override = ValveOverride{State: state, Until: until}
			x.allowImmediate = true
			return nil
		}
	}
	return errors.New("fermenter not found")
}

// Convenience: set override for a duration from now.
func (s *SystemState) SetValveOverrideFor(id, state string, d time.Duration) error {
	return s.SetValveOverrideUntil(id, state, time.Now().Add(d))
}

// Convenience: set an indefinite override (no expiry).
func (s *SystemState) SetValveOverride(id, state string) error {
	return s.SetValveOverrideUntil(id, state, time.Time{})
}

// Setters you can call from the API
func (s *SystemState) ApplyControllerSettings(bandC float64, minChangeS int, maxOpen int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if bandC > 0 {
		for _, x := range s.FV {
			x.BandC = bandC
		}
	}
	if minChangeS >= 0 {
		d := time.Duration(minChangeS) * time.Second
		for _, x := range s.FV {
			x.MinChange = d
		}
	}
	// 0 means unlimited
	if maxOpen >= 0 {
		s.MaxOpen = maxOpen
	}
}

// SnapshotControllerSettings returns the current controller settings safely.
func (s *SystemState) SnapshotControllerSettings() ControllerSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	snap := ControllerSnapshot{MaxOpen: s.MaxOpen}
	if len(s.FV) > 0 {
		snap.BandC = s.FV[0].BandC
		snap.MinChangeS = int(s.FV[0].MinChange / time.Second)
	}
	return snap
}
