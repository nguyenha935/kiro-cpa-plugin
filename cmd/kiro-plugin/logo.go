package main

import (
	_ "embed"
	"encoding/base64"
)

// logoPNG is the provider mark shown by management clients. Kiro is not a
// built-in CPA provider, so no panel ships an icon for it; the plugin has to
// supply one through pluginapi.Metadata.Logo or every Kiro row renders bare.
//
//go:embed assets/kiro-logo.png
var logoPNG []byte

// pluginLogoDataURI returns the mark as a data URI.
//
// A data URI rather than a plugin route: management clients render the logo in a
// plain <img>, which cannot carry the management key, so any authenticated
// endpoint would answer 401. The panel passes data: URIs through untouched
// (resolvePluginAssetURL), and this keeps the asset working offline and with no
// extra route to secure.
func pluginLogoDataURI() string {
	if len(logoPNG) == 0 {
		return ""
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(logoPNG)
}
