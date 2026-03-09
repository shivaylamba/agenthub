package db

import (
	"path/filepath"
	"testing"
)

func TestMergeQueriesUseAllParents(t *testing.T) {
	db := openTestDB(t)

	mustInsertCommit(t, db, "root", nil, "", "root")
	mustInsertCommit(t, db, "alpha", []string{"root"}, "", "alpha")
	mustInsertCommit(t, db, "beta", []string{"root"}, "", "beta")
	mustInsertCommit(t, db, "merge", []string{"alpha", "beta"}, "", "merge")

	children, err := db.GetChildren("root")
	if err != nil {
		t.Fatalf("get children(root): %v", err)
	}
	assertHashes(t, children, []string{"alpha", "beta"})

	alphaChildren, err := db.GetChildren("alpha")
	if err != nil {
		t.Fatalf("get children(alpha): %v", err)
	}
	assertHashes(t, alphaChildren, []string{"merge"})

	betaChildren, err := db.GetChildren("beta")
	if err != nil {
		t.Fatalf("get children(beta): %v", err)
	}
	assertHashes(t, betaChildren, []string{"merge"})

	leaves, err := db.GetLeaves()
	if err != nil {
		t.Fatalf("get leaves: %v", err)
	}
	assertHashes(t, leaves, []string{"merge"})

	lineage, err := db.GetLineage("merge")
	if err != nil {
		t.Fatalf("get lineage: %v", err)
	}
	assertHashes(t, lineage, []string{"merge", "alpha", "beta", "root"})

	merge, err := db.GetCommit("merge")
	if err != nil {
		t.Fatalf("get commit(merge): %v", err)
	}
	if merge == nil {
		t.Fatal("expected merge commit")
	}
	assertStrings(t, merge.ParentHashes, []string{"alpha", "beta"})
}

func TestMigrateBackfillsCommitParents(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agenthub.db")
	db, err := Open(path)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	if _, err := db.db.Exec(`
		CREATE TABLE commits (
			hash TEXT PRIMARY KEY,
			parent_hash TEXT,
			agent_id TEXT,
			message TEXT,
			created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
		);
		INSERT INTO commits (hash, parent_hash, agent_id, message) VALUES ('root', '', '', 'root');
		INSERT INTO commits (hash, parent_hash, agent_id, message) VALUES ('child', 'root', 'agent', 'child');
	`); err != nil {
		t.Fatalf("seed legacy schema: %v", err)
	}

	if err := db.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	children, err := db.GetChildren("root")
	if err != nil {
		t.Fatalf("get children(root): %v", err)
	}
	assertHashes(t, children, []string{"child"})

	leaves, err := db.GetLeaves()
	if err != nil {
		t.Fatalf("get leaves: %v", err)
	}
	assertHashes(t, leaves, []string{"child"})
}

func openTestDB(t *testing.T) *DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "agenthub.db")
	db, err := Open(path)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

func mustInsertCommit(t *testing.T, db *DB, hash string, parents []string, agentID, message string) {
	t.Helper()
	if err := db.InsertCommit(hash, parents, agentID, message); err != nil {
		t.Fatalf("insert commit %s: %v", hash, err)
	}
}

func assertHashes(t *testing.T, commits []Commit, want []string) {
	t.Helper()
	got := make([]string, 0, len(commits))
	for _, commit := range commits {
		got = append(got, commit.Hash)
	}
	assertStrings(t, got, want)
}

func assertStrings(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("unexpected length: got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("unexpected values: got %v want %v", got, want)
		}
	}
}
