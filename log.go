package copper

import (
	"log"
	"sync"
)

var logMutex sync.RWMutex
var logDebug *log.Logger
var logError *log.Logger

// DebugLog returns logger that is used for debug messages
func DebugLog() *log.Logger {
	logMutex.RLock()
	l := logDebug
	logMutex.RUnlock()
	return l
}

// ErrorLog returns logger that is used for error messages
func ErrorLog() *log.Logger {
	logMutex.RLock()
	l := logError
	logMutex.RUnlock()
	return l
}

// SetDebugLog sets logger that is used for debug messages
func SetDebugLog(l *log.Logger) *log.Logger {
	logMutex.Lock()
	logDebug, l = l, logDebug
	logMutex.Unlock()
	return l
}

// SetErrorLog sets logger that is used for error messages
func SetErrorLog(l *log.Logger) *log.Logger {
	logMutex.Lock()
	logError, l = l, logError
	logMutex.Unlock()
	return l
}
