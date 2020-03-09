package defs

import (
	"fmt"
	"net/url"
	"path"
	"strings"
	"time"
)

const (
	TelemetryLevelDisabled = "disabled"
	TelemetryLevelBasic    = "basic"
	TelemetryLevelFull     = "full"
	TelemetryLevelDebug    = "debug"
)

// TelemetryLog is the logger for `log` field in telemetry data
type TelemetryLog struct {
	level   int
	content []string
}

// SetLevel sets the log level
func (t *TelemetryLog) SetLevel(level int) {
	t.level = level
}

// Logf logs when log level is higher than or equal to "full"
func (t *TelemetryLog) Logf(format string, a ...interface{}) {
	if t.level >= 2 {
		t.content = append(t.content, fmt.Sprintf("%s: %s", time.Now().String(), fmt.Sprintf(format, a...)))
	}
}

// Warnf logs when log level is higher than or equal to "full", with a WARN prefix
func (t *TelemetryLog) Warnf(format string, a ...interface{}) {
	if t.level >= 2 {
		t.content = append(t.content, fmt.Sprintf("%s: WARN: %s", time.Now().String(), fmt.Sprintf(format, a...)))
	}
}

// Verbosef logs when log level is higher than or equal to "debug"
func (t *TelemetryLog) Verbosef(format string, a ...interface{}) {
	if t.level >= 3 {
		t.content = append(t.content, fmt.Sprintf("%s: %s", time.Now().String(), fmt.Sprintf(format, a...)))
	}
}

// String returns the concatenated string of field `content`
func (t *TelemetryLog) String() string {
	return strings.Join(t.content, "\n")
}

// TelemetryExtra represents the `extra` field in the telemetry data
type TelemetryExtra struct {
	ServerName string `json:"server"`
	Extra      string `json:"extra,omitempty"`
}

// TelemetryServer represents the telemetry server configuration
type TelemetryServer struct {
	Level  string `json:"telemetryLevel"`
	Server string `json:"server"`
	Path   string `json:"path"`
	Share  string `json:"shareURL"`
}

// GetLevel translates the level string to corresponding integer value
func (t *TelemetryServer) GetLevel() int {
	switch t.Level {
	default:
		fallthrough
	case TelemetryLevelDisabled:
		return 0
	case TelemetryLevelBasic:
		return 1
	case TelemetryLevelFull:
		return 2
	case TelemetryLevelDebug:
		return 3
	}
}

// Disabled checks if the telemetry level is "disabled"
func (t *TelemetryServer) Disabled() bool {
	return t.Level == TelemetryLevelDisabled
}

// Basic checks if the telemetry level is "basic"
func (t *TelemetryServer) Basic() bool {
	return t.Level == TelemetryLevelBasic
}

// Full checks if the telemetry level is "full"
func (t *TelemetryServer) Full() bool {
	return t.Level == TelemetryLevelFull
}

// Debug checks if the telemetry level is "debug"
func (t *TelemetryServer) Debug() bool {
	return t.Level == TelemetryLevelDebug
}

// GetPath parses and returns the telemetry path
func (t *TelemetryServer) GetPath() (*url.URL, error) {
	u, err := url.Parse(t.Server)
	if err != nil {
		return nil, err
	}
	u.Path = path.Join(u.Path, t.Path)
	return u, nil
}

// GetShare parses and returns the telemetry share path
func (t *TelemetryServer) GetShare() (*url.URL, error) {
	u, err := url.Parse(t.Server)
	if err != nil {
		return nil, err
	}
	u.Path = path.Join(u.Path, t.Share) + "/"
	return u, nil
}
