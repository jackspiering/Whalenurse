package store

import (
	"database/sql"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/jackspiering/whalenurse/internal/engine"
)

var t0 = time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)

func open(t *testing.T, dir string) *Store {
	t.Helper()
	s, err := Open(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func sampleKey() engine.KeyRecord {
	return engine.KeyRecord{
		Key: "h/p/web/1", Name: "p-web-1", MarkerAt: t0.Add(-time.Minute),
		Loop:  engine.LoopRecord{Have: true, LastCount: 4, LastObs: t0, N: 3, First: t0.Add(-6 * time.Minute), Last: t0.Add(-time.Minute)},
		Flaps: []time.Time{t0.Add(-2 * time.Hour), t0},
		Incident: &engine.IncidentRecord{
			Opened: t0, Class: "unhealthy", Rung: 2, CooldownUntil: t0.Add(time.Minute), Spent: true, SpentReason: "ladder",
			Suspended: true, SuspendedAt: t0.Add(30 * time.Second), Recovering: true, RecoverySince: t0.Add(time.Hour), RecoveryCount: 7,
		},
	}
}

func TestStateSurvivesReopeningTheDatabase(t *testing.T) {
	dir := t.TempDir()
	s := open(t, dir)
	k := sampleKey()
	s.SaveKey(k)
	s.SavePause(engine.Pause{Scope: "p/web", Until: t0.Add(2 * time.Hour), Created: t0})
	s.SavePause(engine.Pause{Scope: "q", Created: t0}) // indefinite
	s.SaveAction(t0.Add(-10 * time.Minute))
	s.SaveAction(t0.Add(-2 * time.Hour)) // outside the rate-limit hour
	s.SetMeta("boot_id", "abc")
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	s = open(t, dir)
	defer s.Close()
	snap, err := s.Load(t0)
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Keys) != 1 || !reflect.DeepEqual(snap.Keys[0], k) {
		t.Fatalf("key did not round-trip:\n got %+v\nwant %+v", snap.Keys, k)
	}
	if len(snap.Pauses) != 2 || snap.Pauses[0].Scope != "p/web" || !snap.Pauses[0].Until.Equal(t0.Add(2*time.Hour)) || !snap.Pauses[1].Until.IsZero() {
		t.Fatalf("%+v", snap.Pauses)
	}
	if len(snap.Actions) != 1 || !snap.Actions[0].Equal(t0.Add(-10*time.Minute)) {
		t.Fatalf("only the last hour of actions counts: %+v", snap.Actions)
	}
	if v, ok := s.Meta("boot_id"); !ok || v != "abc" {
		t.Fatalf("%q %v", v, ok)
	}
	if _, ok := s.Meta("nope"); ok {
		t.Fatal("missing meta reported present")
	}
}

func TestKeyWithoutAnIncidentAndAClearedMarker(t *testing.T) {
	s := open(t, t.TempDir())
	defer s.Close()
	s.SaveKey(sampleKey())
	k := sampleKey()
	k.Incident, k.MarkerAt = nil, time.Time{} // everything cleared
	s.SaveKey(k)
	snap, _ := s.Load(t0)
	if len(snap.Keys) != 1 || snap.Keys[0].Incident != nil || !snap.Keys[0].MarkerAt.IsZero() {
		t.Fatalf("%+v", snap.Keys)
	}
	hist, _ := s.History("")
	if len(hist) != 1 || hist[0].Outcome != "cleared" {
		t.Fatalf("an incident the engine dropped without saying why is history too: %+v", hist)
	}
}

func TestClosedIncidentsBecomeHistoryAndRetentionPrunesThem(t *testing.T) {
	s := open(t, t.TempDir())
	defer s.Close()
	s.SaveKey(sampleKey())
	s.CloseIncident(engine.ClosedIncident{Key: "h/p/web/1", Opened: t0, Closed: t0.Add(time.Hour), Class: "unhealthy", Outcome: "recovered"})
	old := engine.ClosedIncident{Key: "h/p/old/1", Opened: t0.Add(-40 * 24 * time.Hour), Closed: t0.Add(-39 * 24 * time.Hour), Class: "crash-loop", Outcome: "recovered"}
	s.CloseIncident(old)

	snap, _ := s.Load(t0)
	if snap.Keys[0].Incident != nil {
		t.Fatalf("the closed incident is still loaded as open: %+v", snap.Keys[0].Incident)
	}
	hist, err := s.History("")
	if err != nil || len(hist) != 2 || hist[0].Key != "h/p/web/1" {
		t.Fatalf("%+v %v", hist, err)
	}
	n, err := s.Prune(t0.Add(2*time.Hour), 30*24*time.Hour, 24*time.Hour)
	if err != nil || n != 1 {
		t.Fatalf("pruned %d, %v", n, err)
	}
	hist, _ = s.History("")
	if len(hist) != 1 || hist[0].Key != "h/p/web/1" {
		t.Fatalf("%+v", hist)
	}
	if h, _ := s.History("h/p/old/1"); len(h) != 0 {
		t.Fatalf("%+v", h)
	}
}

func TestPruneDropsOldFlapAndActionRows(t *testing.T) {
	s := open(t, t.TempDir())
	defer s.Close()
	s.SaveAction(t0.Add(-2 * time.Hour))
	if _, err := s.db.Exec(`INSERT INTO flaps (key, at) VALUES ('x', ?), ('x', ?)`, tn(t0.Add(-30*time.Hour)), tn(t0.Add(-time.Hour))); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Prune(t0, 30*24*time.Hour, 24*time.Hour); err != nil {
		t.Fatal(err)
	}
	var flaps, actions int
	s.db.QueryRow(`SELECT count(*) FROM flaps`).Scan(&flaps)
	s.db.QueryRow(`SELECT count(*) FROM actions`).Scan(&actions)
	if flaps != 1 || actions != 0 {
		t.Fatalf("flaps=%d actions=%d", flaps, actions)
	}
}

func TestDeleteKeyForgetsItButKeepsHistory(t *testing.T) {
	s := open(t, t.TempDir())
	defer s.Close()
	s.SaveKey(sampleKey())
	s.DeleteKey("h/p/web/1")
	snap, _ := s.Load(t0)
	if len(snap.Keys) != 0 {
		t.Fatalf("%+v", snap.Keys)
	}
	if hist, _ := s.History(""); len(hist) != 1 || hist[0].Outcome != "gone" {
		t.Fatalf("%+v", hist)
	}
}

func TestOnlyOneOpenIncidentPerKey(t *testing.T) {
	s := open(t, t.TempDir())
	defer s.Close()
	a := sampleKey()
	s.SaveKey(a)
	b := sampleKey()
	b.Incident = &engine.IncidentRecord{Opened: t0.Add(time.Hour), Class: "crash-loop"}
	s.SaveKey(b) // a newer incident: the stale open row is closed, not left to violate the unique index
	var open int
	s.db.QueryRow(`SELECT count(*) FROM incidents WHERE closed_at IS NULL`).Scan(&open)
	if open != 1 || s.WriteErrors() != 0 {
		t.Fatalf("open=%d write errors=%d", open, s.WriteErrors())
	}
}

func TestRefusesADatabaseFromTheFuture(t *testing.T) {
	dir := t.TempDir()
	s := open(t, dir)
	s.db.Exec("PRAGMA user_version = 99")
	s.Close()
	if _, err := Open(dir, nil); err == nil {
		t.Fatal("opened a schema newer than this build understands")
	}
}

func TestOpenCreatesTheDirectoryAndIsReopenable(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "a", "b")
	for i := 0; i < 2; i++ {
		s, err := Open(dir, nil)
		if err != nil {
			t.Fatal(err)
		}
		s.Close()
	}
	db, err := sql.Open("sqlite", "file:"+filepath.Join(dir, FileName))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var v int
	db.QueryRow("PRAGMA user_version").Scan(&v)
	if v != schemaVersion {
		t.Fatalf("user_version %d", v)
	}
}

func TestAWriteToAClosedStoreIsCountedNotFatal(t *testing.T) {
	s := open(t, t.TempDir())
	s.Close()
	s.SaveKey(sampleKey())
	if s.WriteErrors() == 0 {
		t.Fatal("a failed write must be counted")
	}
}
