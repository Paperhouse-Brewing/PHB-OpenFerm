// SPDX-License-Identifier: Apache-2.0
package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"phb/fermenter-runtime/internal/control"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite" // registers driver "sqlite"
)

type EventPoint struct {
	T    int64  `json:"t"` // unix seconds
	Type string `json:"type"`
	FVID string `json:"fv_id"`
	Data string `json:"data"`
}

type Store struct{ db *sql.DB }

type ProfileListItem struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

func Open(path string) (*Store, error) {
	// WAL + busy-timeout; creates file if missing
	dsn := "file:" + path + "?mode=rwc&_journal_mode=WAL&_busy_timeout=5000"

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	s := &Store{db: db}
	if err := s.ensure(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) ensure() error {
	_, err := s.db.Exec(`
	PRAGMA journal_mode = WAL;
	PRAGMA synchronous   = NORMAL;

	CREATE TABLE IF NOT EXISTS fermenter_state(
	  fv_id       TEXT PRIMARY KEY,
	  name        TEXT    NOT NULL,
	  target_c    REAL    NOT NULL,
	  last_beer_c REAL    NOT NULL DEFAULT 0,
	  last_valve  TEXT    NOT NULL DEFAULT 'closed',
	  updated_at  TIMESTAMP NOT NULL,
	  mode TEXT NOT NULL DEFAULT 'profile',
	  override_state TEXT NOT NULL DEFAULT '',
	  override_until INTEGER NOT NULL DEFAULT 0
	);

	CREATE TABLE IF NOT EXISTS events(
	  id   INTEGER PRIMARY KEY AUTOINCREMENT,
	  ts   INTEGER NOT NULL,
	  type TEXT      NOT NULL,
	  fv_id TEXT,
	  data TEXT
	);
	CREATE INDEX IF NOT EXISTS idx_events_ts ON events(ts);

	-- new time-series samples for charts:
	CREATE TABLE IF NOT EXISTS samples (
	  ts        INTEGER NOT NULL,  -- unix seconds
	  fv_id     TEXT    NOT NULL,
	  beer_c    REAL    NOT NULL,
	  target_c  REAL    NOT NULL,
	  valve     TEXT    NOT NULL,
	  PRIMARY KEY (fv_id, ts)
	);
	CREATE INDEX IF NOT EXISTS idx_samples_fv_ts ON samples(fv_id, ts);

	-- fermentation profiles (JSON spec for readability)
	CREATE TABLE IF NOT EXISTS profiles (
	  id         INTEGER PRIMARY KEY AUTOINCREMENT,
	  name       TEXT NOT NULL,
	  spec_json  TEXT NOT NULL,     -- JSON: { "steps":[ ... ] }
	  created_at INTEGER NOT NULL   -- unix seconds
	);

	-- active profile per fermenter
	CREATE TABLE IF NOT EXISTS profile_assignments (
	  fv_id            TEXT PRIMARY KEY,  -- one active profile per FV
	  profile_id       INTEGER NOT NULL,
	  started_ts       INTEGER NOT NULL,  -- when the profile started
	  step_idx         INTEGER NOT NULL DEFAULT 0,
	  step_started_ts  INTEGER NOT NULL,  -- when current step began
	  from_c           REAL    NOT NULL,  -- starting temp for ramps
	  paused           INTEGER NOT NULL DEFAULT 0  -- 0=false, 1=true
	);
	CREATE INDEX IF NOT EXISTS idx_profiles_created ON profiles(created_at);

	CREATE TABLE IF NOT EXISTS settings (
	  key   TEXT PRIMARY KEY,
	  value TEXT NOT NULL
	);
	`)
	return err
}

func (s *Store) Close() error { return s.db.Close() }

func isBusy(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "SQLITE_BUSY") || strings.Contains(s, "database is locked")
}

func (s *Store) execRetry(q string, args ...any) error {
	var err error
	backoff := 50 * time.Millisecond
	for i := 0; i < 6; i++ { // ~0.05s → ~1.6s
		if _, err = s.db.Exec(q, args...); !isBusy(err) {
			return err
		}
		time.Sleep(backoff)
		backoff *= 2
	}
	return err
}

func (s *Store) ApplySavedTargets(state *control.SystemState) error {
	rows, err := s.db.Query(`SELECT fv_id, target_c, mode, override_state, override_until FROM fermenter_state`)
	if err != nil {
		return err
	}
	defer rows.Close()
	var id, mode, ovState string
	var t float64
	var ovUntil int64
	if err := rows.Scan(&id, &t, &mode, &ovState, &ovUntil); err == nil {
		_ = state.SetTarget(id, t)
		if mode != "" {
			_ = state.SetMode(id, control.ControlMode(mode))
		}
		if ovState != "" {
			var until time.Time
			if ovUntil > 0 {
				until = time.Unix(ovUntil, 0)
			}
			_ = state.SetValveOverrideUntil(id, ovState, until)
		}
	}
	return rows.Err()
}

func (s *Store) SaveTarget(id string, t float64, name string) error {
	return s.execRetry(`
INSERT INTO fermenter_state(fv_id,name,target_c,updated_at)
VALUES(?,?,?,?)
ON CONFLICT(fv_id) DO UPDATE SET
  target_c=excluded.target_c, name=excluded.name, updated_at=excluded.updated_at
`, id, name, t, time.Now().UTC())
}

func (s *Store) Snapshot(state *control.SystemState) error {
	now := time.Now().UTC()
	var firstErr error
	state.ForEach(func(f *control.Fermenter) {
		if err := s.execRetry(`
INSERT INTO fermenter_state(fv_id,name,target_c,last_beer_c,last_valve,updated_at,mode,override_state,override_until)
VALUES(?,?,?,?,?,?,?,?,?)
ON CONFLICT(fv_id) DO UPDATE SET
	name=excluded.name, 
    target_c=excluded.target_c,
	last_beer_c=excluded.last_beer_c, 
    last_valve=excluded.last_valve,
	updated_at=excluded.updated_at,
	mode=excluded.mode,
	override_state=excluded.override_state,
	override_until=excluded.override_until
`, f.ID, f.Name, f.TargetC, f.BeerC, f.Valve, now, string(f.Mode), f.Override.State, func() int64 {
			if f.Override.Until.IsZero() {
				return 0
			}
			return f.Override.Until.Unix()
		}()); err != nil && firstErr == nil {
			firstErr = err
		}
	})
	return firstErr
}

func (s *Store) AddEvent(typ, fvID, data string) {
	_ = s.execRetry(`INSERT INTO events(ts,type,fv_id,data) VALUES(?,?,?,?)`,
		time.Now().Unix(), typ, fvID, data)
}

type Event struct {
	TS   time.Time `json:"ts"`
	Type string    `json:"type"`
	FVID string    `json:"fv_id"`
	Data string    `json:"data"`
}

func (s *Store) ListEvents(limit int) ([]Event, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.Query(`SELECT ts,type,ifnull(fv_id,''),ifnull(data,'') FROM events ORDER BY ts DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Event
	for rows.Next() {
		var e Event
		if err := rows.Scan(&e.TS, &e.Type, &e.FVID, &e.Data); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *Store) InsertSamples(state *control.SystemState) error {
	now := time.Now().Unix()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	stmt, err := tx.Prepare(`INSERT OR REPLACE INTO samples (ts,fv_id,beer_c,target_c,valve) VALUES (?,?,?,?,?)`)
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	defer stmt.Close()

	state.ForEach(func(fv *control.Fermenter) {
		_, _ = stmt.Exec(now, fv.ID, fv.BeerC, fv.TargetC, fv.Valve)
	})
	return tx.Commit()
}

type SeriesPoint struct {
	T       int64   `json:"t"` // unix seconds
	BeerC   float64 `json:"beer_c"`
	TargetC float64 `json:"target_c"`
}

// step = bucket size (e.g., 60s). from/to define range.
func (s *Store) QuerySeries(fvID string, from, to time.Time, step time.Duration) ([]SeriesPoint, error) {
	if step <= 0 {
		step = time.Minute
	}
	fromS, toS := from.Unix(), to.Unix()
	bucket := int64(step.Seconds())

	rows, err := s.db.Query(`
SELECT (ts / ?) * ? AS b,
       AVG(beer_c)   AS beer,
       AVG(target_c) AS tgt
FROM samples
WHERE fv_id = ? AND ts BETWEEN ? AND ?
GROUP BY b
ORDER BY b ASC
`, bucket, bucket, fvID, fromS, toS)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []SeriesPoint
	for rows.Next() {
		var b int64
		var beer, tgt float64
		if err := rows.Scan(&b, &beer, &tgt); err != nil {
			return nil, err
		}
		out = append(out, SeriesPoint{T: b, BeerC: beer, TargetC: tgt})
	}
	return out, rows.Err()
}

// optional: prune old rows
func (s *Store) PruneSamples(maxAge time.Duration) error {
	cut := time.Now().Add(-maxAge).Unix()
	_, err := s.db.Exec(`DELETE FROM samples WHERE ts < ?`, cut)
	return err
}

// ProfileSpec is stored as JSON for human-readable OSS.
type ProfileSpec struct {
	Steps []struct {
		Type         string  `json:"type"`                      // "hold" or "ramp"
		TargetC      float64 `json:"target_c"`                  // target temp for hold/ramp end
		DurationS    int64   `json:"duration_s,omitempty"`      // for hold
		Duration     string  `json:"duration,omitempty"`        // e.g. "2d12h", "36h", "90m"
		RateCPerHour float64 `json:"rate_c_per_hour,omitempty"` // for ramp
	} `json:"steps"`
}

func (s *Store) CreateProfile(name string, spec ProfileSpec) (int64, error) {
	b, _ := json.Marshal(spec)
	res, err := s.db.Exec(`INSERT INTO profiles(name,spec_json,created_at) VALUES(?,?,?)`,
		name, string(b), time.Now().Unix())
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) ListProfiles() ([]ProfileListItem, error) {
	rows, err := s.db.Query(`SELECT id, name FROM profiles ORDER BY id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []ProfileListItem
	for rows.Next() {
		var it ProfileListItem
		if err := rows.Scan(&it.ID, &it.Name); err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	if out == nil {
		out = make([]ProfileListItem, 0)
	} // << ensure [] not null
	return out, rows.Err()
}

func (s *Store) GetProfile(id int64) (ProfileSpec, error) {
	var js string
	if err := s.db.QueryRow(`SELECT spec_json FROM profiles WHERE id=?`, id).Scan(&js); err != nil {
		return ProfileSpec{}, err
	}
	var spec ProfileSpec
	if err := json.Unmarshal([]byte(js), &spec); err != nil {
		return ProfileSpec{}, err
	}
	return spec, nil
}

type Assignment struct {
	FVID          string
	ProfileID     int64
	StartedTs     int64
	StepIdx       int
	StepStartedTs int64
	FromC         float64
	Paused        bool
}

func (s *Store) GetAssignment(fvID string) (*Assignment, error) {
	var a Assignment
	var paused int
	err := s.db.QueryRow(`SELECT fv_id,profile_id,started_ts,step_idx,step_started_ts,from_c,paused
	                      FROM profile_assignments WHERE fv_id=?`, fvID).
		Scan(&a.FVID, &a.ProfileID, &a.StartedTs, &a.StepIdx, &a.StepStartedTs, &a.FromC, &paused)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	a.Paused = paused != 0
	return &a, nil
}

// helper if you don't already have it
func btoi(b bool) int {
	if b {
		return 1
	}
	return 0
}

func (s *Store) UpsertAssignment(a Assignment) error {
	// satisfy NOT NULL constraint if caller didn’t set it
	started := a.StartedTs
	if started == 0 {
		started = time.Now().Unix()
	}

	_, err := s.db.Exec(`
INSERT INTO profile_assignments (
  fv_id, profile_id, started_ts, step_idx, step_started_ts, from_c, paused
) VALUES (?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(fv_id) DO UPDATE SET
  profile_id      = excluded.profile_id,
  started_ts      = excluded.started_ts,
  step_idx        = excluded.step_idx,
  step_started_ts = excluded.step_started_ts,
  from_c          = excluded.from_c,
  paused          = excluded.paused
`, a.FVID, a.ProfileID, started, a.StepIdx, a.StepStartedTs, a.FromC, btoi(a.Paused))
	return err
}

func (s *Store) DeleteAssignment(fvID string) error {
	_, err := s.db.Exec(`DELETE FROM profile_assignments WHERE fv_id=?`, fvID)
	return err
}

func (s *Store) ApplySavedModes(state *control.SystemState) error {
	rows, err := s.db.Query(`SELECT fv_id, ifnull(mode,'profile') FROM fermenter_state`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id, mode string
		if err := rows.Scan(&id, &mode); err == nil {
			_ = state.SetMode(id, control.ControlMode(mode))
		}
	}
	return rows.Err()
}

func (s *Store) SaveMode(id string, mode string) error {
	_, err := s.db.Exec(`
INSERT INTO fermenter_state(fv_id, mode, updated_at, name, target_c, last_beer_c, last_valve)
VALUES(?, ?, ?, '', 0, 0, 'closed')
ON CONFLICT(fv_id) DO UPDATE SET
  mode=excluded.mode,
  updated_at=excluded.updated_at
`, id, mode, time.Now().UTC())
	return err
}

// internal/store/sqlite.go
func (s *Store) QueryEvents(fvID string, from int64, to int64) ([]EventPoint, error) {
	rows, err := s.db.Query(`
        SELECT
          ts AS t,
          type,
          IFNULL(fv_id, ''),
          IFNULL(data, '')
        FROM events
        WHERE (? = '' OR fv_id = ?)
          AND ts BETWEEN ? AND ?
        ORDER BY ts ASC
    `, fvID, fvID, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []EventPoint
	for rows.Next() {
		var e EventPoint
		if err := rows.Scan(&e.T, &e.Type, &e.FVID, &e.Data); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *Store) getSetting(key string) (string, error) {
	var v string
	err := s.db.QueryRow(`SELECT value FROM settings WHERE key=?`, key).Scan(&v)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return v, err
}
func (s *Store) setSetting(key, val string) error {
	return s.execRetry(`INSERT INTO settings(key,value) VALUES(?,?)
        ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, val)
}

type ControllerSettings struct {
	BandC      float64 `json:"band_c"`
	MinChangeS int     `json:"min_change_s"`
	MaxOpen    int     `json:"max_open"` // 0 = unlimited
}

func (s *Store) LoadControllerSettings() (ControllerSettings, error) {
	var out ControllerSettings
	if v, _ := s.getSetting("band_c"); v != "" {
		if f, _ := strconv.ParseFloat(v, 64); f > 0 {
			out.BandC = f
		}
	}
	if v, _ := s.getSetting("min_change_s"); v != "" {
		if n, _ := strconv.Atoi(v); n >= 0 {
			out.MinChangeS = n
		}
	}
	if v, _ := s.getSetting("max_open"); v != "" {
		if n, _ := strconv.Atoi(v); n >= 0 {
			out.MaxOpen = n
		}
	}
	return out, nil
}

func (s *Store) SaveControllerSettings(cs ControllerSettings) error {
	if err := s.setSetting("band_c", fmt.Sprintf("%g", cs.BandC)); err != nil {
		return err
	}
	if err := s.setSetting("min_change_s", strconv.Itoa(cs.MinChangeS)); err != nil {
		return err
	}
	if err := s.setSetting("max_open", strconv.Itoa(cs.MaxOpen)); err != nil {
		return err
	}
	return nil
}
