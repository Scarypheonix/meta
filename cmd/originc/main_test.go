package main

import "testing"

func TestVersionIsSet(t *testing.T) {
	if Version == "" {
		t.Fatal("Version must not be empty")
	}
}

func TestUsageMentionsEverySubcommand(t *testing.T) {
	for _, sub := range []string{"version", "check", "build", "run"} {
		if !contains(usage, sub) {
			t.Errorf("usage text does not mention subcommand %q", sub)
		}
	}
}

func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
