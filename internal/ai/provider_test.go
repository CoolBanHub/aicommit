package ai

import "testing"

func TestExtractCommitMessageFromJSON(t *testing.T) {
	got, err := ExtractCommitMessage(`{"message":"Add commit generation"}`)
	if err != nil {
		t.Fatal(err)
	}
	if got != "Add commit generation" {
		t.Fatalf("unexpected message %q", got)
	}
}

func TestExtractCommitMessageFromFencedJSON(t *testing.T) {
	got, err := ExtractCommitMessage("```json\n{\"message\":\"fix: handle env files.\"}\n```")
	if err != nil {
		t.Fatal(err)
	}
	if got != "fix: handle env files" {
		t.Fatalf("unexpected message %q", got)
	}
}

func TestNormalizeCommitMessageUsesFirstLine(t *testing.T) {
	got := NormalizeCommitMessage("Add service mode\n\nMore details")
	if got != "Add service mode" {
		t.Fatalf("unexpected message %q", got)
	}
}
