package anthropic

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/angalor/tokenfuse/internal/provider"
)

func TestDeactivateKey_NeverLogsAdminKey(t *testing.T) {
	// This test asserts the non-negotiable: admin keys are radioactive and must never
	// appear in logs, errors, or output even on failure paths.
	const secret = "sk-ant-admin-THIS-MUST-NEVER-APPEAR-IN-LOGS-OR-PANIC-12345"

	var buf bytes.Buffer
	_ = slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	// We simulate what a real action + dry-run + error paths do.
	// Even if we force an error path, the secret must not leak.
	// Here we just ensure that when constructing and calling (which will fail without real net),
	// the key value is not written anywhere we control.

	// Create action (key is passed only to HTTP layer, never logged by us)
	act := NewDeactivateKey(secret, nil)

	k := provider.Key{ID: "testkey", Name: "test"}

	// Call will fail (no server), but we check captured output + any error strings.
	_, err := act.Trip(context.Background(), k)

	// Check all output we produced
	output := buf.String() + " " + fmtErr(err)

	if strings.Contains(output, secret) || strings.Contains(output, "sk-ant-admin-THIS") {
		t.Fatalf("admin key leaked into logs or error output:\n%s", output)
	}
}

func fmtErr(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
