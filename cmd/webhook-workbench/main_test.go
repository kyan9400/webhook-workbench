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

func TestPrivateReplayEnvironment(t *testing.T) {
	t.Setenv("WEBHOOK_WORKBENCH_ALLOW_PRIVATE_REPLAY", "true")
	defaults, err := environmentDefaults()
	if err != nil {
		t.Fatal(err)
	}
	if !defaults.allowPrivateReplay {
		t.Fatal("expected private replay to be enabled")
	}

	t.Setenv("WEBHOOK_WORKBENCH_ALLOW_PRIVATE_REPLAY", "not-a-bool")
	if _, err := environmentDefaults(); err == nil {
		t.Fatal("expected invalid boolean error")
	}
}

func TestInvalidMaxBody(t *testing.T) {
	var output bytes.Buffer
	if err := run(context.Background(), []string{"--max-body", "0"}, &output, &output); err == nil {
		t.Fatal("expected validation error")
	}
}
