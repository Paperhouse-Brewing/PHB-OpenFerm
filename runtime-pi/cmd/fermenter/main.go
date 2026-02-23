// SPDX-License-Identifier: Apache-2.0
package main

import (
	"context"
	"encoding/json"
	"flag"
	"log"
	"math"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/coreos/go-systemd/v22/daemon"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"phb/fermenter-runtime/internal/alarm"
	"phb/fermenter-runtime/internal/api"
	"phb/fermenter-runtime/internal/arduino"
	"phb/fermenter-runtime/internal/control"
	"phb/fermenter-runtime/internal/sse"
	"phb/fermenter-runtime/internal/store"
)

func main() {
	// --- Config --------------------------------------------------------------
	cfgFlag := flag.String("config", "config.yaml", "path to config file")
	flag.Parse()
	cfgPath := os.Getenv("PHB_CONFIG")
	if cfgPath == "" {
		cfgPath = *cfgFlag
	}
	cfg, err := control.LoadConfig(cfgPath)
	if err != nil {
		log.Fatalf("config (%s): %v", cfgPath, err)
	}

	// --- Store + State + resume ---------------------------------------------
	const dbPath = "/var/lib/openbrew/phb.db"
	st, err := store.Open(dbPath)
	if err != nil {
		log.Fatalf("store open (%s): %v", dbPath, err)
	}
	defer st.Close()
	log.Printf("store: using %s", dbPath)

	state := control.NewState(cfg)

	// Resume saved targets on boot
	if err := st.ApplySavedTargets(state); err != nil {
		log.Printf("store: apply targets: %v", err)
	}
	if err := st.ApplySavedModes(state); err != nil {
		log.Printf("store: apply modes: %v", err)
	}
	// Persist future target changes
	state.OnTargetChange = func(id string, t float64, name string) {
		if err := st.SaveTarget(id, t, name); err != nil {
			log.Printf("store: save target %s: %v", id, err)
		} else {
			log.Printf("store: saved %s target=%.3f°C", id, t)
		}
	}
	// Create DB and an initial snapshot
	if err := st.Snapshot(state); err != nil {
		log.Printf("store: initial snapshot: %v", err)
	}

	// Settings
	if cs, err := st.LoadControllerSettings(); err == nil {
		// fall back to config values if not set in DB
		if cs.BandC == 0 {
			cs.BandC = cfg.Control.BandC
		}
		if cs.MinChangeS == 0 {
			cs.MinChangeS = cfg.Control.MinChangeS
		}
		if cs.MaxOpen == 0 {
			cs.MaxOpen = cfg.Control.MaxOpenValves
		} // 0 = unlimited is fine here too
		state.ApplyControllerSettings(cs.BandC, cs.MinChangeS, cs.MaxOpen)
	} else {
		log.Printf("store: load settings: %v", err)
	}

	// --- Alarms --------------------------------------------------------------
	al := alarm.NewManager(
		cfg.Alarms.HighMarginC,
		cfg.Alarms.LowMarginC,
		time.Duration(cfg.Alarms.DebounceS)*time.Second,
		func(typ, fvID, data string) { st.AddEvent(typ, fvID, data) },
	)
	go func() {
		tk := time.NewTicker(time.Second)
		defer tk.Stop()
		for range tk.C {
			al.Evaluate(state)
		}
	}()

	// --- Sampling / pruning --------------------------------------------------
	go func() {
		tk := time.NewTicker(10 * time.Second)
		defer tk.Stop()
		for range tk.C {
			if err := st.InsertSamples(state); err != nil {
				log.Printf("store: sample: %v", err)
			}
		}
	}()
	go func() {
		tk := time.NewTicker(24 * time.Hour)
		defer tk.Stop()
		for range tk.C {
			if err := st.PruneSamples(45 * 24 * time.Hour); err != nil {
				log.Printf("store: prune: %v", err)
			}
		}
	}()
	// Periodic snapshots for resume/history
	go func() {
		tk := time.NewTicker(30 * time.Second)
		defer tk.Stop()
		for range tk.C {
			_ = st.Snapshot(state)
		}
	}()

	// --- Profile scheduler (targets) ----------------------------------------
	go func() {
		tk := time.NewTicker(5 * time.Second)
		defer tk.Stop()
		for range tk.C {
			now := time.Now()
			for _, fv := range state.FV {
				if fv.Mode != control.ModeProfile {
					continue
				}

				a, err := st.GetAssignment(fv.ID)
				if err != nil || a == nil || a.Paused {
					continue
				}
				spec, err := st.GetProfile(a.ProfileID)
				if err != nil || len(spec.Steps) == 0 {
					continue
				}

				step := spec.Steps[a.StepIdx]
				elapsed := now.Unix() - a.StepStartedTs
				target := fv.TargetC
				advance := false

				switch strings.ToLower(step.Type) {
				case "hold":
					target = step.TargetC
					if step.DurationS > 0 && elapsed >= step.DurationS {
						advance = true
					}
				case "ramp":
					rate := step.RateCPerHour
					if rate <= 0 {
						rate = 0.25
					}
					sign := 1.0
					if step.TargetC < a.FromC {
						sign = -1.0
					}
					delta := float64(elapsed) / 3600.0 * rate * sign
					target = a.FromC + delta
					if (sign > 0 && target >= step.TargetC) || (sign < 0 && target <= step.TargetC) {
						target = step.TargetC
						advance = true
					}
				default:
					advance = true
				}

				if math.Abs(target-fv.TargetC) > 0.05 {
					_ = state.SetTarget(fv.ID, target)
				}
				if advance {
					a.StepIdx++
					if a.StepIdx >= len(spec.Steps) {
						_ = st.DeleteAssignment(fv.ID)
						continue
					}
					a.StepStartedTs = now.Unix()
					a.FromC = target
					_ = st.UpsertAssignment(*a)
				}
			}
		}
	}()

	// --- SSE hub -------------------------------------------------------------
	hub := sse.NewHub()
	go hub.Run()
	go func() {
		tk := time.NewTicker(time.Second)
		defer tk.Stop()
		for range tk.C {
			msg, _ := json.Marshal(state.Export())
			hub.Broadcast(msg)
		}
	}()

	// --- Hardware driver (Arduino) BEFORE controllers -----------------------
	var drv control.ValveDriver = control.NoopDriver{} // simulator by default
	var lastTelem atomic.Value                         // time.Time
	lastTelem.Store(time.Now())

	if cfg.Serial.Device != "" {
		dev, err := arduino.Open(cfg.Serial.Device, cfg.Serial.Baud)
		if err == nil {
			drv = dev
			go dev.Run(func(t arduino.Telemetry) {
				state.UpdateBeerTemp(t.FV, t.BeerC)
				lastTelem.Store(time.Now())
			})
			defer dev.Close()
		}
	}

	// Watchdog goroutine
	go func() {
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			lt, _ := lastTelem.Load().(time.Time)
			if time.Since(lt) > 30*time.Second { // threshold
				// Close all valves once and log an alarm
				state.ForEach(func(f *control.Fermenter) {
					_ = drv.SetValve(f.ID, false)
					f.Valve = "closed"
				})
				st.AddEvent("serial_timeout", "", "no telemetry >30s; all valves closed")
			}
		}
	}()

	// --- Controllers (one per FV) -------------------------------------------
	for i := range state.FV {
		fv := state.FV[i]
		go control.RunController(fv, state, time.Second, drv) // NOTE: signature with driver
	}

	// --- HTTP server ---------------------------------------------------------
	r := chi.NewRouter()
	r.Use(middleware.RequestID, middleware.RealIP, middleware.Logger, middleware.Recoverer)
	r.Get("/sse", hub.ServeHTTP)
	r.Mount("/", api.NewServer(state, al, st))

	server := &http.Server{Addr: cfg.HTTP.Addr, Handler: r}
	go func() {
		log.Printf("http: listening on %s (config=%s)", cfg.HTTP.Addr, cfgPath)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("http: %v", err)
		}
	}()

	// --- systemd notify / watchdog ------------------------------------------
	daemon.SdNotify(false, daemon.SdNotifyReady)
	if interval, err := daemon.SdWatchdogEnabled(false); err == nil && interval > 0 {
		go func() {
			tk := time.NewTicker(interval / 2)
			defer tk.Stop()
			for range tk.C {
				daemon.SdNotify(false, daemon.SdNotifyWatchdog)
			}
		}()
	}

	// --- Graceful shutdown ---------------------------------------------------
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	log.Println("shutting down")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = server.Shutdown(ctx) // graceful HTTP close
}
