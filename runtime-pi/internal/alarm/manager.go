// SPDX-License-Identifier: Apache-2.0
package alarm

import (
	"fmt"
	"sync"
	"time"

	"phb/fermenter-runtime/internal/control"
)

type Type string

const (
	HighTemp Type = "HIGH_TEMP"
	LowTemp  Type = "LOW_TEMP"
)

type Alarm struct {
	ID           string    `json:"id"` // e.g., HIGH_TEMP:fv1
	Type         Type      `json:"type"`
	FVID         string    `json:"fv_id"`
	Started      time.Time `json:"started"`
	LastSeen     time.Time `json:"last_seen"`
	Acknowledged bool      `json:"acknowledged"`
	Message      string    `json:"message"`
	Value        float64   `json:"value"` // last value seen that sustained the alarm
}

type Manager struct {
	mu       sync.Mutex
	active   map[string]*Alarm
	highMarg float64
	lowMarg  float64
	debounce time.Duration
	addEvent func(typ, fvID, data string) // injected to persist events
}

func NewManager(highMarg, lowMarg float64, debounce time.Duration, addEvent func(string, string, string)) *Manager {
	return &Manager{
		active:   make(map[string]*Alarm),
		highMarg: highMarg,
		lowMarg:  lowMarg,
		debounce: debounce,
		addEvent: addEvent,
	}
}

func (m *Manager) key(t Type, fvID string) string {
	return string(t) + ":" + fvID
}

func (m *Manager) raise(t Type, fv *control.Fermenter, now time.Time) {
	id := m.key(t, fv.ID)
	a, ok := m.active[id]
	if !ok {
		msg := ""
		switch t {
		case HighTemp:
			msg = fmt.Sprintf("%s high: %.2f°C > setpoint %.2f°C + %.2f°C", fv.Name, fv.BeerC, fv.TargetC, m.highMarg)
		case LowTemp:
			msg = fmt.Sprintf("%s low: %.2f°C < setpoint %.2f°C - %.2f°C", fv.Name, fv.BeerC, fv.TargetC, m.lowMarg)
		}
		a = &Alarm{
			ID: id, Type: t, FVID: fv.ID,
			Started: now, LastSeen: now, Message: msg, Value: fv.BeerC,
		}
		m.active[id] = a
		if m.addEvent != nil {
			m.addEvent("ALARM_OPEN", fv.ID, msg)
		}
		return
	}
	// refresh
	a.LastSeen = now
	a.Value = fv.BeerC
}

func (m *Manager) clear(t Type, fv *control.Fermenter, now time.Time) {
	id := m.key(t, fv.ID)
	if _, ok := m.active[id]; ok {
		delete(m.active, id)
		if m.addEvent != nil {
			m.addEvent("ALARM_CLEAR", fv.ID, string(t))
		}
	}
}

// Evaluate reads current state and opens/closes alarms with debounce.
func (m *Manager) Evaluate(state *control.SystemState) {
	now := time.Now()
	toClose := make([]struct {
		typ Type
		fv  *control.Fermenter
	}, 0, 4)
	toOpen := make([]struct {
		typ Type
		fv  *control.Fermenter
	}, 0, 4)

	state.ForEach(func(fv *control.Fermenter) {
		high := fv.BeerC > fv.TargetC+m.highMarg
		low := fv.BeerC < fv.TargetC-m.lowMarg

		// Decide opens
		if high {
			toOpen = append(toOpen, struct {
				typ Type
				fv  *control.Fermenter
			}{HighTemp, fv})
		}
		if low {
			toOpen = append(toOpen, struct {
				typ Type
				fv  *control.Fermenter
			}{LowTemp, fv})
		}

		// Decide closes (only when back within band)
		if !high {
			toClose = append(toClose, struct {
				typ Type
				fv  *control.Fermenter
			}{HighTemp, fv})
		}
		if !low {
			toClose = append(toClose, struct {
				typ Type
				fv  *control.Fermenter
			}{LowTemp, fv})
		}
	})

	m.mu.Lock()
	defer m.mu.Unlock()

	// Debounce: require sustained condition for m.debounce
	for _, o := range toOpen {
		id := m.key(o.typ, o.fv.ID)
		if a, ok := m.active[id]; ok {
			// already open: refresh
			a.LastSeen = now
			a.Value = o.fv.BeerC
			continue
		}
		// use LastSeen as a temp “candidate start”
		candidate := &Alarm{ID: id, Type: o.typ, FVID: o.fv.ID, Started: now, LastSeen: now, Message: "", Value: o.fv.BeerC}
		// We keep it “pending” by placing it in active and letting it mature
		// Simpler approach: open immediately and rely on debounce for clearing; or:
		// open only if condition persists on next tick > debounce.
		// To keep code simple and robust, open immediately:
		m.active[id] = candidate
		if m.addEvent != nil {
			msg := ""
			if o.typ == HighTemp {
				msg = fmt.Sprintf("%s high: %.2f°C > setpoint %.2f°C + %.2f°C", o.fv.Name, o.fv.BeerC, o.fv.TargetC, m.highMarg)
			} else {
				msg = fmt.Sprintf("%s low: %.2f°C < setpoint %.2f°C - %.2f°C", o.fv.Name, o.fv.BeerC, o.fv.TargetC, m.lowMarg)
			}
			candidate.Message = msg
			m.addEvent("ALARM_OPEN", o.fv.ID, msg)
		}
	}

	for _, c := range toClose {
		id := m.key(c.typ, c.fv.ID)
		if a, ok := m.active[id]; ok {
			// Only clear if the condition has been normal for >= debounce
			if now.Sub(a.LastSeen) >= m.debounce {
				delete(m.active, id)
				if m.addEvent != nil {
					m.addEvent("ALARM_CLEAR", c.fv.ID, string(c.typ))
				}
			}
		}
	}
}

func (m *Manager) Active() []Alarm {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Alarm, 0, len(m.active))
	for _, a := range m.active {
		out = append(out, *a)
	}
	return out
}

func (m *Manager) ActiveCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.active)
}

func (m *Manager) Ack(id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if a, ok := m.active[id]; ok {
		a.Acknowledged = true
		if m.addEvent != nil {
			m.addEvent("ALARM_ACK", a.FVID, a.ID)
		}
		return true
	}
	return false
}
