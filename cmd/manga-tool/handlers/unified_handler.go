package handlers

import (
	"html/template"

	"github.com/gorilla/sessions"

	"manga-tool/cmd/manga-tool/utils"
	"manga-tool/internal"
	"manga-tool/internal/util"
)

// UnifiedMangaHandler contains all dependencies for manga operations
// This replaces the separate MangaHandler and MangaOperationHandler structs
type UnifiedMangaHandler struct {
	Config         *utils.AppConfig
	ProcessManager *internal.ProcessManager
	SessionStore   *sessions.CookieStore
	Logger         func(level, message string)
	CurrentProcess *string
	Templates      *template.Template
	WebInput       func(processID, prompt, inputType string) string
	PromptManager  *util.PromptManager
}

// NewUnifiedMangaHandler creates a new unified manga handler
func NewUnifiedMangaHandler(
	config *utils.AppConfig,
	processManager *internal.ProcessManager,
	sessionStore *sessions.CookieStore,
	logger func(level, message string),
	currentProcess *string,
	templates *template.Template,
	webInput func(processID, prompt, inputType string) string,
	promptManager *util.PromptManager,
) *UnifiedMangaHandler {
	return &UnifiedMangaHandler{
		Config:         config,
		ProcessManager: processManager,
		SessionStore:   sessionStore,
		Logger:         logger,
		CurrentProcess: currentProcess,
		Templates:      templates,
		WebInput:       webInput,
		PromptManager:  promptManager,
	}
}
