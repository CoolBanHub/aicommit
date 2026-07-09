package main

import (
	"reflect"
	"testing"
)

func TestNormalizeCommitArgsSupportsPushAlias(t *testing.T) {
	got := normalizeCommitArgs([]string{"push", "--message", "push", "no-push", "--provider=codex"})
	want := []string{"--push", "--message", "push", "--no-push", "--provider=codex"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("normalizeCommitArgs() = %#v, want %#v", got, want)
	}
}
