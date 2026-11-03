package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// storedKeyProbe encodes the panel's persisted auth state the way the
// Management Center does (src/utils/encryption.ts: enc::v2:: since v1.27.0,
// enc::v1:: before it) and prints what the page's storedKey reads from each.
// The key is longer than the salt so the user-agent half of the v1 XOR is
// exercised.
const storedKeyProbe = `
const SALT_TEXT = 'cli-proxy-api-webui::secure-storage';
const host = 'cpa.example:8317';
const ua = 'Mozilla/5.0 (Macintosh) Chrome/150';
const key = 'mk-' + 'x'.repeat(200);
const xor = (text, salt) => {
  const data = new TextEncoder().encode(text);
  const bytes = new TextEncoder().encode(salt);
  return btoa(String.fromCharCode(...data.map((b, i) => b ^ bytes[i % bytes.length])));
};
const persisted = JSON.stringify({ state: { managementKey: key, rememberPassword: true }, version: 0 });
const cases = {
  v2: 'enc::v2::' + xor(persisted, SALT_TEXT + '|' + host),
  v1: 'enc::v1::' + xor(persisted, SALT_TEXT + '|' + host + '|' + ua),
  plain: persisted,
  'v2-other-host': 'enc::v2::' + xor(persisted, SALT_TEXT + '|elsewhere:1'),
};
for (const [name, value] of Object.entries(cases)) {
  const window = { localStorage: { getItem: (k) => (k === STORAGE_AUTH ? value : null) }, location: { host }, atob };
  const navigator = { userAgent: ua };
  const read = new Function('window', 'navigator', DECLS + '\n' + STORED_KEY + '\nreturn storedKey();');
  console.log(name + '=' + (read(window, navigator) === key ? 'read' : 'none'));
}
`

// The page reads the key the panel stored with "Remember password", so a
// change to the panel's storage format sends every user back to the key prompt
// (issue #34: v1.27.0 moved to enc::v2::).
func TestStoredKeyReadsEveryPanelFormat(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		if os.Getenv("CI") != "" {
			t.Fatal("node is required in CI to run the page's storedKey")
		}
		t.Skip("node not installed")
	}
	decls := regexp.MustCompile(`var (STORAGE_AUTH|OBFUSCATED|OBFUSCATED_LEGACY|SALT) = '[^']*';`).FindAllString(shellCommonScript, -1)
	storedKey := regexp.MustCompile(`(?s)function storedKey\(\) \{.*?\n  \}\n`).FindString(shellCommonScript)
	if len(decls) == 0 || storedKey == "" {
		t.Fatal("storedKey or its constants are missing from the shell script")
	}
	script := "const DECLS = " + jsString(strings.Join(decls, "\n")) + ";\n" +
		"const STORED_KEY = " + jsString(storedKey) + ";\n" +
		"const STORAGE_AUTH = 'cli-proxy-auth';\n" + storedKeyProbe
	path := filepath.Join(t.TempDir(), "probe.js")
	if err := os.WriteFile(path, []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(node, path).CombinedOutput()
	if err != nil {
		t.Fatalf("node: %v\n%s", err, out)
	}
	got := strings.Fields(string(out))
	want := []string{"v2=read", "v1=read", "plain=read", "v2-other-host=none"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("storedKey read %v, want %v", got, want)
	}
}

func jsString(value string) string {
	replacer := strings.NewReplacer(`\`, `\\`, "`", "\\`", "${", "\\${")
	return "`" + replacer.Replace(value) + "`"
}
