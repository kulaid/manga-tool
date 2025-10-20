package internal

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// ProcessType represents different types of processes
type ProcessType string

const (
	ProcessTypeMetadataUpdate ProcessType = "metadata_update"
	ProcessTypeCacheWarm      ProcessType = "cache_warm"
	ProcessTypeDelete         ProcessType = "delete"
	ProcessTypeProcess        ProcessType = "process"
)

// ProcessStatus represents the current status of a process
type ProcessStatus string

const (
	ProcessStatusRunning   ProcessStatus = "running"
	ProcessStatusComplete  ProcessStatus = "complete"
	ProcessStatusFailed    ProcessStatus = "failed"
	ProcessStatusCancelled ProcessStatus = "cancelled"
)

// Process represents a running process in the system
type Process struct {
	ID          string                 `json:"id"`                 // Unique identifier for the process
	Type        ProcessType            `json:"type"`               // Type of process
	Title       string                 `json:"title"`              // Title of the manga being processed
	Status      ProcessStatus          `json:"status"`             // Current status
	Progress    int                    `json:"progress"`           // Current progress (0-100)
	Total       int                    `json:"total"`              // Total items to process
	Message     string                 `json:"message"`            // Current status message
	Error       string                 `json:"error"`              // Error message if failed
	StartTime   time.Time              `json:"start_time"`         // When the process started
	EndTime     time.Time              `json:"end_time"`           // When the process completed/failed
	IsWaiting   bool                   `json:"is_waiting"`         // Whether the process is waiting for user input
	InputPrompt string                 `json:"input_prompt"`       // Current input prompt if waiting
	InputType   string                 `json:"input_type"`         // Type of input needed
	Metadata    map[string]interface{} `json:"metadata,omitempty"` // Additional process metadata

	// These fields won't be persisted to JSON
	CancelFunc      func()    `json:"-"` // Function to cancel the process
	ForceCancel     func()    `json:"-"` // Function to force cancel the process
	cancelOnce      sync.Once // Ensure cancel is only called once
	forceCancelOnce sync.Once // Ensure force cancel is only called once
}

// Series represents a manga series
type Series struct {
	Name  string
	Manga []*Manga
}

// Manga represents a single manga file or directory
type Manga struct {
	Name string
	Path string
}

// ProcessManager handles all running processes
type ProcessManager struct {
	processes   map[string]*Process
	mu          sync.RWMutex
	storagePath string
}

var (
	manager *ProcessManager
	once    sync.Once
)

// GetProcessManager returns the singleton process manager
func GetProcessManager() *ProcessManager {
	once.Do(func() {
		// Set default storage path to data directory in current path
		storageDir := "./data"
		if err := os.MkdirAll(storageDir, 0755); err != nil {
			fmt.Printf("Warning: Failed to create data directory: %v\n", err)
		}

		storagePath := filepath.Join(storageDir, "processes.json")

		manager = &ProcessManager{
			processes:   make(map[string]*Process),
			storagePath: storagePath,
		}

		// Try to load existing processes on startup
		if err := manager.LoadProcesses(); err != nil {
			fmt.Printf("Warning: Failed to load process history: %v\n", err)
		}

		// Mark any running processes as failed since they were interrupted by restart
		for _, p := range manager.processes {
			if p.Status == ProcessStatusRunning {
				p.Status = ProcessStatusFailed
				p.Message = "Process interrupted by service restart"
				p.EndTime = time.Now()
			}
		}

		// Save processes to disk
		if err := manager.SaveProcesses(); err != nil {
			fmt.Printf("Warning: Failed to save updated process states: %v\n", err)
		}
	})
	return manager
}

// SaveProcesses persists all processes to disk
func (pm *ProcessManager) SaveProcesses() error {
	pm.mu.RLock()
	defer pm.mu.RUnlock()

	// Convert processes map to slice for JSON serialization
	processes := make([]*Process, 0, len(pm.processes))
	for _, p := range pm.processes {
		processes = append(processes, p)
	}

	// Marshal to JSON with indentation for readability
	data, err := json.MarshalIndent(processes, "", "  ")
	if err != nil {
		return fmt.Errorf("error marshaling processes: %v", err)
	}

	// Write to file
	if err := os.WriteFile(pm.storagePath, data, 0644); err != nil {
		return fmt.Errorf("error writing processes to file: %v", err)
	}

	return nil
}

// LoadProcesses loads processes from disk
func (pm *ProcessManager) LoadProcesses() error {
	// Check if the file exists
	if _, err := os.Stat(pm.storagePath); os.IsNotExist(err) {
		// No file exists yet, not an error
		return nil
	}

	// Read the file
	data, err := os.ReadFile(pm.storagePath)
	if err != nil {
		return fmt.Errorf("error reading processes file: %v", err)
	}

	// Unmarshal the JSON
	var processes []*Process
	if err := json.Unmarshal(data, &processes); err != nil {
		return fmt.Errorf("error unmarshaling processes: %v", err)
	}

	// Add to the map
	pm.mu.Lock()
	defer pm.mu.Unlock()

	for _, p := range processes {
		pm.processes[p.ID] = p
	}

	return nil
}

// NewProcess creates a new process and registers it with the manager
func (pm *ProcessManager) NewProcess(processType ProcessType, title string) *Process {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	// Generate unique ID using timestamp and title
	id := fmt.Sprintf("%s-%d", processType, time.Now().UnixNano())

	process := &Process{
		ID:        id,
		Type:      processType,
		Title:     title,
		Status:    ProcessStatusRunning,
		StartTime: time.Now(),
	}

	pm.processes[id] = process

	// Save processes to disk after adding new process
	go pm.SaveProcesses()

	return process
}

// GetProcess returns a process by ID
func (pm *ProcessManager) GetProcess(id string) (*Process, bool) {
	pm.mu.RLock()
	defer pm.mu.RUnlock()

	process, exists := pm.processes[id]
	return process, exists
}

// ListProcesses returns all processes
func (pm *ProcessManager) ListProcesses() []*Process {
	pm.mu.RLock()
	defer pm.mu.RUnlock()

	processes := make([]*Process, 0, len(pm.processes))
	for _, p := range pm.processes {
		processes = append(processes, p)
	}
	return processes
}

// ListActiveProcesses returns only running processes
func (pm *ProcessManager) ListActiveProcesses() []*Process {
	pm.mu.RLock()
	defer pm.mu.RUnlock()

	processes := make([]*Process, 0)
	for _, p := range pm.processes {
		if p.Status == ProcessStatusRunning {
			processes = append(processes, p)
		}
	}
	return processes
}

// UpdateProcess updates a process's status
func (pm *ProcessManager) UpdateProcess(id string, update func(*Process)) bool {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	if process, exists := pm.processes[id]; exists {
		update(process)

		// Save processes to disk after updating
		go pm.SaveProcesses()

		return true
	}
	return false
}

// CompleteProcess marks a process as complete
func (pm *ProcessManager) CompleteProcess(id string) bool {
	return pm.UpdateProcess(id, func(p *Process) {
		p.Status = ProcessStatusComplete
		p.Progress = p.Total
		p.EndTime = time.Now()
		p.Message = "Processing complete!"
		// Clear waiting state when process completes
		p.SetWaiting(false, "", "")
	})
}

// FailProcess marks a process as failed
func (pm *ProcessManager) FailProcess(id string, err string) bool {
	return pm.UpdateProcess(id, func(p *Process) {
		p.Status = ProcessStatusFailed
		p.Error = err
		p.EndTime = time.Now()
		// Clear waiting state when process fails
		p.SetWaiting(false, "", "")
	})
}

// CancelProcess attempts to cancel a running process gracefully
func (pm *ProcessManager) CancelProcess(id string) bool {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	if process, exists := pm.processes[id]; exists && process.Status == ProcessStatusRunning {
		if process.CancelFunc != nil {
			// Use sync.Once to ensure cancel is only called once
			process.cancelOnce.Do(func() {
				process.CancelFunc()
			})
		}
		process.Status = ProcessStatusCancelled
		process.EndTime = time.Now()
		process.Message = "Process cancelled"
		// Clear waiting state when process is cancelled
		process.SetWaiting(false, "", "")

		// Save processes to disk after cancelling
		go pm.SaveProcesses()

		return true
	}
	return false
}

// ForceCancelProcess forcefully cancels a running process
func (pm *ProcessManager) ForceCancelProcess(id string) bool {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	if process, exists := pm.processes[id]; exists && process.Status == ProcessStatusRunning {
		if process.ForceCancel != nil {
			// Use sync.Once to ensure force cancel is only called once
			process.forceCancelOnce.Do(func() {
				process.ForceCancel()
			})
		}
		process.Status = ProcessStatusCancelled
		process.EndTime = time.Now()
		process.Message = "Process forcefully cancelled"
		// Clear waiting state when process is force cancelled
		process.SetWaiting(false, "", "")

		// Save processes to disk after cancelling
		go pm.SaveProcesses()

		return true
	}
	return false
}

// DeleteProcess removes a process from history
func (pm *ProcessManager) DeleteProcess(id string) bool {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	if process, exists := pm.processes[id]; exists {
		// Only allow deletion of non-running processes
		if process.Status != ProcessStatusRunning {
			delete(pm.processes, id)

			// Save processes to disk after deletion
			go pm.SaveProcesses()

			return true
		}
	}
	return false
}

// CleanupOldProcesses removes completed/failed processes older than the given duration
func (pm *ProcessManager) CleanupOldProcesses(age time.Duration) {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	now := time.Now()
	deleted := false

	for id, process := range pm.processes {
		if process.Status != ProcessStatusRunning {
			if now.Sub(process.EndTime) > age {
				delete(pm.processes, id)
				deleted = true
			}
		}
	}

	// Save processes to disk if any were deleted
	if deleted {
		go pm.SaveProcesses()
	}
}

// Update updates a process's progress and message
func (p *Process) Update(progress int, total int, message string) {
	p.Progress = progress
	p.Total = total
	p.Message = message
}

// SetWaiting sets a process's waiting status and input details
func (p *Process) SetWaiting(waiting bool, prompt string, inputType string) {
	p.IsWaiting = waiting
	p.InputPrompt = prompt
	p.InputType = inputType
}

// Duration returns the duration of the process
func (p *Process) Duration() time.Duration {
	if p.Status == ProcessStatusRunning {
		return time.Since(p.StartTime)
	}
	return p.EndTime.Sub(p.StartTime)
}

// ProgressPercentage returns the progress as a percentage
func (p *Process) ProgressPercentage() int {
	if p.Total <= 0 {
		return 0
	}
	return (p.Progress * 100) / p.Total
}
