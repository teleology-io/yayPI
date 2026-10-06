package cron

import (
	"context"
	"errors"
	"testing"

	"github.com/teleology-io/yayPI/internal/schema"
	"github.com/teleology-io/yayPI/internal/testutil"
)

func TestDBLockerOneHolderPerTick(t *testing.T) {
	reg := schema.NewRegistry()
	reg.RegisterEntity(schema.NewJobLockEntity())
	dbm := testutil.SQLiteDB(t, reg)
	a, b := newDBLocker(dbm.Default()), newDBLocker(dbm.Default())
	ctx := context.Background()

	lock, err := a.Lock(ctx, "nightly")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.Lock(ctx, "nightly"); !errors.Is(err, errLocked) {
		t.Fatalf("second instance acquired a held lock: %v", err)
	}
	// After unlock the lease still covers minHold, so a slightly-late replica can't
	// re-run the same tick.
	if err := lock.Unlock(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Lock(ctx, "nightly"); err == nil {
		t.Fatal("lock reacquired within minHold of the run")
	}
	// Once the lease has fully expired it can be taken.
	b.minHold = 0
	a.minHold = 0
	if _, err := dbm.Default().SQL.Exec(`UPDATE yaypi_job_locks SET locked_until = 0`); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Lock(ctx, "nightly"); err != nil {
		t.Fatalf("expired lease not reacquired: %v", err)
	}
}
