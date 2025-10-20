package scraper

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	// NOTE: If you get import errors for this package, run: go get golang.org/x/net
	// This needs to be installed on your Ubuntu server with: go get -u golang.org/x/net/html
	"golang.org/x/net/html"
)

// Logger interface for handling logs
type Logger interface {
	Info(msg string)
	Warning(msg string)
	Error(msg string)
}

// WebInput is a function type for getting user input
type WebInput func(prompt, inputType string) string

// MangaSource represents a manga source
type MangaSource struct {
	URL        string
	SourceType string
}

// ChapterTitles is a map of chapter numbers to titles
type ChapterTitles map[float64]string

// SavedSources maps manga titles to their sources
type SavedSources map[string]map[string]string

// ChapterPattern is the regex for matching chapter titles
var ChapterPattern = regexp.MustCompile(`Chapter (\d+(?:\.\d+)?):?\s*(.+?)$`)

// LoadSources loads saved sources from a JSON file
func LoadSources(filename string) (SavedSources, error) {
	sources := make(SavedSources)
	data, err := os.ReadFile(filename)
	if err != nil {
		return sources, err
	}

	if err := json.Unmarshal(data, &sources); err != nil {
		return sources, err
	}

	return sources, nil
}

// SaveSources saves sources to a JSON file
func SaveSources(filename string, sources SavedSources) error {
	data, err := json.MarshalIndent(sources, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(filename, data, 0644)
}

// GetCachedSources gets cached sources for a manga title
func GetCachedSources(filename, mangaTitle string) map[string]string {
	sources, err := LoadSources(filename)
	if err != nil {
		return nil
	}

	return sources[mangaTitle]
}

// SaveMangaSources saves manga sources for a title
func SaveMangaSources(filename, mangaTitle, mangareaderURL, mangadexURL string) error {
	sources, err := LoadSources(filename)
	if err != nil {
		sources = make(SavedSources)
	}

	if _, ok := sources[mangaTitle]; !ok {
		sources[mangaTitle] = make(map[string]string)
	}

	if mangareaderURL != "" {
		sources[mangaTitle]["mangareader"] = mangareaderURL
	}

	if mangadexURL != "" {
		sources[mangaTitle]["mangadex"] = mangadexURL
	}

	return SaveSources(filename, sources)
}

// GetMangaReaderChapters fetches chapter titles from MangaReader.to
func GetMangaReaderChapters(url string, logger Logger, webInput WebInput) ChapterTitles {
	if logger != nil {
		logger.Info("Fetching chapter titles from MangaReader.to...")
	}

	chapters := make(ChapterTitles)

	client := &http.Client{
		Timeout: 30 * time.Second,
	}

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		if logger != nil {
			logger.Error(fmt.Sprintf("Error creating request: %v", err))
		}
		return chapters
	}

	// Set headers to mimic a browser
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/91.0.4472.124 Safari/537.36")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,image/webp,*/*;q=0.8")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	req.Header.Set("Connection", "keep-alive")
	req.Header.Set("Referer", "https://mangareader.to/")
	req.Header.Set("Cookie", "chapter-lang=en; isAdult=1; tad=1")

	// Also set cookies the standard way
	cookies := []*http.Cookie{
		{Name: "chapter-lang", Value: "en", Domain: ".mangareader.to", Path: "/"},
		{Name: "isAdult", Value: "1", Domain: ".mangareader.to", Path: "/"},
		{Name: "tad", Value: "1", Domain: ".mangareader.to", Path: "/"},
	}

	for _, cookie := range cookies {
		req.AddCookie(cookie)
	}

	resp, err := client.Do(req)
	if err != nil {
		if logger != nil {
			logger.Error(fmt.Sprintf("Error fetching page: %v", err))
		}
		return chapters
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		if logger != nil {
			logger.Error(fmt.Sprintf("Error status code: %d", resp.StatusCode))
		}
		return chapters
	}

	// Parse HTML
	doc, err := html.Parse(resp.Body)
	if err != nil {
		if logger != nil {
			logger.Error(fmt.Sprintf("Error parsing HTML: %v", err))
		}
		return chapters
	}

	// Find chapter lists (will only find en-chapters due to ID filtering)
	chapterLists := findChapterLists(doc)

	if len(chapterLists) == 0 {
		if logger != nil {
			logger.Error("No chapter lists found")
		}
		return chapters
	}

	// Use the first (and likely only) chapter list found
	chosenList := chapterLists[0]
	if logger != nil {
		logger.Info("Using English chapter list")
	}

	// Extract chapters from the chosen list
	chapterItems := findChapterItems(chosenList)
	for idx, item := range chapterItems {
		chapterText := getTextContent(item)
		chapterText = strings.Replace(chapterText, "Read", "", -1)
		chapterText = strings.TrimSpace(chapterText)

		match := ChapterPattern.FindStringSubmatch(chapterText)
		if len(match) > 2 {
			chapterNum, err := strconv.ParseFloat(match[1], 64)
			if err != nil {
				chapterNum = float64(idx + 1)
			}

			chapterTitle := strings.TrimSpace(match[2])

			// Strip out redundant "Chapter {number} - " prefix from the title
			// This handles cases like "Chapter 383: Chapter 383 - Endpoint"
			// where we only want "Endpoint"
			redundantPrefixPattern := regexp.MustCompile(`^Chapter\s+\d+(?:\.\d+)?\s*-\s*`)
			chapterTitle = redundantPrefixPattern.ReplaceAllString(chapterTitle, "")
			chapterTitle = strings.TrimSpace(chapterTitle)

			if chapterTitle != "" {
				chapters[chapterNum] = chapterTitle
			} else {
				chapters[chapterNum] = chapterText
			}
		} else {
			chapters[float64(idx+1)] = chapterText
		}
	}

	if logger != nil {
		logger.Info(fmt.Sprintf("Found %d chapter titles from MangaReader.to", len(chapters)))
	}

	return chapters
}

// MangaDexChapterResponse represents the MangaDex API response
type MangaDexChapterResponse struct {
	Result string `json:"result"`
	Data   []struct {
		Attributes struct {
			Chapter string `json:"chapter"`
			Title   string `json:"title"`
		} `json:"attributes"`
	} `json:"data"`
	Total int `json:"total"`
}

// GetMangaDexChapters fetches chapter titles from MangaDex
func GetMangaDexChapters(url string, logger Logger) ChapterTitles {
	if logger != nil {
		logger.Info("Fetching chapter titles from MangaDex...")
	}

	chapters := make(ChapterTitles)

	// Extract manga ID from URL
	idRegex := regexp.MustCompile(`/title/([0-9a-fA-F-]+)`)
	match := idRegex.FindStringSubmatch(url)
	if len(match) < 2 {
		if logger != nil {
			logger.Error("Invalid MangaDex link. Could not extract manga ID.")
		}
		return chapters
	}

	mangaID := match[1]
	if logger != nil {
		logger.Info(fmt.Sprintf("Manga ID extracted: %s", mangaID))
	}

	client := &http.Client{
		Timeout: 30 * time.Second,
	}

	allChapters := []MangaDexChapterResponse{}
	limit := 100
	offset := 0

	for {
		apiURL := fmt.Sprintf("https://api.mangadex.org/chapter?manga=%s&translatedLanguage[]=en&limit=%d&offset=%d&order[chapter]=asc",
			mangaID, limit, offset)

		req, err := http.NewRequest("GET", apiURL, nil)
		if err != nil {
			if logger != nil {
				logger.Error(fmt.Sprintf("Error creating request: %v", err))
			}
			break
		}

		resp, err := client.Do(req)
		if err != nil {
			if logger != nil {
				logger.Error(fmt.Sprintf("Error fetching chapters: %v", err))
			}
			break
		}

		var data MangaDexChapterResponse
		err = json.NewDecoder(resp.Body).Decode(&data)
		resp.Body.Close()

		if err != nil {
			if logger != nil {
				logger.Error(fmt.Sprintf("Error decoding response: %v", err))
			}
			break
		}

		if data.Result != "ok" {
			if logger != nil {
				logger.Error("API returned an error")
			}
			break
		}

		allChapters = append(allChapters, data)

		total := data.Total
		if logger != nil {
			logger.Info(fmt.Sprintf("Retrieved %d/%d chapters...", offset+len(data.Data), total))
		}

		offset += limit
		if offset >= total {
			break
		}
	}

	// Process all chapters
	for _, response := range allChapters {
		for _, ch := range response.Data {
			chapterNumber := ch.Attributes.Chapter
			chapterTitle := ch.Attributes.Title

			if chapterNumber == "" || chapterTitle == "" {
				continue
			}

			// Parse chapter number
			numVal, err := strconv.ParseFloat(chapterNumber, 64)
			if err != nil {
				continue
			}

			chapters[numVal] = chapterTitle
		}
	}

	if logger != nil {
		logger.Info(fmt.Sprintf("Found %d chapter titles from MangaDex", len(chapters)))
	}

	return chapters
}

// CollectMissingChapterTitles collects titles for chapters that are missing
func CollectMissingChapterTitles(neededChapters []float64, mangaTitle string, existingTitles ChapterTitles, logger Logger, webInput WebInput) ChapterTitles {
	if webInput == nil {
		return existingTitles
	}

	missingChapters := []float64{}
	for _, ch := range neededChapters {
		if _, ok := existingTitles[ch]; !ok {
			missingChapters = append(missingChapters, ch)
		}
	}

	if len(missingChapters) == 0 {
		return existingTitles
	}

	if logger != nil {
		logger.Info(fmt.Sprintf("Need titles for %d chapters", len(missingChapters)))
		divider := strings.Repeat("=", 70)
		logger.Info("\n" + divider)
		logger.Info(fmt.Sprintf("MANGA: %s - CHAPTER TITLES NEEDED", mangaTitle))
		logger.Info(divider)
		logger.Info("You can enter titles now, or press Enter for default titles.")
		logger.Info("Collect all titles at once to avoid interruptions during processing.")
		logger.Info(divider + "\n")
	}

	result := make(ChapterTitles)
	for k, v := range existingTitles {
		result[k] = v
	}

	for _, chapterNum := range missingChapters {
		prompt := fmt.Sprintf("Title for Chapter %g (or press Enter for default): ", chapterNum)
		title := webInput(prompt, "text")

		if title != "" {
			result[chapterNum] = title
			if logger != nil {
				logger.Info(fmt.Sprintf("Added title for Chapter %g: '%s'", chapterNum, title))
			}
		} else {
			if logger != nil {
				logger.Info(fmt.Sprintf("Using default title for Chapter %g", chapterNum))
			}
		}
	}

	if logger != nil {
		logger.Info("\n" + strings.Repeat("=", 70))
		logger.Info("All chapter titles collected. Starting processing...")
		logger.Info(strings.Repeat("=", 70) + "\n")
	}

	return result
}

// Helper functions for HTML parsing

// findChapterLists finds all chapter lists in the HTML
func findChapterLists(n *html.Node) []*html.Node {
	var lists []*html.Node

	if n.Type == html.ElementNode && n.Data == "ul" {
		hasLangChapters := false
		elementID := ""

		// Check both class and id attributes
		for _, attr := range n.Attr {
			if attr.Key == "class" {
				classes := strings.Fields(attr.Val)
				for _, class := range classes {
					if class == "lang-chapters" {
						hasLangChapters = true
						break
					}
				}
			}
			if attr.Key == "id" {
				elementID = attr.Val
			}
		}

		// Only include the English chapter list (id="en-chapters")
		// Ignore other language lists (id="ja-chapters", etc.)
		if hasLangChapters {
			if elementID == "en-chapters" {
				lists = append(lists, n)
			}
			// Skip other language-specific lists
		} else {
			// For non-language-specific lists, check for general classes
			for _, attr := range n.Attr {
				if attr.Key == "class" {
					classes := strings.Fields(attr.Val)
					for _, class := range classes {
						if class == "ulclear" || class == "reading-list" {
							lists = append(lists, n)
							break
						}
					}
				}
			}
		}
	}

	for c := n.FirstChild; c != nil; c = c.NextSibling {
		lists = append(lists, findChapterLists(c)...)
	}

	return lists
}

// findChapterItems finds all chapter items in a list
func findChapterItems(n *html.Node) []*html.Node {
	var items []*html.Node

	if n.Type == html.ElementNode && n.Data == "li" {
		for _, attr := range n.Attr {
			if attr.Key == "class" {
				classes := strings.Fields(attr.Val)
				for _, class := range classes {
					if class == "item" || class == "reading-item" || class == "chapter-item" {
						items = append(items, n)
						break
					}
				}
			}
		}
	}

	for c := n.FirstChild; c != nil; c = c.NextSibling {
		items = append(items, findChapterItems(c)...)
	}

	return items
}

// getTextContent gets all text content from a node
func getTextContent(n *html.Node) string {
	if n.Type == html.TextNode {
		return n.Data
	}

	var text string
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		text += getTextContent(c)
	}

	return text
}

// Helper functions for Go 1.16 compatibility
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
