package defs

import (
	"bytes"
	"fmt"
	"io"
	"math/rand/v2"
	"sync"
	"sync/atomic"
	"time"
)

// BytesCounter implements io.Reader and io.Writer interface, for counting bytes being read/written in HTTP requests
type BytesCounter struct {
	start      time.Time
	pos        int
	total      uint64
	payload    []byte
	reader     io.ReadSeeker
	mebi       bool
	uploadSize int

	lock *sync.Mutex
}

func NewCounter() *BytesCounter {
	return &BytesCounter{
		lock: &sync.Mutex{},
	}
}

// Write implements io.Writer
func (c *BytesCounter) Write(p []byte) (int, error) {
	n := len(p)
	atomic.AddUint64(&c.total, uint64(n))
	return n, nil
}

// Read implements io.Reader
func (c *BytesCounter) Read(p []byte) (int, error) {
	c.lock.Lock()
	n, err := c.reader.Read(p)
	c.total += uint64(n)
	c.pos += n
	if c.pos == c.uploadSize {
		c.resetReader()
	}
	c.lock.Unlock()

	return n, err
}

// SetBase sets the base for dividing bytes into megabyte or mebibyte
func (c *BytesCounter) SetMebi(mebi bool) {
	c.mebi = mebi
}

// SetUploadSize sets the size of payload being uploaded
func (c *BytesCounter) SetUploadSize(uploadSize int) {
	c.uploadSize = uploadSize * 1024
}

// AvgBytes returns the average bytes/second
func (c *BytesCounter) AvgBytes() float64 {
	return float64(c.total) / time.Since(c.start).Seconds()
}

// AvgMbps returns the average mbits/second
func (c *BytesCounter) AvgMbps() float64 {
	var base float64 = 125000
	if c.mebi {
		base = 131072
	}
	return c.AvgBytes() / base
}

// AvgHumanize returns the average bytes/kilobytes/megabytes/gigabytes (or bytes/kibibytes/mebibytes/gibibytes) per second
func (c *BytesCounter) AvgHumanize() string {
	val := c.AvgBytes()

	var base float64 = 1000
	if c.mebi {
		base = 1024
	}

	if val < base {
		return fmt.Sprintf("%.2f bytes/s", val)
	} else if val/base < base {
		return fmt.Sprintf("%.2f KB/s", val/base)
	} else if val/base/base < base {
		return fmt.Sprintf("%.2f MB/s", val/base/base)
	} else {
		return fmt.Sprintf("%.2f GB/s", val/base/base/base)
	}
}

// Payload returns the pre-allocated random byte array for upload
func (c *BytesCounter) Payload() []byte {
	return c.payload
}

// GenerateBlob generates a random byte array of `uploadSize` in the `payload` field, and sets the `reader` field to
// read from it
func (c *BytesCounter) GenerateBlob() {
	c.payload = getRandomData(c.uploadSize)
	c.reader = bytes.NewReader(c.payload)
}

// resetReader resets the `reader` field to 0 position
func (c *BytesCounter) resetReader() (int64, error) {
	c.pos = 0
	return c.reader.Seek(0, 0)
}

// Start will set the `start` field to current time
func (c *BytesCounter) Start() {
	c.start = time.Now()
}

// Total returns the total bytes read/written
func (c *BytesCounter) Total() uint64 {
	return atomic.LoadUint64(&c.total)
}

// CurrentSpeed returns the current bytes/second
func (c *BytesCounter) CurrentSpeed() float64 {
	return float64(c.total) / time.Since(c.start).Seconds()
}

// SeekWrapper is a wrapper around io.Reader to give it a noop io.Seeker interface
type SeekWrapper struct {
	io.Reader
}

// Seek implements the io.Seeker interface
func (r *SeekWrapper) Seek(offset int64, whence int) (int64, error) {
	return offset, nil
}

// getAvg returns the average value of an float64 array
func getAvg(vals []float64) float64 {
	var total float64
	for _, v := range vals {
		total += v
	}

	return total / float64(len(vals))
}

// getRandomData returns an `length` sized array of random bytes using fast PRNG
func getRandomData(length int) []byte {
	data := make([]byte, length)
	// math/rand/v2 ChaCha8 generator is ~50x faster than crypto/rand for bulk data.
	// We use the default source which is automatically seeded.
	for i := 0; i < length; i += 8 {
		val := rand.Uint64()
		for j := 0; j < 8 && i+j < length; j++ {
			data[i+j] = byte(val >> (j * 8))
		}
	}
	return data
}
