package sqlite

import "testing"

func TestMigrateMovesProviderSwitchingFromItsBranchNumbers(t *testing.T) {
	for _, old := range []int64{201, 216} {
		db := openMigratedDatabaseCopy(t, providerSwitchingVersion)
		// As a database that ran a preview build, where the migration had this number.
		if _, err := db.Exec(`UPDATE goose_db_version SET version_id = ? WHERE version_id = ?`, old, providerSwitchingVersion); err != nil {
			t.Fatalf("seed branch numbering: %v", err)
		}
		if err := migrate(db); err != nil {
			t.Fatalf("migrate a database that ran it as %d: %v", old, err)
		}
		var atOld, atNew int
		if err := db.QueryRow(`SELECT COUNT(*) FROM goose_db_version WHERE version_id = ?`, old).Scan(&atOld); err != nil {
			t.Fatal(err)
		}
		if err := db.QueryRow(`SELECT COUNT(*) FROM goose_db_version WHERE version_id = ? AND is_applied = 1`, providerSwitchingVersion).Scan(&atNew); err != nil {
			t.Fatal(err)
		}
		if atOld != 0 || atNew != 1 {
			t.Fatalf("versions %d=%d new=%d, want the migration recorded once at its version", old, atOld, atNew)
		}
		if err := migrate(db); err != nil {
			t.Fatalf("second migrate: %v", err)
		}
	}
}
