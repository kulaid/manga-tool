package utils

import (
	"log"
	"os"
	"sort"
)

// GetMangaTitles returns a list of manga titles from the manga directory
func GetMangaTitles(mangaDir string) ([]string, error) {
	var titles []string

	// Log config details for debugging
	log.Printf("Listing manga titles from mount: %s", mangaDir)

	// Check if mount point exists
	if _, err := os.Stat(mangaDir); os.IsNotExist(err) {
		log.Printf("Warning: Mount point %s does not exist", mangaDir)
		return titles, nil
	}

	// Read directory contents directly from mount
	files, err := os.ReadDir(mangaDir)
	if err != nil {
		log.Printf("Error reading mount directory: %v", err)
		return nil, err
	}

	// Extract directory names (manga titles)
	for _, file := range files {
		if file.IsDir() {
			titles = append(titles, file.Name())
			log.Printf("Found manga directory: %s", file.Name())
		}
	}

	log.Printf("Found %d manga titles from mount", len(titles))
	return titles, nil
}

// SortStringSlice sorts a string slice in alphabetical order
func SortStringSlice(s []string) {
	sort.Strings(s)
}

// DirExists checks if a directory exists
func DirExists(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return info.IsDir()
}

// GetDirPermissions returns the permissions of a directory as a string
func GetDirPermissions(path string) string {
	info, err := os.Stat(path)
	if err != nil {
		return err.Error()
	}
	return info.Mode().String()
}
