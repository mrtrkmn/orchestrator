package cmd

import (
	"runtime"
	"strconv"

	log "github.com/sirupsen/logrus"
)

// goroutineHook is a logrus hook that adds the current goroutine ID
// to every log entry as the "goroutine" field.
type goroutineHook struct{}

func (h *goroutineHook) Levels() []log.Level {
	return log.AllLevels
}

func (h *goroutineHook) Fire(entry *log.Entry) error {
	entry.Data["goroutine"] = goID()
	return nil
}

// goID returns the numeric goroutine ID of the caller.
// It uses runtime.Stack which is safe but not blazing fast;
// acceptable for log-rate calls.
func goID() string {
	var buf [64]byte
	// runtime.Stack writes "goroutine <id> [...]"
	n := runtime.Stack(buf[:], false)
	s := string(buf[:n])

	// Parse: skip "goroutine " prefix, read until the space after the id.
	const prefix = "goroutine "
	if len(s) <= len(prefix) {
		return "?"
	}
	s = s[len(prefix):]
	for i := 0; i < len(s); i++ {
		if s[i] == ' ' {
			if _, err := strconv.Atoi(s[:i]); err == nil {
				return s[:i]
			}
			break
		}
	}
	return "?"
}
