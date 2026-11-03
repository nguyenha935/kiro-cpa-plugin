package main

import (
	"crypto/sha256"
	"encoding/base64"
	"net/http"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// A shell page is the only thing a resource route serves: CPA answers
// /v0/resource/plugins/<id>/... without the management key, so each page is the
// same static bytes for everyone and reaches account data only through
// Management API routes, with the key the Management Center kept.
type shellPage struct {
	Body []byte
	// Policy is the CSP of the page's meta tag. It admits exactly the page's
	// one inline script, by hash, and network requests to the same origin.
	Policy string
	// Header is Policy plus frame-ancestors, which a meta policy may not carry.
	Header string
}

// newShellPage builds a page from its whole stylesheet. Both pages show the
// plugin's logo in their sign-in dialog as a data: image; no page loads an
// image from anywhere else.
func newShellPage(title, css, script, body string) shellPage {
	sum := sha256.Sum256([]byte(script))
	policy := "default-src 'none'; img-src data:; style-src 'unsafe-inline'; script-src 'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) +
		"'; connect-src 'self'; base-uri 'none'; form-action 'none'"
	page := `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<meta http-equiv="Content-Security-Policy" content="` + policy + `">
<meta name="referrer" content="no-referrer">
<title>` + title + `</title>
<style>
` + css + `</style>
<script>` + script + `</script>
</head>
<body>
` + body + `</body>
</html>
`
	return shellPage{Body: []byte(page), Policy: policy, Header: policy + "; frame-ancestors 'self'"}
}

// shellScript wraps a page's strings and logic around the common helpers and
// the sign-in form in one closure. The string tables come first: t() reads them.
func shellScript(text, logic string) string {
	return "\n(function () {\n  'use strict';\n" + text + signInText + shellCommonScript + "\n" + signInScript + "\n" + logic + "})();\n"
}

func shellResponse(page shellPage) ([]byte, error) {
	return okEnvelope(pluginapi.ManagementResponse{
		StatusCode: http.StatusOK,
		Headers: http.Header{
			"Content-Type":            []string{"text/html; charset=utf-8"},
			"Cache-Control":           []string{"no-cache"},
			"Content-Security-Policy": []string{page.Header},
			"Referrer-Policy":         []string{"no-referrer"},
			"X-Content-Type-Options":  []string{"nosniff"},
		},
		Body: page.Body,
	})
}

// panelBaseCSS is the Management Center's look, shared by both pages: its
// three themes (src/styles/themes.scss: the default warm paper, white and
// dark), its type, its pill buttons and its fields. The panel's own default
// carries no data-theme attribute, so it is the :root block here.
const panelBaseCSS = `:root{
  color-scheme:light;
  --bg-secondary:#faf9f5;--bg-primary:#f0eee8;--bg-tertiary:#e9e6df;
  --text-primary:#2d2a26;--text-secondary:#6d6760;--text-tertiary:#a29c95;
  --border-color:#e3e1db;--border-primary:#d5d2cb;--border-hover:#cecac4;
  --primary-color:#8b8680;--ring:rgba(139,134,128,.18);
  --viz-success:#10b981;--viz-failure:#c65746;
  --amber:#d97706;--amber-text:#92400e;--failure-text:#c65746;
  --shadow:0 1px 2px 0 rgb(0 0 0 / .08);--shadow-lg:0 10px 18px -3px rgb(0 0 0 / .1);
  --font-mono:ui-monospace,"SF Mono","Cascadia Mono","JetBrains Mono","Fira Code",Menlo,Consolas,"Liberation Mono",monospace;
  --ease:cubic-bezier(.23,1,.32,1);
}
html[data-panel-theme="white"]{
  --bg-secondary:#ffffff;--bg-primary:#ffffff;--bg-tertiary:#f6f6f6;
  --border-color:#e5e5e5;--border-primary:#d9d9d9;--border-hover:#cccccc;
}
html[data-panel-theme="dark"]{
  color-scheme:dark;
  --bg-secondary:#151412;--bg-primary:#1d1b18;--bg-tertiary:#262320;
  --text-primary:#f6f4f1;--text-secondary:#c9c3bb;--text-tertiary:#9c958d;
  --border-color:#3a3530;--border-primary:#4a453f;--border-hover:#5a544d;
  --viz-success:#0fae78;--viz-failure:#cf4b3a;
  --amber:#f59e0b;--amber-text:#fcd34d;--failure-text:#f1b0a6;
  --shadow:0 1px 3px 0 rgb(0 0 0 / .3);--shadow-lg:0 10px 15px -3px rgb(0 0 0 / .3);
}
*{box-sizing:border-box}
[hidden]{display:none!important}
body{
  margin:0;background:var(--bg-secondary);color:var(--text-primary);
  font-family:-apple-system,BlinkMacSystemFont,"Segoe UI",Roboto,Oxygen,Ubuntu,Cantarell,"Fira Sans","Droid Sans","Helvetica Neue",sans-serif;
  font-size:14px;line-height:1.5;-webkit-font-smoothing:antialiased;text-rendering:optimizeLegibility;
}
.i{width:16px;height:16px;flex-shrink:0;fill:none;stroke:currentColor;stroke-width:1.8;stroke-linecap:round;stroke-linejoin:round}
.alert{
  margin:0;padding:10px 12px;border:1px solid color-mix(in srgb,var(--viz-failure) 28%,transparent);border-radius:10px;
  background:color-mix(in srgb,var(--viz-failure) 8%,transparent);color:color-mix(in srgb,var(--viz-failure) 82%,var(--text-primary));
  font-size:13px;line-height:1.55;overflow-wrap:anywhere;
}
.pill{
  display:inline-flex;align-items:center;justify-content:center;gap:7px;flex-shrink:0;height:38px;padding:0 16px;
  border:1px solid transparent;border-radius:999px;font:inherit;font-size:13px;font-weight:600;line-height:1;
  white-space:nowrap;text-decoration:none;cursor:pointer;
  transition:transform 160ms var(--ease),background-color 200ms var(--ease),border-color 200ms var(--ease),box-shadow 200ms var(--ease);
}
.pill:focus{outline:none}
.pill:focus-visible{outline:2px solid var(--primary-color);outline-offset:2px}
.pill:active:not(:disabled){transform:scale(.97)}
.pill:disabled{cursor:progress;opacity:.6}
.primary{background:var(--text-primary);color:var(--bg-secondary)}
.secondary{border-color:var(--border-primary);background:var(--bg-secondary);color:var(--text-primary)}
.wide{width:100%}
.spinner{display:none;width:14px;height:14px;border:2px solid currentColor;border-right-color:transparent;border-radius:50%;animation:spin .8s linear infinite}
.busy .spinner{display:inline-block}
@keyframes spin{to{transform:rotate(360deg)}}
input,select,textarea{
  width:100%;min-width:0;min-height:40px;padding:9px 12px;
  border:1px solid var(--border-color);border-radius:8px;background:var(--bg-secondary);color:var(--text-primary);
  font:inherit;font-size:14px;transition:border-color 150ms ease,box-shadow 150ms ease;
}
input::placeholder,textarea::placeholder{color:var(--text-tertiary)}
input:focus,select:focus,textarea:focus{outline:none;border-color:var(--primary-color);box-shadow:0 0 0 3px var(--ring)}
textarea{font-family:var(--font-mono);font-size:12px;line-height:1.55;resize:vertical}
.sr{position:absolute;width:1px;height:1px;overflow:hidden;clip:rect(0 0 0 0);white-space:nowrap}
@media (hover:hover) and (pointer:fine){
  .primary:hover:not(:disabled){background:color-mix(in srgb,var(--text-primary) 86%,var(--bg-secondary));box-shadow:0 10px 22px -10px color-mix(in srgb,var(--text-primary) 45%,transparent)}
  .secondary:hover:not(:disabled){border-color:var(--border-hover);background:var(--bg-tertiary)}
}
/* Touch: every control is at least 44px, and inputs keep 16px text so iOS
   does not zoom in on focus. */
@media (pointer:coarse){
  input,select{min-height:44px;font-size:16px}
  .pill{height:44px}
}
@media (prefers-reduced-motion:reduce){
  .spinner{animation:none}
  .pill:active:not(:disabled){transform:none}
}
`

// shellCommonScript reads the panel's theme, language and kept management key,
// localises [data-t] nodes and calls the plugin's Management API routes.
const shellCommonScript = `  var COMMON_TEXT = {
    en: {
      keyRejected: 'CLIProxyAPI rejected that management key. Enter it again.',
      keyLabel: 'Management key',
      keyHint: 'The key stays in this page and is forgotten when it closes. Sign in to the Management Center with “Remember password” ticked to skip this step.',
      keySubmit: 'Continue'
    },
    vi: {
      keyRejected: 'CLIProxyAPI từ chối management key này. Nhập lại.',
      keyLabel: 'Management key',
      keyHint: 'Key chỉ nằm trong trang này và mất khi đóng trang. Đăng nhập Management Center có tích “Ghi nhớ mật khẩu” để bỏ qua bước này.',
      keySubmit: 'Tiếp tục'
    },
    'zh-CN': {
      keyRejected: 'CLIProxyAPI 拒绝了该管理密钥，请重新输入。',
      keyLabel: '管理密钥',
      keyHint: '密钥只保存在此页面中，关闭页面后即失效。登录管理中心时勾选“记住密码”即可跳过此步骤。',
      keySubmit: '继续'
    }
  };
  var STORAGE_AUTH = 'cli-proxy-auth';
  var STORAGE_THEME = 'cli-proxy-theme';
  var STORAGE_LANGUAGE = 'cli-proxy-language';
  var OBFUSCATED = 'enc::v2::';
  var OBFUSCATED_LEGACY = 'enc::v1::';
  var SALT = 'cli-proxy-api-webui::secure-storage';
  var params = new URLSearchParams(window.location.search);
  var root = document.documentElement;
  // base is where this CPA mounts its routes; the plugin's own Management API
  // routes sit under api, CPA's OAuth session routes under base.
  var base = window.location.pathname.split('/v0/resource/')[0];
  var api = base + '/v0/management/plugins/kiro/';
  var memoryKey = '';

  // The Usage page is framed by the Management Center and reads its theme and
  // language from the panel document; the sign-in page opens in a tab of its
  // own and reads what the panel stored. A query value wins over both.
  function panelRoot() {
    try {
      if (window.parent && window.parent !== window && window.parent.document) {
        return window.parent.document.documentElement;
      }
    } catch (error) {
      return null;
    }
    return null;
  }

  function panelSetting(storageKey, field) {
    try {
      var parsed = JSON.parse(window.localStorage.getItem(storageKey) || 'null');
      var state = parsed && parsed.state ? parsed.state : parsed;
      return state && typeof state[field] === 'string' ? state[field] : '';
    } catch (error) {
      return '';
    }
  }

  // pickPanelTheme returns the theme the panel applies: "light" (its default,
  // no attribute), "white" or "dark". The panel resolves its "auto" to dark
  // or white by the system setting (src/stores/useThemeStore.ts).
  function pickPanelTheme() {
    var asked = (params.get('theme') || '').toLowerCase();
    if (asked === 'light' || asked === 'white' || asked === 'dark') {
      return asked;
    }
    var panel = panelRoot();
    if (panel) {
      var applied = panel.getAttribute('data-theme');
      return applied === 'dark' || applied === 'white' ? applied : 'light';
    }
    var stored = panelSetting(STORAGE_THEME, 'theme');
    if (stored === 'light' || stored === 'white' || stored === 'dark') {
      return stored;
    }
    return window.matchMedia && window.matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'white';
  }

  function pickLang() {
    var panel = panelRoot();
    var value = params.get('lang') || (panel && panel.getAttribute('lang')) ||
      panelSetting(STORAGE_LANGUAGE, 'language') || navigator.language || 'en';
    if (/^vi(-|$)/i.test(value)) {
      return 'vi';
    }
    return /^zh(-|$)/i.test(value) ? 'zh-CN' : 'en';
  }

  var lang = pickLang();
  // t looks a key up in the page's own strings, then the sign-in form's, then
  // the key prompt's.
  function t(key) {
    var tables = [TEXT, SIGNIN_TEXT, COMMON_TEXT];
    for (var index = 0; index < tables.length; index += 1) {
      var found = (tables[index][lang] || tables[index].en)[key];
      if (found !== undefined) {
        return found;
      }
    }
    return key;
  }
  root.setAttribute('data-panel-theme', pickPanelTheme());
  root.setAttribute('lang', lang);

  // storedKey reads the key the panel keeps when "Remember password" is
  // ticked. The panel obfuscates it with a XOR over its host; this is the
  // inverse of src/utils/encryption.ts in the panel, not a secret. Panels
  // before v1.27.0 wrote enc::v1::, whose XOR also covered the user agent.
  function storedKey() {
    try {
      var raw = window.localStorage.getItem(STORAGE_AUTH);
      if (!raw) {
        return '';
      }
      var text = raw;
      var legacy = raw.indexOf(OBFUSCATED_LEGACY) === 0;
      if (legacy || raw.indexOf(OBFUSCATED) === 0) {
        var binary = window.atob(raw.slice(OBFUSCATED.length));
        var salt = new TextEncoder().encode(SALT + '|' + window.location.host + (legacy ? '|' + navigator.userAgent : ''));
        var bytes = new Uint8Array(binary.length);
        for (var index = 0; index < binary.length; index += 1) {
          bytes[index] = binary.charCodeAt(index) ^ salt[index % salt.length];
        }
        text = new TextDecoder().decode(bytes);
      }
      var parsed = JSON.parse(text);
      var state = parsed && parsed.state ? parsed.state : parsed;
      return state && typeof state.managementKey === 'string' ? state.managementKey.trim() : '';
    } catch (error) {
      return '';
    }
  }

  function currentKey() {
    return memoryKey || storedKey();
  }

  function localise() {
    document.title = t('title');
    var labelled = document.querySelectorAll('[data-t]');
    for (var index = 0; index < labelled.length; index += 1) {
      labelled[index].textContent = t(labelled[index].getAttribute('data-t'));
    }
  }

  // request calls a Management API route with the management key: a relative
  // path is one of the plugin's routes, an absolute one a CPA route. A 401
  // forgets a typed key and rejects with needKey, so the page can ask again.
  function request(path, options) {
    var key = currentKey();
    if (!key) {
      return Promise.reject({ needKey: true });
    }
    options.headers = options.headers || {};
    options.headers.Authorization = 'Bearer ' + key;
    options.cache = 'no-store';
    options.credentials = 'omit';
    return window.fetch((path.charAt(0) === '/' ? base : api) + path, options).then(function (response) {
      if (response.status === 401) {
        memoryKey = '';
        throw { needKey: true, rejected: true };
      }
      if (response.ok) {
        return response;
      }
      return response.text().then(function (body) {
        var message = body;
        try {
          message = JSON.parse(body).error || body;
        } catch (error) {
          message = body;
        }
        throw { message: String(message || 'HTTP ' + response.status).slice(0, 300) };
      });
    });
  }

  function whenReady(start) {
    if (document.readyState === 'loading') {
      document.addEventListener('DOMContentLoaded', start);
    } else {
      start();
    }
  }
`
