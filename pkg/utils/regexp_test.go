package utils

import (
	"github.com/stretchr/testify/assert"
	"regexp"
	"testing"
)

func TestFindNamedMatches(t *testing.T) {
	scenarios := []struct {
		regex    *regexp.Regexp
		input    string
		expected map[string]string
	}{
		{
			regexp.MustCompile(`^(?P<name>\w+)`),
			"hello world",
			map[string]string{
				"name": "hello",
			},
		},
		{
			regexp.MustCompile(`^https?://.*/(?P<owner>.*)/(?P<repo>.*?)(\.git)?$`),
			"https://my_username@bitbucket.org/johndoe/social_network.git",
			map[string]string{
				"owner": "johndoe",
				"repo":  "social_network",
				"":      ".git", // unnamed capture group
			},
		},
		{
			regexp.MustCompile(`(?P<owner>hello) world`),
			"yo world",
			nil,
		},
	}

	for _, scenario := range scenarios {
		actual := FindNamedMatches(scenario.regex, scenario.input)
		assert.Equal(t, actual, scenario.expected)

	}
}
