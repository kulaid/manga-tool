package main

import (
	"fmt"
	"html/template"
	"log"
	"sync"
	"time"

	"manga-tool/cmd/manga-tool/handlers"
	"manga-tool/cmd/manga-tool/utils"
	"manga-tool/internal"
	"manga-tool/internal/util"
)

// AppContext holds all the application state and dependencies
// This replaces most of the global variables in main.go
// No auth - using Authelia for authentication
type AppContext struct {
	// Configuration
	Config *utils.AppConfig

	// Core services
	ProcessManager *internal.ProcessManager
	Templates      *template.Template
	PromptManager  *util.PromptManager

	// State
	CurrentProcessID string
	StartTime        time.Time

	// User input handling
	UserPrompts map[string]chan string
	InputLock   sync.Mutex

	// Handlers (no auth handler - using Authelia)
	MangaHandler   *handlers.MangaHandler
	MangaOpHandler *handlers.MangaOperationHandler
}

// NewAppContext creates a new application context with all dependencies initialized
func NewAppContext(config *utils.AppConfig) (*AppContext, error) {
	// Initialize core services (no session store - using Authelia)
	templates, err := utils.Initialize(config)
	if err != nil {
		return nil, err
	}

	processManager := internal.GetProcessManager()

	// Create the context
	ctx := &AppContext{
		Config:         config,
		ProcessManager: processManager,
		Templates:      templates,
		StartTime:      time.Now(),
		UserPrompts:    make(map[string]chan string),
	}

	// Initialize prompt manager with simple logger
	simpleLogger := utils.NewSimpleLogger("")
	ctx.PromptManager = util.NewPromptManager(simpleLogger, config.PromptTimeout, nil)

	// Initialize handlers (no auth - using Authelia)
	ctx.MangaHandler = &handlers.MangaHandler{
		Config:         config,
		ProcessManager: processManager,
		Logger:         logMessage,
		CurrentProcess: &ctx.CurrentProcessID,
		Templates:      templates,
		WebInput:       ctx.WebInput,
		PromptManager:  ctx.PromptManager,
	}

	ctx.MangaOpHandler = &handlers.MangaOperationHandler{
		Config:         config,
		ProcessManager: processManager,
		Logger:         logMessage,
		Templates:      templates,
		UserPrompts:    &ctx.UserPrompts,
		InputLock:      &ctx.InputLock,
		WebInput:       ctx.WebInput,
	}

	return ctx, nil
}

// logMessage logs a message to stdout for Docker logs
func logMessage(level, message string) {
	timestamp := time.Now().Format("2006-01-02 15:04:05")
	log.Printf("[%s] [%s] %s", timestamp, level, message)
}

// WebInput gets input from the web interface
func (ctx *AppContext) WebInput(processID, prompt, inputType string) string {
	// Create a unique ID for this prompt
	promptID := fmt.Sprintf("%s-%d", processID, time.Now().UnixNano())

	// Create a channel for user input
	ctx.InputLock.Lock()
	ctx.UserPrompts[promptID] = make(chan string, 1)
	inputChan := ctx.UserPrompts[promptID]
	ctx.InputLock.Unlock()
	defer func() {
		ctx.InputLock.Lock()
		delete(ctx.UserPrompts, promptID)
		ctx.InputLock.Unlock()
	}()

	// Get the process object to set waiting state
	proc, exists := ctx.ProcessManager.GetProcess(processID)
	if !exists {
		logMessage("ERROR", fmt.Sprintf("No active process found for input prompt (ID: %s)", processID))
		return ""
	}

	// Set process to waiting state
	proc.SetWaiting(true, prompt, inputType)

	// Log that we're waiting for input
	logMessage("INFO", "Waiting for user input: "+prompt)

	// Create a channel that will be closed if process is cancelled
	cancelledChan := make(chan struct{})

	// Monitor for process cancellation in a goroutine
	go func() {
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				// Check if process still exists and is not cancelled
				if currentProc, exists := ctx.ProcessManager.GetProcess(processID); !exists ||
					currentProc.Status == "cancelled" || currentProc.Status == "failed" {
					close(cancelledChan)
					return
				}
			case <-cancelledChan:
				return
			}
		}
	}()

	// Wait for input with timeout or cancellation
	select {
	case input := <-inputChan:
		// Reset waiting state
		proc.SetWaiting(false, "", "")
		logMessage("INFO", "Received user input: "+input)
		return input
	case <-cancelledChan:
		// Process was cancelled
		proc.SetWaiting(false, "", "")
		logMessage("INFO", "Input cancelled due to process cancellation")
		return ""
	case <-time.After(ctx.Config.PromptTimeout):
		// Timeout occurred
		proc.SetWaiting(false, "", "")
		logMessage("WARNING", "Input prompt timed out after "+ctx.Config.PromptTimeout.String())
		return ""
	}
}
