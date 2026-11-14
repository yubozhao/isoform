package main

import "testing"

// A Moments reply sometimes adds a fenced note after the JSON. The parser
// must read the first JSON value and ignore what follows; cutting from the
// first to the last fence or brace used to fail with "invalid character '`'".
func TestParseMomentsResponseIgnoresTextAfterJSON(t *testing.T) {
	cases := map[string]string{
		"fenced json then a second fenced block": "```json\n{\"moments\":[{\"startIdx\":1,\"endIdx\":2,\"title\":\"A\"}]}\n```\nNote: S3 is short.\n```\n{\"skipped\":[3]}\n```",
		"plain json then prose with braces":      "{\"moments\":[{\"startIdx\":1,\"endIdx\":2,\"title\":\"A\"}]}\nI left {S3} out.",
		"prose before fenced json":               "Here you go:\n```json\n{\"moments\":[{\"startIdx\":1,\"endIdx\":2,\"title\":\"A\"}]}\n```",
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := parseMomentsResponse(raw, 3)
			if err != nil {
				t.Fatalf("parseMomentsResponse: %v", err)
			}
			if len(got) != 1 || got[0].StartIdx != 1 || got[0].EndIdx != 2 || got[0].Title != "A" {
				t.Fatalf("moments = %+v", got)
			}
		})
	}
}
