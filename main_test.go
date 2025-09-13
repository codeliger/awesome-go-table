package main

import (
	"reflect"
	"testing"
)

func TestParseGithubsURLRegex(t *testing.T) {
	testCases := []struct {
		name     string
		input    string
		expected [][][]byte
	}{
		{
			name:  "Standard github.com URL",
			input: "[awesome-go](https://github.com/avelino/awesome-go) - A curated list of awesome Go frameworks, libraries and software.",
			expected: [][][]byte{
				{
					[]byte("[awesome-go](https://github.com/avelino/awesome-go) - A curated list of awesome Go frameworks, libraries and software."),
					[]byte("awesome-go"),
					[]byte("https://github.com/avelino/awesome-go"),
					nil, // subdomain
					nil, // github.io path
					[]byte("avelino"),
					[]byte("awesome-go"),
					[]byte("A curated list of awesome Go frameworks, libraries and software."),
				},
			},
		},
		{
			name:  "github.io URL without path",
			input: "[gilbert](https://go-gilbert.github.io) - Buildsystem and task runner for Go projects.",
			expected: [][][]byte{
				{
					[]byte("[gilbert](https://go-gilbert.github.io) - Buildsystem and task runner for Go projects."),
					[]byte("gilbert"),
					[]byte("https://go-gilbert.github.io"),
					[]byte("go-gilbert"),
					nil, // github.io path
					nil, // github.com owner
					nil, // github.com repo
					[]byte("Buildsystem and task runner for Go projects."),
				},
			},
		},
		{
			name:  "github.io URL with path",
			input: "[my-project](https://my-user.github.io/my-project) - My awesome project.",
			expected: [][][]byte{
				{
					[]byte("[my-project](https://my-user.github.io/my-project) - My awesome project."),
					[]byte("my-project"),
					[]byte("https://my-user.github.io/my-project"),
					[]byte("my-user"),
					[]byte("/my-project"),
					nil, // github.com owner
					nil, // github.com repo
					[]byte("My awesome project."),
				},
			},
		},
		{
			name:  "github.com URL with subdomain",
			input: "[status](https://status.github.com/owner/repo) - Status page.",
			expected: [][][]byte{
				{
					[]byte("[status](https://status.github.com/owner/repo) - Status page."),
					[]byte("status"),
					[]byte("https://status.github.com/owner/repo"),
					[]byte("status"),
					nil, // github.io path
					[]byte("owner"),
					[]byte("repo"),
					[]byte("Status page."),
				},
			},
		},
		{
			name:     "No URL in text",
			input:    "This is just some text without a URL.",
			expected: nil,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			actual := parseGithubsURLRegex(tc.input)
			if !reflect.DeepEqual(actual, tc.expected) {
				t.Errorf("Test case '%s' failed.\nExpected:\n%#v\nGot:\n%#v", tc.name, tc.expected, actual)
				if len(actual) > 0 && len(tc.expected) > 0 && len(actual[0]) == len(tc.expected[0]) {
					for i := range actual[0] {
						if !reflect.DeepEqual(actual[0][i], tc.expected[0][i]) {
							t.Errorf("Mismatch at index %d:\nExpected: %s\nGot:      %s", i, tc.expected[0][i], actual[0][i])
						}
					}
				}
			}
		})
	}
}
