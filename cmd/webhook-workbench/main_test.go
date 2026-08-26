package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestVersion(t *testing.T) {
	var output bytes.Buffer
	if err := run(context.Background(), []string{"--version"}, &output, &output); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "webhook-workbench dev") {
		t.Fatalf("unexpected output: %s", output.String())
	}
}

func TestInvalidMaxBody(t *testing.T) {
	var output bytes.Buffer
	if err := run(context.Background(), []string{"--max-body", "0"}, &output, &output); err == nil {
		t.Fatal("expected validation error")
	}
}
