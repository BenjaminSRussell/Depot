package jobs_test

import (
	"context"
	"testing"
	"time"

	"github.com/BenjaminSRussell/depot/platform/dbx"
	"github.com/BenjaminSRussell/depot/platform/testkit"
	"github.com/BenjaminSRussell/depot/services/batch/internal/jobs"
	"github.com/BenjaminSRussell/depot/services/batch/internal/store"
	"github.com/BenjaminSRussell/depot/services/batch/migrations"
)

func TestPgScheduleSourceSeesEveryTenantAndMarksFiredOnce(t *testing.T) {
	admin, app := testkit.DBWithApp(t)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	jobStore := store.New(app)
	for n := 1; n <= 2; n++ {
		if _, err := jobStore.SaveJob(scoped(n), store.Job{Code: "nightly", Handler: "synthetic.rows", Cron: "*/5 * * * *", TimeZone: "UTC"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := jobStore.SaveJob(scoped(1), store.Job{Code: "manual", Handler: "synthetic.rows"}); err != nil {
		t.Fatal(err)
	}
	src := jobs.NewPgScheduleSource(app)
	due, err := src.Due(context.Background())
	if err != nil || len(due) != 2 {
		t.Fatalf("want the two scheduled jobs of both tenants, got %d, %v", len(due), err)
	}
	first := due[0]
	if first.LegalEntityID != testkit.TenantID(9) || first.ScheduledBy != testkit.TenantID(8) || first.Cron != "*/5 * * * *" {
		t.Fatalf("schedule binding lost: %+v", first)
	}
	fired := time.Now().Truncate(time.Minute)
	if moved, err := src.MarkFired(context.Background(), first, fired); err != nil || !moved {
		t.Fatalf("first mark = %v, %v", moved, err)
	}
	if moved, err := src.MarkFired(context.Background(), first, fired); err != nil || moved {
		t.Fatalf("a second replica must lose the compare-and-set, got %v, %v", moved, err)
	}
}
