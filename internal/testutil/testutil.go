// Package testutil provides shared test helpers for plancheck's test suite.
package testutil

import "regexp"

var testPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?:^|/)__tests?__/`),
	regexp.MustCompile(`(?:^|/)tests?/`),
	regexp.MustCompile(`\.(?:test|spec)\.[jt]sx?$`),
	regexp.MustCompile(`(?:^|/)test_\w+\.py$`),
	regexp.MustCompile(`\w+_test\.py$`),
}

// IsTestFile returns true if the file path looks like a test file.
func IsTestFile(file string) bool {
	for _, re := range testPatterns {
		if re.MatchString(file) {
			return true
		}
	}
	return false
}
