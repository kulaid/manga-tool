package cache

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// MangaSource represents saved source URLs for a manga
type MangaSource struct {
	MangaReader string `json:"mangareader,omitempty"`
	MangaDex    string `json:"mangadex,omitempty"`
	DownloadURL string `json:"download_url,omitempty"`
	IsOneshot   bool   `json:"is_oneshot,omitempty"`
}

// ChapterTitle represents a title for a specific chapter
type ChapterTitle struct {
	Number float64 `json:"number"`
	Title  string  `json:"title"`
}

// MangaTitles stores chapter titles for a manga
type MangaTitles struct {
	Titles []ChapterTitle `json:"titles"`
}

// SourceCache stores all manga sources
type SourceCache map[string]MangaSource

// TitlesCache stores all manga chapter titles
type TitlesCache map[string]MangaTitles

// CacheFile is the path to the cache file
var CacheFile string

// TitlesCacheFile is the path to the chapter titles cache file
var TitlesCacheFile string

// Initialize sets the cache file path
func Initialize(appDir string) {
	CacheFile = filepath.Join(appDir, "manga_sources.json")
	TitlesCacheFile = filepath.Join(appDir, "chapter_titles.json")
}

// LoadCache loads the sources cache from disk
func LoadCache() (SourceCache, error) {
	cache := make(SourceCache)

	if _, err := os.Stat(CacheFile); os.IsNotExist(err) {
		return cache, nil
	}

	data, err := os.ReadFile(CacheFile)
	if err != nil {
		return cache, err
	}

	err = json.Unmarshal(data, &cache)
	return cache, err
}

// SaveCache saves the sources cache to disk
func SaveCache(cache SourceCache) error {
	data, err := json.MarshalIndent(cache, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(CacheFile, data, 0644)
}

// GetCachedSources gets cached source links for a manga title
func GetCachedSources(title string) (MangaSource, error) {
	cache, err := LoadCache()
	if err != nil {
		return MangaSource{}, err
	}

	return cache[title], nil
}

// SaveSources saves all source information for a manga title, including oneshot flag
// Parameters can be empty strings to skip updating those fields (preserves existing values)
// The isOneshot flag is always updated since it's a checkbox (true/false are both valid states)
func SaveSources(title, mangareaderURL, mangadexURL, downloadURL string, isOneshot bool) error {
	cache, err := LoadCache()
	if err != nil {
		return err
	}

	// Get existing or create new
	source, exists := cache[title]
	if !exists {
		source = MangaSource{}
	}

	// Update non-empty values only (preserves existing values if empty string passed)
	if mangareaderURL != "" {
		source.MangaReader = mangareaderURL
	}
	if mangadexURL != "" {
		source.MangaDex = mangadexURL
	}
	if downloadURL != "" {
		source.DownloadURL = downloadURL
	}
	// Always update oneshot flag (it's a checkbox, so both true and false are valid)
	source.IsOneshot = isOneshot

	cache[title] = source
	return SaveCache(cache)
}

// LoadTitlesCache loads the chapter titles cache from disk
func LoadTitlesCache() (TitlesCache, error) {
	cache := make(TitlesCache)

	if _, err := os.Stat(TitlesCacheFile); os.IsNotExist(err) {
		return cache, nil
	}

	data, err := os.ReadFile(TitlesCacheFile)
	if err != nil {
		return cache, err
	}

	err = json.Unmarshal(data, &cache)
	return cache, err
}

// SaveTitlesCache saves the chapter titles cache to disk
func SaveTitlesCache(cache TitlesCache) error {
	data, err := json.MarshalIndent(cache, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(TitlesCacheFile, data, 0644)
}

// GetCachedChapterTitles gets cached chapter titles for a manga
func GetCachedChapterTitles(title string) (map[float64]string, error) {
	cache, err := LoadTitlesCache()
	if err != nil {
		return nil, err
	}

	mangaTitles, exists := cache[title]
	if !exists {
		return make(map[float64]string), nil
	}

	// Convert from slice to map
	result := make(map[float64]string)
	for _, ch := range mangaTitles.Titles {
		result[ch.Number] = ch.Title
	}

	return result, nil
}

// SaveChapterTitles saves chapter titles for a manga
func SaveChapterTitles(title string, chapterTitles map[float64]string) error {
	cache, err := LoadTitlesCache()
	if err != nil {
		cache = make(TitlesCache)
	}

	// Create or update the manga titles entry
	mangaTitles, exists := cache[title]
	if !exists {
		mangaTitles = MangaTitles{
			Titles: []ChapterTitle{},
		}
	}

	// Convert from map to slice
	titles := []ChapterTitle{}
	for num, title := range chapterTitles {
		titles = append(titles, ChapterTitle{
			Number: num,
			Title:  title,
		})
	}

	mangaTitles.Titles = titles
	cache[title] = mangaTitles
	return SaveTitlesCache(cache)
}
