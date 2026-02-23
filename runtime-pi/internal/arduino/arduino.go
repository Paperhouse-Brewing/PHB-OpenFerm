// SPDX-License-Identifier: Apache-2.0
package arduino

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"log"
	"sync"
	"time"

	"go.bug.st/serial"
)

type Telemetry struct {
	Type  string  `json:"type"` // "telemetry"
	FV    string  `json:"fv"`
	BeerC float64 `json:"beer_c"`
}

type Device struct {
	mu   sync.Mutex
	port serial.Port
	r    *bufio.Reader
	w    io.Writer

	lastSeen time.Time
	closed   chan struct{}
}

// Open the serial port
func Open(dev string, baud int) (*Device, error) {
	if baud == 0 {
		baud = 115200
	}
	mode := &serial.Mode{BaudRate: baud}
	p, err := serial.Open(dev, mode)
	if err != nil {
		return nil, err
	}
	d := &Device{
		port:     p,
		r:        bufio.NewReader(p),
		w:        p,
		lastSeen: time.Now(),
		closed:   make(chan struct{}),
	}
	return d, nil
}

func (d *Device) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	select {
	case <-d.closed: // already closed
	default:
		close(d.closed)
	}
	return d.port.Close()
}

// Run the read loop. Calls onTelemetry for each telemetry record.
func (d *Device) Run(onTelemetry func(t Telemetry)) {
	// ping watchdog
	go func() {
		tk := time.NewTicker(5 * time.Second)
		defer tk.Stop()
		for {
			select {
			case <-tk.C:
				_ = d.send(map[string]any{"type": "ping"})
				// stale watchdog log
				if time.Since(d.lastSeen) > 15*time.Second {
					log.Printf("arduino: no data for %v", time.Since(d.lastSeen))
				}
			case <-d.closed:
				return
			}
		}
	}()

	// read lines
	for {
		line, err := d.r.ReadBytes('\n')
		if err != nil {
			if errors.Is(err, io.EOF) {
				time.Sleep(time.Second)
				continue
			}
			log.Printf("arduino: read error: %v", err)
			time.Sleep(time.Second)
			continue
		}
		d.lastSeen = time.Now()

		var raw map[string]any
		if err := json.Unmarshal(line, &raw); err != nil {
			continue
		}
		switch raw["type"] {
		case "telemetry":
			b, _ := json.Marshal(raw)
			var t Telemetry
			if err := json.Unmarshal(b, &t); err == nil && onTelemetry != nil {
				onTelemetry(t)
			}
		case "pong":
			// ok
		default:
			// ignore unknown
		}
	}
}

// Valve command (implements our driver interface)
func (d *Device) SetValve(fvID string, open bool) error {
	state := "closed"
	if open {
		state = "open"
	}
	return d.send(map[string]any{"type": "set_valve", "fv": fvID, "state": state})
}

func (d *Device) send(v any) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	b, _ := json.Marshal(v)
	b = append(b, '\n')
	_, err := d.w.Write(b)
	return err
}
