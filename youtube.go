package main

import "regexp"

// YTr is a regexp matching most YT links.
var YTr = regexp.MustCompile(`(?:.+?)?(?:\/v\/|watch\/|\?v=|\&v=|youtu\.be\/|\/v=|^youtu\.be\/|watch\%3Fv\%3D)([a-zA-Z0-9_-]{11,})+`)

func extractYT(text string) []string {
	result := []string{}
	matches := YTr.FindAllStringSubmatch(text, -1)
	for _, match := range matches {
		if len(match[1]) > 0 {
			result = append(result, match[1])
		}
	}

	return uniqueStrSlice(result)
}
