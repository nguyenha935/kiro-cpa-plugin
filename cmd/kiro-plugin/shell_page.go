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

func newShellPage(title, css, script, body string) shellPage {
	sum := sha256.Sum256([]byte(script))
	policy := "default-src 'none'; style-src 'unsafe-inline'; script-src 'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) +
		"'; connect-src 'self'; base-uri 'none'; form-action 'none'"
	page := `<!doctype html>
<html lang="en" data-theme="dark">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<meta http-equiv="Content-Security-Policy" content="` + policy + `">
<meta name="referrer" content="no-referrer">
<title>` + title + `</title>
<style>
` + shellBaseCSS + css + `</style>
<script>` + script + `</script>
</head>
<body>
` + body + `</body>
</html>
`
	return shellPage{Body: []byte(page), Policy: policy, Header: policy + "; frame-ancestors 'self'"}
}

// shellScript wraps a page's strings and logic around shellCommonScript in one
// closure. TEXT must come first: the common helpers read it through t().
func shellScript(text, logic string) string {
	return "\n(function () {\n  'use strict';\n" + text + shellCommonScript + "\n" + logic + "})();\n"
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

// shellBaseCSS is the theme, type scale and controls both pages share.
const shellBaseCSS = `html[data-theme="dark"]{
  color-scheme:dark;
  --page:#141118;--surface:#1c1922;--surface-2:#232029;--text:#f3f0f6;
  --text-2:#c8c2d2;--muted:#9d96a8;--border:#302b39;--border-2:#3c3646;
  --accent:#9b7bf7;
  --ok:#7fd6a9;--ok-bg:#182a22;
  --warn:#f4c37d;--warn-bg:#2c2418;--warn-line:#5d4a20;
  --bad:#ffa8bf;--bad-bg:#2c1c24;--bad-line:#60364a;
  --track:#2a2632;--zebra:#1a1721;--shadow:0 1px 0 rgba(0,0,0,.4),0 18px 46px rgba(0,0,0,.3);
}
html[data-theme="light"]{
  color-scheme:light;
  --page:#f6f4f9;--surface:#ffffff;--surface-2:#f7f5fa;--text:#1a1720;
  --text-2:#3f3949;--muted:#6b6478;--border:#e4e0ea;--border-2:#d2ccdb;
  --accent:#6d47e0;
  --ok:#136b45;--ok-bg:#eaf7f1;
  --warn:#8a5804;--warn-bg:#fdf4e6;--warn-line:#efdcb6;
  --bad:#9c1e3b;--bad-bg:#fdeef2;--bad-line:#f1c4d1;
  --track:#e9e5f0;--zebra:#fbfafd;--shadow:0 1px 0 rgba(26,23,32,.04),0 14px 34px rgba(26,23,32,.07);
}
*{box-sizing:border-box}
body{
  margin:0;background:var(--page);color:var(--text);
  font-family:-apple-system,BlinkMacSystemFont,"Segoe UI",Roboto,Inter,Helvetica,Arial,sans-serif;
  font-size:13px;line-height:1.5;-webkit-font-smoothing:antialiased;text-rendering:optimizeLegibility;
}
[hidden]{display:none!important}
h1{margin:0;font-size:17px;line-height:1.3;font-weight:600;letter-spacing:-.01em}
.intro{margin:2px 0 0;color:var(--muted);font-size:12px}
.act{
  display:inline-flex;align-items:center;min-height:26px;padding:0 10px;
  border:1px solid var(--border-2);border-radius:999px;background:var(--surface);
  color:var(--text-2);font-size:12px;font-weight:500;text-decoration:none;white-space:nowrap;
}
.act:hover{border-color:var(--accent);color:var(--text)}
.act:focus-visible{outline:2px solid var(--accent);outline-offset:1px}
.act.primary{border-color:var(--bad-line);background:var(--bad-bg);color:var(--bad)}
button.act{font-family:inherit;line-height:1.5;cursor:pointer}
button.act:disabled{cursor:progress;opacity:.6}
.notice{
  margin:0;padding:8px 11px;border:1px solid var(--bad-line);border-radius:8px;
  background:var(--bad-bg);color:var(--bad);font-size:12px;
}
/* Shell states: loading, the key prompt and failures, shown before the
   authenticated fragment arrives or in place of it. */
.keyform{display:grid;gap:6px;max-width:460px;margin:14px 0 0}
.keyform label{color:var(--text-2);font-size:12px;font-weight:500}
.keyrow{display:flex;gap:8px}
.keyrow input{
  flex:1 1 auto;min-width:0;min-height:30px;padding:0 10px;border:1px solid var(--border-2);border-radius:8px;
  background:var(--surface);color:var(--text);font:inherit;
}
.keyrow input:focus-visible{outline:2px solid var(--accent);outline-offset:1px}
.keyhint{margin:0;color:var(--muted);font-size:12px}
.flash{margin:12px 0 0}
.sr{position:absolute;width:1px;height:1px;overflow:hidden;clip:rect(0 0 0 0);white-space:nowrap}
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
  var OBFUSCATED = 'enc::v1::';
  var SALT = 'cli-proxy-api-webui::secure-storage';
  var params = new URLSearchParams(window.location.search);
  var root = document.documentElement;
  var api = window.location.pathname.split('/v0/resource/')[0] + '/v0/management/plugins/kiro/';
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

  function systemTheme() {
    return window.matchMedia && window.matchMedia('(prefers-color-scheme: light)').matches ? 'light' : 'dark';
  }

  function pickTheme() {
    var asked = params.get('theme');
    if (asked) {
      return asked.toLowerCase() === 'light' ? 'light' : 'dark';
    }
    var panel = panelRoot();
    if (panel) {
      return panel.getAttribute('data-theme') === 'dark' ? 'dark' : 'light';
    }
    var stored = panelSetting(STORAGE_THEME, 'theme');
    if (stored === 'dark') {
      return 'dark';
    }
    if (stored === 'light' || stored === 'white') {
      return 'light';
    }
    return systemTheme();
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
  function t(key) {
    var own = (TEXT[lang] || TEXT.en)[key];
    return own !== undefined ? own : (COMMON_TEXT[lang] || COMMON_TEXT.en)[key];
  }
  root.setAttribute('data-theme', pickTheme());
  root.setAttribute('lang', lang);

  // storedKey reads the key the panel keeps when "Remember password" is
  // ticked. The panel obfuscates it with a XOR over its host and user agent;
  // this is the inverse of src/utils/encryption.ts in the panel, not a secret.
  function storedKey() {
    try {
      var raw = window.localStorage.getItem(STORAGE_AUTH);
      if (!raw) {
        return '';
      }
      var text = raw;
      if (raw.indexOf(OBFUSCATED) === 0) {
        var binary = window.atob(raw.slice(OBFUSCATED.length));
        var salt = new TextEncoder().encode(SALT + '|' + window.location.host + '|' + navigator.userAgent);
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

  // request calls a plugin Management API route with the management key. A
  // 401 forgets a typed key and rejects with needKey, so the page can ask again.
  function request(path, options) {
    var key = currentKey();
    if (!key) {
      return Promise.reject({ needKey: true });
    }
    options.headers = options.headers || {};
    options.headers.Authorization = 'Bearer ' + key;
    options.cache = 'no-store';
    options.credentials = 'omit';
    return window.fetch(api + path, options).then(function (response) {
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
