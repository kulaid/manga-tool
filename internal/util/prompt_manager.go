package util

import (
	"fmt"
	"manga-tool/internal"
	"sync"
	"time"
)

// PromptType defines the allowed types of prompts
type PromptType int

const (
	// FileDeletionPrompt is for selecting files to delete
	FileDeletionPrompt PromptType = iota
	// ChapterListPrompt is for selecting which chapter list to use
	ChapterListPrompt
	// ChapterTitlePrompt is for entering missing chapter titles
	ChapterTitlePrompt
)

// PromptManager handles all user prompts in a centralized way
type PromptManager struct {
	skipAllChapterTitles bool
	activePrompt chan string
	mu           sync.Mutex
	logger       Logger
	timeout      time.Duration
	process      *internal.Process
}

// NewPromptManager creates a new prompt manager
func NewPromptManager(logger Logger, timeout time.Duration, process *internal.Process) *PromptManager {
	if timeout <= 0 {
		timeout = 5 * time.Minute // Default timeout
	}

	return &PromptManager{
		activePrompt: make(chan string, 1),
		logger:       logger,
		timeout:      timeout,
		process:      process,
	}
}

// ShowPrompt sends a prompt to the client and waits for a response
func (pm *PromptManager) ShowPrompt(pType PromptType, details string) string {
	   // If user has chosen to skip all chapter titles, auto-return empty for ChapterTitlePrompt
	   if pType == ChapterTitlePrompt && pm.skipAllChapterTitles {
		   if pm.logger != nil {
			   pm.logger.Info("Skipping chapter title prompt due to skipAllChapterTitles flag")
		   }
		   if pm.process != nil {
			   pm.process.SetWaiting(false, "", "")
		   }
		   return ""
	   }
	pm.mu.Lock()

	var promptMsg string
	var inputType = "text"

	switch pType {
	case FileDeletionPrompt:
		promptMsg = "Which files would you like to delete?"
	case ChapterListPrompt:
		promptMsg = "Which chapter list would you like to use?"
	case ChapterTitlePrompt:
		promptMsg = "Enter missing chapter title:"
	}

	if details != "" {
		promptMsg = promptMsg + "\n" + details
	}

	// Update process status
	if pm.process != nil {
		pm.process.SetWaiting(true, promptMsg, inputType)
	}

	// Log the prompt
	if pm.logger != nil {
		pm.logger.Info(promptMsg)
	}

	pm.mu.Unlock()

	// Wait for the response with timeout
	var response string
	select {
	case response = <-pm.activePrompt:
		if pm.logger != nil {
			pm.logger.Info(fmt.Sprintf("Received response: %s", response))
		}
	case <-time.After(pm.timeout):
		if pm.logger != nil {
			pm.logger.Warning(fmt.Sprintf("Prompt timed out after %v", pm.timeout))
		}
		response = ""
	}

	// Clear the waiting input status
	if pm.process != nil {
		pm.process.SetWaiting(false, "", "")
	}

	return response
}

// ShowCustomPrompt sends a custom prompt with specific prompt type and ID
func (pm *PromptManager) ShowCustomPrompt(promptMsg string, inputType string, promptID string) string {
	pm.mu.Lock()

	// Update process status
	if pm.process != nil {
		pm.process.SetWaiting(true, promptMsg, inputType)
	}

	// Log the prompt
	if pm.logger != nil {
		pm.logger.Info(fmt.Sprintf("Custom prompt [%s]: %s", promptID, promptMsg))
	}

	pm.mu.Unlock()

	// Wait for the response with timeout
	var response string
	select {
	case response = <-pm.activePrompt:
		if pm.logger != nil {
			pm.logger.Info(fmt.Sprintf("Received response for [%s]: %s", promptID, response))
		}
	case <-time.After(pm.timeout):
		if pm.logger != nil {
			pm.logger.Warning(fmt.Sprintf("Prompt [%s] timed out after %v", promptID, pm.timeout))
		}
		response = ""
	}

	// Clear the waiting input status
	if pm.process != nil {
		pm.process.SetWaiting(false, "", "")
	}

	return response
}

// SubmitResponse submits a response to the active prompt
func (pm *PromptManager) SubmitResponse(response string) {
	   // If user submitted the skip-all-titles magic string, set the skip flag
	   if response == "__SKIP_ALL_TITLES__" {
		   pm.skipAllChapterTitles = true
		   if pm.logger != nil {
			   pm.logger.Info("User requested to skip all future chapter title prompts.")
		   }
		   // Immediately submit empty string as the response for the current prompt
		   select {
		   case pm.activePrompt <- "":
			   // Response submitted as empty
		   default:
			   // No active prompt waiting
		   }
		   return
	   }
	pm.mu.Lock()
	defer pm.mu.Unlock()

	select {
	case pm.activePrompt <- response:
		// Response submitted successfully
	default:
		// No active prompt waiting, log an error
		if pm.logger != nil {
			pm.logger.Warning("Received response but no active prompt is waiting")
		}
	}
}

// SetProcess updates the process associated with this prompt manager
func (pm *PromptManager) SetProcess(process *internal.Process) {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	pm.process = process
	if pm.logger != nil {
		pm.logger.Info(fmt.Sprintf("PromptManager now associated with process: %s", process.ID))
	}
}

// AskForFileDeletion shows the file deletion prompt
func (pm *PromptManager) AskForFileDeletion(fileList string) string {
	return pm.ShowPrompt(FileDeletionPrompt, fileList)
}

// AskForChapterList shows the chapter list selection prompt
func (pm *PromptManager) AskForChapterList(listDetails string) string {
	return pm.ShowPrompt(ChapterListPrompt, listDetails)
}

// AskForChapterTitle shows the chapter title prompt
func (pm *PromptManager) AskForChapterTitle(chapterInfo string) string {
	return pm.ShowPrompt(ChapterTitlePrompt, chapterInfo)
}
