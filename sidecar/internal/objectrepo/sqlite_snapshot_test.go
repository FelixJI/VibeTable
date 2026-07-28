package objectrepo

import (
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func TestSQLiteReadTransactionIsAFixedBarrierView(t *testing.T) {
	path := filepath.Join(t.TempDir(), "barrier.db")
	writer, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	if _, err := writer.Exec(`
		PRAGMA journal_mode=WAL;
		CREATE TABLE state (id INTEGER PRIMARY KEY, value TEXT NOT NULL);
		INSERT INTO state(id, value) VALUES (1, 'before');
	`); err != nil {
		t.Fatal(err)
	}
	reader, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	tx, err := reader.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	var value string
	if err := tx.QueryRow("SELECT value FROM state WHERE id=1").Scan(&value); err != nil || value != "before" {
		t.Fatalf("initial barrier read=%q err=%v", value, err)
	}
	if _, err := writer.Exec("UPDATE state SET value='after' WHERE id=1"); err != nil {
		t.Fatal(err)
	}
	if err := tx.QueryRow("SELECT value FROM state WHERE id=1").Scan(&value); err != nil || value != "before" {
		t.Fatalf("read view moved after concurrent commit: value=%q err=%v", value, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := reader.QueryRow("SELECT value FROM state WHERE id=1").Scan(&value); err != nil || value != "after" {
		t.Fatalf("new view did not observe commit: value=%q err=%v", value, err)
	}
}
