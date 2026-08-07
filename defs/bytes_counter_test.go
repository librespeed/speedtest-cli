package defs

import (
	"sync"
	"testing"
)

// TestBytesCounterConcurrentAccess exercises the pattern the counter is used
// in: several transfer goroutines writing while the progress spinner polls the
// averages. It is the regression test for the mixed atomic/non-atomic access
// that used to make this racy, so it is only meaningful under -race.
func TestBytesCounterConcurrentAccess(t *testing.T) {
	const (
		writers    = 8
		writes     = 2000
		chunkSize  = 16
		pollers    = 4
		wantTotal  = uint64(writers * writes * chunkSize)
		pollBudget = 1 << 20
	)

	c := NewCounter()
	c.Start()

	stop := make(chan struct{})

	var polling sync.WaitGroup
	for i := 0; i < pollers; i++ {
		polling.Add(1)
		go func() {
			defer polling.Done()
			for n := 0; n < pollBudget; n++ {
				select {
				case <-stop:
					return
				default:
				}
				_ = c.AvgBytes()
				_ = c.AvgMbps()
				_ = c.AvgHumanize()
				_ = c.CurrentSpeed()
				_ = c.Total()
			}
		}()
	}

	var writing sync.WaitGroup
	for i := 0; i < writers; i++ {
		writing.Add(1)
		go func() {
			defer writing.Done()
			chunk := make([]byte, chunkSize)
			for j := 0; j < writes; j++ {
				n, err := c.Write(chunk)
				if err != nil {
					t.Errorf("Write returned error: %v", err)
					return
				}
				if n != chunkSize {
					t.Errorf("Write returned %d, want %d", n, chunkSize)
					return
				}
			}
		}()
	}

	writing.Wait()
	close(stop)
	polling.Wait()

	if got := c.Total(); got != wantTotal {
		t.Errorf("Total() = %d, want %d (lost updates indicate a broken counter)", got, wantTotal)
	}
}
