package domain

import "testing"

func TestCanStartRun(t *testing.T) {
	if err := CanStartRun(Job{Status: JobHold}, false); err == nil || err.(*Error).Code != CodeJobHold {
		t.Fatalf("hold: %v", err)
	}
	if err := CanStartRun(Job{Exclusive: true, Status: JobActive}, true); err == nil || err.(*Error).Code != CodeRunOverlap {
		t.Fatalf("overlap: %v", err)
	}
	if err := CanStartRun(Job{Exclusive: true, Status: JobActive}, false); err != nil {
		t.Fatal(err)
	}
}

func TestValidateJob(t *testing.T) {
	if fields := ValidateJob(Job{}); len(fields) == 0 {
		t.Fatal("expected errors")
	}
	if fields := ValidateJob(Job{Code: "sweep", Handler: "outbox.sweep", PartitionCount: 1, Concurrency: 1, PartitionStrategy: PartitionHash, Priority: PriorityB}); len(fields) != 0 {
		t.Fatalf("%v", fields)
	}
}

func TestRangeBounds(t *testing.T) {
	start, end := RangeBounds(1_000_000, 16, 0)
	if start != 0 || end != 62_500 {
		t.Fatalf("%d %d", start, end)
	}
	start, end = RangeBounds(1_000_000, 16, 15)
	if start != 937_500 || end != 1_000_000 {
		t.Fatalf("%d %d", start, end)
	}
}

func TestValidateJobCronAndTimeZone(t *testing.T) {
	base := Job{Code: "c", Handler: "h", PartitionCount: 1, Concurrency: 1}
	tests := []struct {
		name, cron, zone, field string
	}{
		{"no schedule", "", "", ""},
		{"valid", "*/5 * * * *", "Europe/Berlin", ""},
		{"minute out of range", "61 * * * *", "UTC", "cron"},
		{"four fields", "* * * *", "UTC", "cron"},
		{"thirtieth of february", "0 0 30 2 *", "UTC", "cron"},
		{"unknown zone", "* * * * *", "Mars/Olympus", "timeZone"},
		{"local zone", "* * * * *", "Local", "timeZone"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			job := base
			job.Cron, job.TimeZone = tc.cron, tc.zone
			fields := ValidateJob(job)
			if tc.field == "" && len(fields) != 0 || tc.field != "" && fields[tc.field] == "" {
				t.Fatalf("fields = %v, want field %q", fields, tc.field)
			}
		})
	}
}
