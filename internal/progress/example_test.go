package progress_test

import (
	"fmt"
	"os"
	"time"

	"github.com/ShizukaJiku/gitsi/internal/progress"
)

// Without a console (a pipe or a log) it writes one line every 10% and a final one with the summary.
func ExampleReporter_Transfer() {
	now := time.Unix(0, 0)
	r := progress.New(progress.Options{Out: os.Stdout, Now: func() time.Time { return now }})
	cb := r.Transfer("report.zip", 10<<20, "verifying on the hub")
	for done := int64(0); done <= 10<<20; done += 1 << 20 { // 1 MiB every half second
		cb(done, 10<<20)
		now = now.Add(500 * time.Millisecond)
	}
	fmt.Println("Sent: report.zip")
	// Output:
	//   report.zip: 0% (0 B / 10.0 MiB)
	//   report.zip: 10% (1.0 MiB / 10.0 MiB)
	//   report.zip: 20% (2.0 MiB / 10.0 MiB) 2.0 MiB/s, ~4 s left
	//   report.zip: 30% (3.0 MiB / 10.0 MiB) 2.0 MiB/s, ~4 s left
	//   report.zip: 40% (4.0 MiB / 10.0 MiB) 2.0 MiB/s, ~3 s left
	//   report.zip: 50% (5.0 MiB / 10.0 MiB) 2.0 MiB/s, ~3 s left
	//   report.zip: 60% (6.0 MiB / 10.0 MiB) 2.0 MiB/s, ~2 s left
	//   report.zip: 70% (7.0 MiB / 10.0 MiB) 2.0 MiB/s, ~2 s left
	//   report.zip: 80% (8.0 MiB / 10.0 MiB) 2.0 MiB/s, ~1 s left
	//   report.zip: 90% (9.0 MiB / 10.0 MiB) 2.0 MiB/s, ~1 s left
	//   report.zip: 100% (10.0 MiB in 5 s, 2.0 MiB/s) - verifying on the hub...
	// Sent: report.zip
}
