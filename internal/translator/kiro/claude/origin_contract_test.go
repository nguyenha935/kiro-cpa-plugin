package claude

import "testing"

// The Kiro runtime decides which events to stream based on the request origin.
// Measured against runtime.eu-central-1.kiro.dev on 2026-09-06 with a real
// Enterprise credential, all three values answer HTTP 200 with the same
// completion, but the event set differs:
//
//	AI_EDITOR -> assistantResponseEvent, metadataEvent, meteringEvent, contextUsageEvent
//	KIRO_CLI  -> assistantResponseEvent, metadataEvent, meteringEvent, contextUsageEvent
//	CLI       -> assistantResponseEvent, metadataEvent
//
// Rewriting a Kiro origin to a bare "CLI" therefore silences the metering and
// context-usage events the plugin needs for credit accounting, without any error
// to reveal it. No origin may be normalised to "CLI".
func TestNormalizeOriginNeverDowngradesToMeteringLessCLI(t *testing.T) {
	for _, origin := range []string{"KIRO_CLI", "AMAZON_Q", "KIRO_AI_EDITOR", "KIRO_IDE", "AI_EDITOR"} {
		if got := normalizeOrigin(origin); got == "CLI" {
			t.Fatalf("origin %q normalised to CLI, which drops meteringEvent and contextUsageEvent", origin)
		}
	}
}

func TestNormalizeOriginMapsOntoAcceptedValues(t *testing.T) {
	cases := map[string]string{
		"KIRO_CLI":       "KIRO_CLI",
		"AMAZON_Q":       "KIRO_CLI",
		"KIRO_AI_EDITOR": "AI_EDITOR",
		"KIRO_IDE":       "AI_EDITOR",
	}
	for origin, want := range cases {
		if got := normalizeOrigin(origin); got != want {
			t.Fatalf("normalizeOrigin(%q) = %q, want %q", origin, got, want)
		}
	}
}

// An origin the plugin does not recognise is passed through untouched: guessing a
// replacement is how the metering-less downgrade happened in the first place.
func TestNormalizeOriginPassesThroughUnknownValues(t *testing.T) {
	for _, origin := range []string{"AI_EDITOR", "MD_IDE", "CHATBOT", "", "SOMETHING_NEW"} {
		if got := normalizeOrigin(origin); got != origin {
			t.Fatalf("normalizeOrigin(%q) = %q, want it unchanged", origin, got)
		}
	}
}
