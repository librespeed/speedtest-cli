package defs

import (
	"fmt"

	log "github.com/sirupsen/logrus"
)

// NoFormatter is the formatter for logrus
type NoFormatter struct{}

// Format prints the log message without timestamp/log level etc.
func (f *NoFormatter) Format(entry *log.Entry) ([]byte, error) {
	return []byte(fmt.Sprintf("%s\n", entry.Message)), nil
}
