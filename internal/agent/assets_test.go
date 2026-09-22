package agent

import (
	"strings"
	"testing"
)

func TestRunnerRegistersGeminiFlashMetadata(t *testing.T) {
	source := string(Runner())
	for _, value := range []string{
		`id: "google/gemini-3.8-flash"`,
		`contextWindow: 1048576`,
		`maxTokens: 65536`,
		`thinkingFormat: "openrouter"`,
		`openRouterRouting: { zdr: true }`,
	} {
		if !strings.Contains(source, value) {
			t.Fatalf("runner does not contain %q", value)
		}
	}
}

func TestRunnerUsesLeanDirectSessionStartup(t *testing.T) {
	source := string(Runner())
	for _, value := range []string{
		`"--session", "/session/alo.jsonl"`,
		`"--no-extensions"`,
		`"--no-skills"`,
		`"--no-prompt-templates"`,
		`"--no-themes"`,
		`PI_SKIP_VERSION_CHECK: "1"`,
		`Evidence summary:`,
		`Replay exit status:`,
	} {
		if !strings.Contains(source, value) {
			t.Fatalf("runner does not contain %q", value)
		}
	}
	if strings.Contains(source, `"--session-id"`) {
		t.Fatal("runner still discovers sessions by ID")
	}
}
