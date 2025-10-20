package utils

import (
	"log"
	"time"
)

// SimpleLogger is a basic logger that outputs to stdout for Docker logs
type SimpleLogger struct {
	ProcessID string
}

// NewSimpleLogger creates a new simple logger
func NewSimpleLogger(processID string) *SimpleLogger {
	return &SimpleLogger{
		ProcessID: processID,
	}
}

// Info logs an informational message
func (l *SimpleLogger) Info(msg string) {
	logMessage("INFO", msg)
}

// Warning logs a warning message
func (l *SimpleLogger) Warning(msg string) {
	logMessage("WARNING", msg)
}

// Error logs an error message
func (l *SimpleLogger) Error(msg string) {
	logMessage("ERROR", msg)
}

// UpdateProgress is a no-op for simple logger (progress is tracked via Process object)
func (l *SimpleLogger) UpdateProgress(percent int) {
	// Progress updates are handled by the Process object itself
	// This method exists for interface compatibility
}

// logMessage logs a message to stdout for Docker container logs
func logMessage(level, message string) {
	timestamp := time.Now().Format("2006-01-02 15:04:05")
	log.Printf("[%s] [%s] %s", timestamp, level, message)
}
