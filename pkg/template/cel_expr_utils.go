package template

import "regexp"

var numericIndexPattern = regexp.MustCompile(`\[\s*\d+\s*\]`)

func clampNumericIndexes(expr string) string {
	return numericIndexPattern.ReplaceAllString(expr, "[0]")
}
