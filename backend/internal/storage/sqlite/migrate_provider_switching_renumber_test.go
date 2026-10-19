package sqlite

import "testing"

func TestMigrateMovesProviderSwitchingFromItsBranchNumber(t *testing.T) {
	db := openMigratedDatabaseCopy(t, 216)
	// As a database that ran the feature branch, where the migration was 0201.
	if _, err := db.Exec(`UPDATE goose_db_version SET version_id = 201 WHERE version_id = 216`); err != nil {
		t.Fatalf("seed branch numbering: %v", err)
	}
	if err := migrate(db); err != nil {
		t.Fatalf("migrate a database that ran 0201_provider_switching: %v", err)
	}
	var at201, at216 int
	if err := db.QueryRow(`SELECT COUNT(*) FROM goose_db_version WHERE version_id = 201`).Scan(&at201); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM goose_db_version WHERE version_id = 216 AND is_applied = 1`).Scan(&at216); err != nil {
		t.Fatal(err)
	}
	if at201 != 0 || at216 != 1 {
		t.Fatalf("versions 201=%d 216=%d, want the migration recorded once as 216", at201, at216)
	}
	if err := migrate(db); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
}
