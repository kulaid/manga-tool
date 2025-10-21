package main

import (
	"net/http"

	"github.com/gorilla/mux"
)

// RouteConfig holds information for registering a route
type RouteConfig struct {
	Path        string
	Handler     http.HandlerFunc
	Methods     []string
	RequireAuth bool
}

// RegisterRoutes registers all application routes with the router
func RegisterRoutes(r *mux.Router, ctx *AppContext) {
	// Define all routes (no auth middleware - using Authelia)
	routes := []RouteConfig{
		// Home - main upload page
		{"/", ctx.MangaHandler.IndexHandler, []string{"GET"}, false},

		// Manga processing routes
		{"/upload", ctx.MangaHandler.UploadHandler, []string{"POST"}, false},
		{"/process", ctx.MangaHandler.ProcessHandler, []string{"GET"}, false},
		{"/process/{id}", ctx.MangaHandler.ProcessDetailsHandler, []string{"GET"}, false},
		{"/process/{id}/cancel", ctx.MangaHandler.ProcessCancelHandler, []string{"POST"}, false},
		{"/processes", ctx.MangaHandler.ProcessesHandler, []string{"GET"}, false},

		// API routes for processes
		{"/api/processes", ctx.MangaOpHandler.ProcessesAPIHandler, []string{"GET"}, false},
		{"/api/processes/{id}/rerun", ctx.MangaOpHandler.ProcessRerunHandler, []string{"POST"}, false},
		{"/api/processes/{id}/cancel", ctx.MangaOpHandler.ProcessCancelHandler, []string{"POST"}, false},
		{"/api/processes/{id}/delete", ctx.MangaOpHandler.ProcessDeleteHandler, []string{"POST"}, false},
		{"/process-rerun/{id}", ctx.MangaOpHandler.ProcessRerunStartHandler, []string{"GET"}, false},

		// API routes for status and logs
		{"/api/status", ctx.MangaOpHandler.StatusHandler, []string{"GET"}, false},
		{"/api/submit-input", ctx.MangaHandler.SubmitInputHandler, []string{"POST"}, false},
		{"/api/logs", ctx.MangaOpHandler.LogsHandler, []string{"GET"}, false},

		// API routes for file and chapter operations
		{"/api/file-selection", ctx.MangaOpHandler.FileSelectionHandler, []string{"POST"}, false},
		{"/api/chapter-titles", ctx.MangaOpHandler.ChapterTitlesHandler, []string{"POST"}, false},
		{"/api/source-selection", ctx.MangaOpHandler.SourceSelectionHandler, []string{"POST"}, false},
		{"/api/prompt-response", ctx.MangaOpHandler.PromptResponseHandler, []string{"POST"}, false},
		{"/api/chapters", ctx.MangaOpHandler.ChaptersAPIHandler, []string{"GET"}, false},

		// Manga management routes
		{"/manage-manga", ctx.MangaOpHandler.ManageMangaHandler, []string{"GET"}, false},
		{"/update-metadata/{title}", ctx.MangaOpHandler.UpdateMetadataHandler, []string{"GET", "POST"}, false},
		{"/delete-manga/{title}", ctx.MangaOpHandler.DeleteMangaHandler, []string{"GET"}, false},
		{"/confirm-delete/{title}", ctx.MangaOpHandler.ConfirmDeleteHandler, []string{"GET", "POST"}, false},
		{"/delete-files/{title}", ctx.MangaOpHandler.DeleteFilesHandler, []string{"GET", "POST"}, false},
	}

	// Register all routes (no auth middleware - using Authelia)
	for _, route := range routes {
		r.HandleFunc(route.Path, route.Handler).Methods(route.Methods...)
	}

	// Static file handling with no-cache headers for development
	staticHandler := http.StripPrefix("/static/", http.FileServer(http.Dir("static")))
	r.PathPrefix("/static/").Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Disable caching for static files to ensure CSS/JS updates are immediately visible
		w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
		w.Header().Set("Pragma", "no-cache")
		w.Header().Set("Expires", "0")
		staticHandler.ServeHTTP(w, r)
	}))
}
