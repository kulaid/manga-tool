package utils

import (
	"regexp"
	"strings"
)

// NormalizeTitle normalizes a manga title for comparison
// by removing special characters, spaces, and making everything lowercase
func NormalizeTitle(title string) string {
	// Convert to lowercase
	normalized := strings.ToLower(title)

	// Remove special characters and spaces
	reg := regexp.MustCompile(`[^a-z0-9]`)
	normalized = reg.ReplaceAllString(normalized, "")

	return normalized
}

// ExactMatchTitle compares titles case-insensitively but preserves words
func ExactMatchTitle(title1, title2 string) bool {
	return strings.EqualFold(title1, title2)
}

// ContainsIgnoreCase checks if a string contains another string (case insensitive)
func ContainsIgnoreCase(s, substr string) bool {
	return strings.Contains(strings.ToLower(s), strings.ToLower(substr))
}

// TrimSpaceAndLower trims whitespace and converts to lowercase
func TrimSpaceAndLower(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}
