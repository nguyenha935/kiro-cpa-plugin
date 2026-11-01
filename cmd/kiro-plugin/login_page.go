package main

import "strings"

// The sign-in page lets the stock Management Center add a Kiro account. Its
// OAuth page shows a plugin card that calls StartLogin and polls the returned
// state, but it has no Kiro form: the method and its fields go to the
// authenticated /connect route. StartLogin therefore returns this page as the
// card's URL, with the state in the fragment, and the page posts the chosen
// method to /connect with the management key. The card's own polling then
// completes the sign-in; this page never polls, because a successful poll
// consumes the flow and the card would then report it as lost.
//
// The page opens in a tab of its own, so it is drawn as the panel's OAuth
// dialog standing alone: one card centred on the page, in the panel's three
// themes (src/styles/themes.scss and OAuthFlowDialog.module.scss of the
// Management Center).

const loginResourcePath = "/login"

var loginShell = newShellPage("Kiro sign-in", loginShellCSS, shellScript(loginShellText, loginShellLogic), loginPageBody(), true)

// loginURL is relative unless login_base_url is set. CPA hands StartLogin only
// its loopback callback base, and the Management Center resolves the link
// against its own origin, which is the CPA that serves this page. A client
// that opens the link elsewhere (EasyCLIProxyAPI parses it with new URL() and
// hands it to the system browser) needs an absolute link, and only the
// operator knows which address reaches this CPA from there.
func loginURL(base, state string) string {
	return base + resourceBasePath + loginResourcePath + "#state=" + state
}

func handleLoginShell() ([]byte, error) {
	return shellResponse(loginShell)
}

// loginIcons are Lucide outlines, the icon set the Management Center draws
// with. They are inline because the page's policy loads nothing.
var loginIcons = map[string]string{
	"user":     `<circle cx="12" cy="8" r="4"/><path d="M4 21v-1a6 6 0 0 1 6-6h4a6 6 0 0 1 6 6v1"/>`,
	"building": `<path d="M6 22V4a2 2 0 0 1 2-2h8a2 2 0 0 1 2 2v18Z"/><path d="M6 12H4a2 2 0 0 0-2 2v6a2 2 0 0 0 2 2h2"/><path d="M18 9h2a2 2 0 0 1 2 2v9a2 2 0 0 1-2 2h-2"/><path d="M10 6h4M10 10h4M10 14h4M10 18h4"/>`,
	"key":      `<path d="M2.586 17.414A2 2 0 0 0 2 18.828V21a1 1 0 0 0 1 1h3a1 1 0 0 0 1-1v-1a1 1 0 0 1 1-1h1a1 1 0 0 0 1-1v-1a1 1 0 0 1 1-1h.172a2 2 0 0 0 1.414-.586l.814-.814a6.5 6.5 0 1 0-4-4z"/><circle cx="16.5" cy="7.5" r=".5"/>`,
	"refresh":  `<path d="M3 12a9 9 0 0 1 9-9 9.75 9.75 0 0 1 6.74 2.74L21 8"/><path d="M21 3v5h-5"/><path d="M21 12a9 9 0 0 1-9 9 9.75 9.75 0 0 1-6.74-2.74L3 16"/><path d="M8 16H3v5"/>`,
	"json":     `<path d="M15 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V7Z"/><path d="M14 2v4a2 2 0 0 0 2 2h4"/><path d="M10 12a1 1 0 0 0-1 1v1a1 1 0 0 1-1 1 1 1 0 0 1 1 1v1a1 1 0 0 0 1 1"/><path d="M14 18a1 1 0 0 0 1-1v-1a1 1 0 0 1 1-1 1 1 0 0 1-1-1v-1a1 1 0 0 0-1-1"/>`,
	"external": `<path d="M15 3h6v6"/><path d="M10 14 21 3"/><path d="M18 13v6a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V8a2 2 0 0 1 2-2h6"/>`,
	"copy":     `<rect width="14" height="14" x="8" y="8" rx="2"/><path d="M4 16c-1.1 0-2-.9-2-2V4c0-1.1.9-2 2-2h10c1.1 0 2 .9 2 2"/>`,
	"eye":      `<path d="M2.062 12.348a1 1 0 0 1 0-.696 10.75 10.75 0 0 1 19.876 0 1 1 0 0 1 0 .696 10.75 10.75 0 0 1-19.876 0"/><circle cx="12" cy="12" r="3"/>`,
	"check":    `<circle cx="12" cy="12" r="10"/><path d="m9 12 2 2 4-4"/>`,
}

// loginPageBody fills the body's {{logo}} and {{icon:name}} slots.
func loginPageBody() string {
	pairs := []string{"{{logo}}", pluginLogoDataURI()}
	for name, paths := range loginIcons {
		pairs = append(pairs, "{{icon:"+name+"}}", `<svg class="i" viewBox="0 0 24 24" aria-hidden="true" focusable="false">`+paths+`</svg>`)
	}
	return strings.NewReplacer(pairs...).Replace(loginShellBody)
}

const loginShellCSS = `:root{
  color-scheme:light;
  --bg-secondary:#faf9f5;--bg-primary:#f0eee8;--bg-tertiary:#e9e6df;
  --text-primary:#2d2a26;--text-secondary:#6d6760;--text-tertiary:#a29c95;
  --border-color:#e3e1db;--border-primary:#d5d2cb;--border-hover:#cecac4;
  --primary-color:#8b8680;--ring:rgba(139,134,128,.18);
  --viz-success:#10b981;--viz-failure:#c65746;
  --shadow-lg:0 10px 18px -3px rgb(0 0 0 / .1);
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
  --shadow-lg:0 10px 15px -3px rgb(0 0 0 / .3);
}
*{box-sizing:border-box}
[hidden]{display:none!important}
html,body{height:100%}
body{
  margin:0;background:var(--bg-secondary);color:var(--text-primary);
  font-family:-apple-system,BlinkMacSystemFont,"Segoe UI",Roboto,Oxygen,Ubuntu,Cantarell,"Fira Sans","Droid Sans","Helvetica Neue",sans-serif;
  font-size:14px;line-height:1.5;-webkit-font-smoothing:antialiased;text-rendering:optimizeLegibility;
}
/* The card sits in the middle of the viewport, as the panel's dialog does,
   and scrolls with the page when it is taller than the window. */
.stage{display:flex;align-items:center;justify-content:center;min-height:100%;padding:32px 16px}
.dialog{
  width:min(540px,100%);overflow:hidden;
  border:1px solid var(--border-color);border-radius:18px;background:var(--bg-primary);box-shadow:var(--shadow-lg);
  animation:dialog-in 280ms var(--ease) both;
}
@keyframes dialog-in{from{opacity:0;transform:translate3d(0,10px,0) scale(.97)}}
.head{display:flex;align-items:center;gap:14px;min-width:0;padding:22px 24px 6px}
.glyph{
  display:flex;align-items:center;justify-content:center;flex-shrink:0;width:44px;height:44px;
  border:1px solid var(--border-color);border-radius:12px;background:var(--bg-secondary);
}
.glyph img{display:block;width:22px;height:22px;border-radius:5px}
.head-text{display:flex;flex-direction:column;gap:3px;min-width:0}
h1{margin:0;font-size:17px;font-weight:650;line-height:1.25;letter-spacing:-.015em}
.caption{font-family:var(--font-mono);font-size:11.5px;font-weight:500;letter-spacing:.02em;color:var(--text-tertiary)}
.body{display:flex;flex-direction:column;gap:18px;padding:14px 24px 24px}
.hint{margin:0;font-size:13px;line-height:1.65;color:var(--text-secondary)}
.alert{
  margin:0;padding:10px 12px;border:1px solid color-mix(in srgb,var(--viz-failure) 28%,transparent);border-radius:10px;
  background:color-mix(in srgb,var(--viz-failure) 8%,transparent);color:color-mix(in srgb,var(--viz-failure) 82%,var(--text-primary));
  font-size:13px;line-height:1.55;overflow-wrap:anywhere;
}
.stack{display:flex;flex-direction:column;gap:16px;margin:0}
.label,.field label{font-size:13px;font-weight:600;color:var(--text-primary)}
.field{display:grid;gap:6px;min-width:0}
.field-hint{margin:0;font-size:12px;line-height:1.6;color:var(--text-secondary)}
.methods{display:grid;gap:10px;min-width:0;margin:0;padding:0;border:0}
.methods legend{padding:0;margin:0 0 10px}
.method-grid{display:grid;grid-template-columns:repeat(auto-fill,minmax(140px,1fr));gap:8px}
.method{position:relative;display:flex}
.method input{position:absolute;opacity:0;pointer-events:none}
.method>span{
  display:flex;flex:1;align-items:center;gap:9px;min-height:44px;padding:8px 12px;
  border:1px solid var(--border-color);border-radius:10px;background:var(--bg-secondary);
  color:var(--text-secondary);font-size:13px;font-weight:600;line-height:1.3;cursor:pointer;
  transition:border-color 200ms var(--ease),background-color 200ms var(--ease),color 200ms var(--ease);
}
.method input:checked+span{border-color:var(--text-primary);color:var(--text-primary);box-shadow:inset 0 0 0 1px var(--text-primary)}
.method input:focus-visible+span{outline:2px solid var(--primary-color);outline-offset:2px}
input,select,textarea{
  width:100%;min-width:0;min-height:40px;padding:9px 12px;
  border:1px solid var(--border-color);border-radius:8px;background:var(--bg-secondary);color:var(--text-primary);
  font:inherit;font-size:14px;transition:border-color 150ms ease,box-shadow 150ms ease;
}
input::placeholder,textarea::placeholder{color:var(--text-tertiary)}
input:focus,select:focus,textarea:focus{outline:none;border-color:var(--primary-color);box-shadow:0 0 0 3px var(--ring)}
textarea{font-family:var(--font-mono);font-size:12px;line-height:1.55;resize:vertical}
.secret{position:relative;display:block}
.secret input{padding-right:46px}
.reveal{
  position:absolute;top:50%;right:4px;transform:translateY(-50%);
  display:flex;align-items:center;justify-content:center;width:36px;height:36px;padding:0;
  border:0;border-radius:8px;background:transparent;color:var(--text-tertiary);cursor:pointer;
}
.reveal[aria-pressed="true"]{color:var(--text-primary)}
.reveal:focus-visible,.icon-btn:focus-visible{outline:2px solid var(--primary-color);outline-offset:1px}
.i{width:16px;height:16px;flex-shrink:0;fill:none;stroke:currentColor;stroke-width:1.8;stroke-linecap:round;stroke-linejoin:round}
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
@media (hover:hover) and (pointer:fine){
  .primary:hover:not(:disabled){background:color-mix(in srgb,var(--text-primary) 86%,var(--bg-secondary));box-shadow:0 10px 22px -10px color-mix(in srgb,var(--text-primary) 45%,transparent)}
  .secondary:hover:not(:disabled),.icon-btn:hover{border-color:var(--border-hover);background:var(--bg-tertiary)}
  .method>span:hover{border-color:var(--border-hover);color:var(--text-primary)}
  .reveal:hover{color:var(--text-primary)}
}
/* Steps follow the panel's dialog: a mono index on a thin rail. */
.steps{counter-reset:step;display:flex;flex-direction:column;margin:0;padding:0;list-style:none}
.step{position:relative;counter-increment:step;display:grid;grid-template-columns:22px minmax(0,1fr);gap:14px;padding-bottom:22px}
.step:last-child{padding-bottom:0}
.step:not(:last-child)::before{content:'';position:absolute;left:10.5px;top:28px;bottom:6px;width:1px;background:var(--border-color)}
.step-index{
  display:flex;align-items:center;justify-content:center;width:22px;height:22px;border:1px solid var(--border-primary);
  border-radius:50%;background:var(--bg-primary);font-family:var(--font-mono);font-size:11px;font-weight:600;line-height:1;color:var(--text-secondary);
}
.step-index::before{content:counter(step)}
.step-body{display:flex;flex-direction:column;gap:10px;min-width:0;padding-top:1px}
.step-title{margin:0;font-size:13.5px;font-weight:600;line-height:1.45}
/* Hints use the secondary text colour: the panel's tertiary measures about
   2.5:1 on the light card, and these lines carry instructions. */
.step-hint{margin:-4px 0 0;font-size:12px;line-height:1.6;color:var(--text-secondary);overflow-wrap:anywhere}
.code-row{display:flex;flex-wrap:wrap;align-items:center;gap:12px;margin:0}
.code-label{font-size:12px;color:var(--text-secondary)}
.code{
  padding:8px 14px;border:1px dashed var(--border-primary);border-radius:10px;background:var(--bg-secondary);
  font-family:var(--font-mono);font-size:22px;font-weight:600;line-height:1.2;letter-spacing:.14em;user-select:all;
}
.url-field{
  display:flex;align-items:center;gap:6px;min-width:0;min-height:38px;padding:0 4px 0 12px;
  border:1px solid var(--border-color);border-radius:10px;background:var(--bg-secondary);
}
.url-text{flex:1;min-width:0;overflow:hidden;font-family:var(--font-mono);font-size:12px;color:var(--text-tertiary);text-overflow:ellipsis;white-space:nowrap}
.icon-btn{
  display:flex;align-items:center;justify-content:center;flex-shrink:0;width:30px;height:30px;padding:0;
  border:1px solid transparent;border-radius:8px;background:transparent;color:var(--text-secondary);cursor:pointer;
}
.actions{display:flex;flex-wrap:wrap;gap:8px}
.copy-status{margin:-4px 0 0;font-size:12px;color:var(--viz-success)}
.copy-status.bad{color:var(--viz-failure)}
.copy-status:empty{display:none}
.done{display:flex;gap:12px;align-items:flex-start;padding:14px;border:1px solid color-mix(in srgb,var(--viz-success) 30%,transparent);border-radius:12px;background:color-mix(in srgb,var(--viz-success) 7%,transparent)}
.done-icon{display:flex;color:var(--viz-success)}
.done-icon .i{width:20px;height:20px}
.done-title{margin:0 0 6px;font-weight:600}
.done .step-hint{margin:0}
.bar{
  display:flex;align-items:center;min-height:52px;padding:8px 24px;border-top:1px solid var(--border-color);
  background:color-mix(in srgb,var(--bg-secondary) 60%,var(--bg-primary));
}
.status{display:flex;align-items:center;gap:9px;min-width:0;margin:0;font-family:var(--font-mono);font-size:12px;font-weight:500;letter-spacing:.01em}
.dot{flex-shrink:0;width:7px;height:7px;border-radius:50%;background:var(--text-tertiary)}
.dot[data-tone="wait"]{background:var(--text-primary);animation:pulse 1.6s ease-in-out infinite}
.dot[data-tone="ok"]{background:var(--viz-success)}
.dot[data-tone="bad"]{background:var(--viz-failure)}
@keyframes pulse{50%{opacity:.35}}
.sr{position:absolute;width:1px;height:1px;overflow:hidden;clip:rect(0 0 0 0);white-space:nowrap}
@media (max-width:480px){
  .stage{align-items:flex-start;padding:16px 12px}
  .head{padding:18px 18px 4px}
  .body{padding:12px 18px 20px}
  .bar{padding:8px 18px}
  .actions .pill{flex:1 1 100%}
}
/* Touch: every control is at least 44px, and inputs keep 16px text so iOS
   does not zoom in on focus. */
@media (pointer:coarse){
  input,select{min-height:44px;font-size:16px}
  .pill{height:44px}
  .reveal{width:40px;height:40px}
  .icon-btn{width:40px;height:40px}
}
@media (prefers-reduced-motion:reduce){
  .dialog,.dot,.spinner{animation:none}
  .pill:active:not(:disabled){transform:none}
}
`

const loginShellBody = `<main class="stage">
  <section class="dialog" aria-labelledby="kiro-title">
    <header class="head">
      <span class="glyph"><img src="{{logo}}" alt="" width="22" height="22"></span>
      <span class="head-text"><h1 id="kiro-title" data-t="title">Kiro sign-in</h1><span class="caption">Plugin</span></span>
    </header>
    <div class="body">
      <p class="hint" data-t="intro"></p>
      <noscript><p class="alert">This page needs JavaScript.</p></noscript>
      <p id="kiro-notice" class="alert" role="alert" hidden></p>
      <form id="kiro-key-form" class="stack" hidden>
        <div class="field">
          <label for="kiro-key" data-t="keyLabel">Management key</label>
          <span class="secret"><input id="kiro-key" type="password" autocomplete="off" spellcheck="false"><button type="button" class="reveal" data-reveal="kiro-key" aria-pressed="false">{{icon:eye}}</button></span>
          <p class="field-hint" data-t="keyHint"></p>
        </div>
        <button class="pill primary wide" type="submit" data-t="keySubmit">Continue</button>
      </form>
      <form id="kiro-login" class="stack" hidden>
        <fieldset class="methods">
          <legend class="label" data-t="methodLabel">Authentication method</legend>
          <div class="method-grid">
            <label class="method"><input type="radio" name="method" value="builder-id" checked><span>{{icon:user}}<span data-t="m_builder-id">AWS Builder ID</span></span></label>
            <label class="method"><input type="radio" name="method" value="idc"><span>{{icon:building}}<span data-t="m_idc">IAM Identity Center</span></span></label>
            <label class="method"><input type="radio" name="method" value="api_key"><span>{{icon:key}}<span data-t="m_api_key">API key</span></span></label>
            <label class="method"><input type="radio" name="method" value="refresh_token"><span>{{icon:refresh}}<span data-t="m_refresh_token">Refresh token</span></span></label>
            <label class="method"><input type="radio" name="method" value="external_idp"><span>{{icon:json}}<span data-t="m_external_idp">External IDP JSON</span></span></label>
          </div>
          <p id="kiro-method-hint" class="field-hint"></p>
        </fieldset>
        <div class="field" data-for="refresh_token"><label for="f-rtype" data-t="refreshType">Account type</label><select id="f-rtype"><option value="builder-id">AWS Builder ID</option><option value="idc">AWS IAM Identity Center</option></select></div>
        <div class="field" data-for="idc refresh-idc"><label for="f-start" data-t="startURL">Start URL</label><input id="f-start" type="url" inputmode="url" placeholder="https://company.awsapps.com/start" autocomplete="off" spellcheck="false"></div>
        <div class="field" data-for="idc api_key refresh_token"><label for="f-region" data-t="region">AWS Region</label><input id="f-region" value="us-east-1" placeholder="us-east-1" autocomplete="off" spellcheck="false"></div>
        <div class="field" data-for="api_key"><label for="f-apikey" data-t="apiKey">Kiro API key</label><span class="secret"><input id="f-apikey" type="password" autocomplete="off" spellcheck="false"><button type="button" class="reveal" data-reveal="f-apikey" aria-pressed="false">{{icon:eye}}</button></span></div>
        <div class="field" data-for="refresh_token"><label for="f-rtoken" data-t="refreshToken">Refresh token</label><span class="secret"><input id="f-rtoken" type="password" autocomplete="off" spellcheck="false"><button type="button" class="reveal" data-reveal="f-rtoken" aria-pressed="false">{{icon:eye}}</button></span></div>
        <div class="field" data-for="refresh_token"><label for="f-clientid" data-t="clientID">Client ID</label><input id="f-clientid" autocomplete="off" spellcheck="false"></div>
        <div class="field" data-for="refresh_token"><label for="f-clientsecret" data-t="clientSecret">Client secret</label><span class="secret"><input id="f-clientsecret" type="password" autocomplete="off" spellcheck="false"><button type="button" class="reveal" data-reveal="f-clientsecret" aria-pressed="false">{{icon:eye}}</button></span></div>
        <div class="field" data-for="external_idp"><label for="f-json" data-t="externalJSON">Authentication JSON</label><textarea id="f-json" rows="9" maxlength="65536" spellcheck="false"></textarea></div>
        <p id="kiro-error" class="alert" role="alert" hidden></p>
        <button id="kiro-submit" class="pill primary wide" type="submit"><span class="spinner" aria-hidden="true"></span><span id="kiro-submit-label"></span></button>
      </form>
      <ol id="kiro-device" class="steps" hidden>
        <li class="step"><span class="step-index" aria-hidden="true"></span><div class="step-body">
          <p class="step-title" data-t="stepAuthorize"></p>
          <p class="step-hint" data-t="deviceIntro"></p>
          <p id="kiro-code-row" class="code-row"><span class="code-label" data-t="userCode">Device code</span><strong id="kiro-code" class="code"></strong></p>
          <div class="url-field"><span id="kiro-url" class="url-text"></span><button type="button" class="icon-btn" id="kiro-copy-link">{{icon:copy}}</button></div>
          <div class="actions"><a id="kiro-open" class="pill primary" target="_blank" rel="noopener noreferrer"><span data-t="openAWS">Open AWS authorization</span>{{icon:external}}</a><button type="button" class="pill secondary" id="kiro-copy-code">{{icon:copy}}<span data-t="copyCode">Copy device code</span></button></div>
          <p id="kiro-copy-status" class="copy-status" role="status"></p>
        </div></li>
        <li class="step"><span class="step-index" aria-hidden="true"></span><div class="step-body">
          <p class="step-title" data-t="stepReturn"></p>
          <p class="step-hint" data-t="returnHint"></p>
        </div></li>
      </ol>
      <div id="kiro-done" class="done" hidden>
        <span class="done-icon">{{icon:check}}</span>
        <div><p class="done-title" data-t="connected"></p><p class="step-hint" data-t="doneHint"></p></div>
      </div>
    </div>
    <footer class="bar"><p class="status" role="status" aria-live="polite"><span id="kiro-dot" class="dot" data-tone="idle"></span><span id="kiro-status"></span></p></footer>
  </section>
</main>
`

// loginShellText holds the page's strings in the three languages the pages
// ship; the method names and hints follow the Management Center's Kiro card.
const loginShellText = `  var TEXT = {
    en: {
      title: 'Kiro sign-in',
      intro: 'Add a Kiro account to CLIProxyAPI. CPA stores each account as an authentication file.',
      noState: 'This page needs a sign-in session from CPA. Open it from the Kiro card on the OAuth page of the Management Center.',
      methodLabel: 'Authentication method',
      'm_builder-id': 'AWS Builder ID',
      m_idc: 'IAM Identity Center',
      m_api_key: 'API key',
      m_refresh_token: 'Refresh token',
      m_external_idp: 'External IDP JSON',
      'h_builder-id': 'Sign in to a personal AWS Builder ID with device authorization.',
      h_idc: 'Sign in to an organization through AWS IAM Identity Center.',
      h_api_key: 'The API key is validated against Kiro’s model catalog before it is saved.',
      h_refresh_token: 'Import AWS OIDC refresh material and its client registration.',
      h_external_idp: 'Import external_idp JSON that uses an allowlisted Microsoft identity provider.',
      startURL: 'Start URL',
      region: 'AWS Region',
      apiKey: 'Kiro API key',
      refreshType: 'Account type',
      refreshToken: 'Refresh token',
      clientID: 'Client ID',
      clientSecret: 'Client secret',
      externalJSON: 'Authentication JSON',
      authorize: 'Start authorization',
      add: 'Add account',
      working: 'Validating and saving the account…',
      stepAuthorize: 'Approve the sign-in on AWS',
      deviceIntro: 'Open the AWS page, check that it shows the device code below, and approve access.',
      userCode: 'Device code',
      openAWS: 'Open AWS authorization',
      copyLink: 'Copy link',
      copyCode: 'Copy device code',
      copied: 'Copied.',
      copyFailed: 'Copy failed; select the text instead.',
      stepReturn: 'Return to the CPA tab',
      returnHint: 'The Kiro card there finishes on its own once AWS approves. Nothing needs to be pasted back, and this page can be closed.',
      connected: 'Kiro account added.',
      doneHint: 'Return to the CPA tab: the Kiro card finishes on its own. This page can be closed.',
      failed: 'Sign-in failed: {message}',
      show: 'Show',
      hide: 'Hide',
      stSession: 'No sign-in session',
      stKey: 'Waiting for the management key',
      stChoose: 'Choose a sign-in method',
      stDevice: 'Waiting for approval on AWS…',
      stDone: 'Account added',
      stFailed: 'Sign-in failed'
    },
    vi: {
      title: 'Đăng nhập Kiro',
      intro: 'Thêm tài khoản Kiro vào CLIProxyAPI. CPA lưu mỗi tài khoản thành một file xác thực.',
      noState: 'Trang này cần một phiên đăng nhập do CPA tạo. Hãy mở nó từ thẻ Kiro ở trang OAuth của Management Center.',
      methodLabel: 'Phương thức xác thực',
      'm_builder-id': 'AWS Builder ID',
      m_idc: 'IAM Identity Center',
      m_api_key: 'API key',
      m_refresh_token: 'Refresh token',
      m_external_idp: 'External IDP JSON',
      'h_builder-id': 'Đăng nhập tài khoản AWS Builder ID cá nhân bằng mã thiết bị.',
      h_idc: 'Đăng nhập tài khoản doanh nghiệp qua AWS IAM Identity Center.',
      h_api_key: 'API key được kiểm tra với danh sách model Kiro trước khi lưu.',
      h_refresh_token: 'Nhập bộ refresh token và thông tin client đã đăng ký với AWS OIDC.',
      h_external_idp: 'Nhập file xác thực external_idp dùng nhà cung cấp danh tính Microsoft được cho phép.',
      startURL: 'Start URL',
      region: 'AWS Region',
      apiKey: 'API key Kiro',
      refreshType: 'Loại tài khoản',
      refreshToken: 'Refresh token',
      clientID: 'Client ID',
      clientSecret: 'Client secret',
      externalJSON: 'Nội dung JSON xác thực',
      authorize: 'Bắt đầu ủy quyền',
      add: 'Thêm tài khoản',
      working: 'Đang kiểm tra và lưu tài khoản…',
      stepAuthorize: 'Chấp thuận đăng nhập trên AWS',
      deviceIntro: 'Mở trang AWS, kiểm tra mã hiển thị trùng với mã thiết bị bên dưới rồi chấp thuận.',
      userCode: 'Mã thiết bị',
      openAWS: 'Mở trang xác thực AWS',
      copyLink: 'Sao chép liên kết',
      copyCode: 'Sao chép mã thiết bị',
      copied: 'Đã sao chép.',
      copyFailed: 'Không sao chép được; hãy chọn đoạn chữ để chép.',
      stepReturn: 'Quay lại tab CPA',
      returnHint: 'Thẻ Kiro ở đó sẽ tự hoàn tất sau khi AWS chấp thuận. Không cần dán gì vào CPA, có thể đóng trang này.',
      connected: 'Đã thêm tài khoản Kiro.',
      doneHint: 'Quay lại tab CPA: thẻ Kiro sẽ tự hoàn tất. Có thể đóng trang này.',
      failed: 'Đăng nhập thất bại: {message}',
      show: 'Hiện',
      hide: 'Ẩn',
      stSession: 'Thiếu phiên đăng nhập',
      stKey: 'Đang chờ management key',
      stChoose: 'Chọn phương thức đăng nhập',
      stDevice: 'Đang chờ chấp thuận trên AWS…',
      stDone: 'Đã thêm tài khoản',
      stFailed: 'Đăng nhập thất bại'
    },
    'zh-CN': {
      title: 'Kiro 登录',
      intro: '将 Kiro 账户添加到 CLIProxyAPI。CPA 会把每个账户保存为认证文件。',
      noState: '此页面需要由 CPA 创建的登录会话。请从管理中心 OAuth 页面的 Kiro 卡片打开。',
      methodLabel: '认证方式',
      'm_builder-id': 'AWS Builder ID',
      m_idc: 'IAM Identity Center',
      m_api_key: 'API key',
      m_refresh_token: 'Refresh token',
      m_external_idp: 'External IDP JSON',
      'h_builder-id': '使用设备授权登录个人 AWS Builder ID。',
      h_idc: '通过 AWS IAM Identity Center 登录组织账户。',
      h_api_key: '保存前会使用 Kiro 模型目录验证 API key。',
      h_refresh_token: '导入 AWS OIDC refresh token 及客户端注册信息。',
      h_external_idp: '导入使用允许的 Microsoft 身份提供商的 external_idp JSON。',
      startURL: 'Start URL',
      region: 'AWS Region',
      apiKey: 'Kiro API key',
      refreshType: '账户类型',
      refreshToken: 'Refresh token',
      clientID: 'Client ID',
      clientSecret: 'Client secret',
      externalJSON: '认证 JSON',
      authorize: '开始授权',
      add: '添加账户',
      working: '正在验证并保存账户…',
      stepAuthorize: '在 AWS 上批准登录',
      deviceIntro: '打开 AWS 页面，确认显示的代码与下方设备代码一致，然后批准访问。',
      userCode: '设备代码',
      openAWS: '打开 AWS 授权页面',
      copyLink: '复制链接',
      copyCode: '复制设备码',
      copied: '已复制。',
      copyFailed: '复制失败，请手动选择文本。',
      stepReturn: '返回 CPA 标签页',
      returnHint: 'AWS 批准后，那里的 Kiro 卡片会自动完成。无需粘贴任何内容，可以关闭此页面。',
      connected: 'Kiro 账户已添加。',
      doneHint: '请返回 CPA 标签页：Kiro 卡片会自动完成。可以关闭此页面。',
      failed: '登录失败：{message}',
      show: '显示',
      hide: '隐藏',
      stSession: '缺少登录会话',
      stKey: '等待管理密钥',
      stChoose: '选择登录方式',
      stDevice: '等待在 AWS 上批准…',
      stDone: '账户已添加',
      stFailed: '登录失败'
    }
  };
`

// loginShellLogic shows the fields of the chosen method, posts them to
// /connect with the sign-in state, and shows the AWS link and device code or
// the connected result. The status bar names the step the page is on.
const loginShellLogic = `  // The sign-in state CPA issued travels in the fragment, so it never reaches
  // a server log; only the management-key POST to /connect carries it.
  var signInState = new URLSearchParams(window.location.hash.slice(1)).get('state') || '';
  var busy = false;

  function start() {
    var notice = document.getElementById('kiro-notice');
    var keyForm = document.getElementById('kiro-key-form');
    var keyInput = document.getElementById('kiro-key');
    var form = document.getElementById('kiro-login');
    var hint = document.getElementById('kiro-method-hint');
    var submit = document.getElementById('kiro-submit');
    var submitLabel = document.getElementById('kiro-submit-label');
    var formError = document.getElementById('kiro-error');
    var refreshType = document.getElementById('f-rtype');
    var device = document.getElementById('kiro-device');
    var done = document.getElementById('kiro-done');
    var openLink = document.getElementById('kiro-open');
    var urlText = document.getElementById('kiro-url');
    var codeText = document.getElementById('kiro-code');
    var copyStatus = document.getElementById('kiro-copy-status');
    var dot = document.getElementById('kiro-dot');
    var statusText = document.getElementById('kiro-status');

    function method() {
      var checked = form.querySelector('input[name="method"]:checked');
      return checked ? checked.value : 'builder-id';
    }

    function value(id) {
      return document.getElementById(id).value.trim();
    }

    // tone is idle, wait, ok or bad: the colour of the status bar's dot.
    function setStatus(tone, key) {
      dot.setAttribute('data-tone', tone);
      setText(statusText, t(key));
    }

    function showAlert(node, message) {
      node.textContent = message;
      node.hidden = !message;
    }

    // Each field names the methods it belongs to; the refresh-token Start URL
    // only applies when the token was issued through IAM Identity Center.
    function syncFields() {
      var current = method();
      var active = [current];
      if (current === 'refresh_token' && refreshType.value === 'idc') {
        active.push('refresh-idc');
      }
      var fields = form.querySelectorAll('[data-for]');
      for (var index = 0; index < fields.length; index += 1) {
        var wanted = fields[index].getAttribute('data-for').split(' ');
        var show = false;
        for (var w = 0; w < wanted.length; w += 1) {
          if (active.indexOf(wanted[w]) >= 0) {
            show = true;
          }
        }
        fields[index].hidden = !show;
      }
      setText(hint, t('h_' + current));
      setText(submitLabel, t(current === 'builder-id' || current === 'idc' ? 'authorize' : 'add'));
    }

    // Rewriting a node's text replaces its text node. Doing that to the submit
    // button between mousedown and mouseup makes WebKit drop the click, so a
    // node is only written when its text actually changes.
    function setText(node, text) {
      if (node.textContent !== text) {
        node.textContent = text;
      }
    }

    // The key prompt reads as an alert only when CPA rejected a typed key.
    function askKey(rejected) {
      showAlert(notice, rejected ? t('keyRejected') : '');
      keyForm.hidden = false;
      form.hidden = true;
      setStatus('wait', 'stKey');
      keyInput.focus();
    }

    function showForm() {
      showAlert(notice, '');
      keyForm.hidden = true;
      form.hidden = false;
      setStatus('idle', 'stChoose');
    }

    function buildRequest() {
      var current = method();
      var body = { state: signInState, method: current, region: value('f-region') || 'us-east-1' };
      if (current === 'idc') {
        body.start_url = value('f-start');
      } else if (current === 'api_key') {
        body.api_key = value('f-apikey');
      } else if (current === 'refresh_token') {
        body.refresh_auth_method = refreshType.value === 'idc' ? 'idc' : 'builder-id';
        body.refresh_token = value('f-rtoken');
        body.client_id = value('f-clientid');
        body.client_secret = value('f-clientsecret');
        if (body.refresh_auth_method === 'idc') {
          body.start_url = value('f-start');
        }
      } else if (current === 'external_idp') {
        body.credential_json = value('f-json');
      }
      return body;
    }

    // Once /connect answered, the secrets typed into the form are no longer
    // needed on the page.
    function leaveForm() {
      form.hidden = true;
      keyForm.hidden = true;
      var secrets = form.querySelectorAll('input[type="password"], .secret input, textarea');
      for (var index = 0; index < secrets.length; index += 1) {
        secrets[index].value = '';
      }
    }

    function showDevice(url, code) {
      // The plugin validated the URL as an AWS authorization page; this only
      // refuses anything that is not https before it becomes a link.
      if (/^https:\/\//i.test(url)) {
        openLink.href = url;
        urlText.textContent = url;
        urlText.title = url;
      }
      codeText.textContent = code || '';
      document.getElementById('kiro-code-row').hidden = !code;
      document.getElementById('kiro-copy-code').hidden = !code;
      leaveForm();
      device.hidden = false;
      setStatus('wait', 'stDevice');
    }

    function showDone() {
      leaveForm();
      done.hidden = false;
      setStatus('ok', 'stDone');
    }

    function copy(text) {
      var finish = function (ok) {
        copyStatus.className = ok ? 'copy-status' : 'copy-status bad';
        copyStatus.textContent = t(ok ? 'copied' : 'copyFailed');
      };
      if (navigator.clipboard && navigator.clipboard.writeText) {
        navigator.clipboard.writeText(text).then(function () {
          finish(true);
        }, function () {
          finish(false);
        });
      } else {
        finish(false);
      }
    }

    function labelControls() {
      document.getElementById('kiro-copy-link').setAttribute('aria-label', t('copyLink'));
      document.getElementById('kiro-copy-link').title = t('copyLink');
      var toggles = document.querySelectorAll('[data-reveal]');
      for (var index = 0; index < toggles.length; index += 1) {
        var shown = toggles[index].getAttribute('aria-pressed') === 'true';
        toggles[index].setAttribute('aria-label', t(shown ? 'hide' : 'show'));
      }
    }

    // Only the method and the token type change which fields apply. A text
    // field's change event fires on blur, which is the press on the button.
    form.addEventListener('change', function (event) {
      if (event.target && (event.target.name === 'method' || event.target === refreshType)) {
        showAlert(formError, '');
        syncFields();
      }
    });

    form.addEventListener('submit', function (event) {
      event.preventDefault();
      if (busy) {
        return;
      }
      if (!currentKey()) {
        askKey(false);
        return;
      }
      busy = true;
      submit.disabled = true;
      submit.classList.add('busy');
      showAlert(formError, '');
      setStatus('wait', 'working');
      request('connect', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(buildRequest())
      })
        .then(function (response) {
          return response.json();
        })
        .then(function (answer) {
          if (answer && answer.status === 'authorization_required') {
            showDevice(String(answer.url || ''), String(answer.user_code || ''));
          } else if (answer && answer.status === 'connected') {
            showDone();
          } else {
            throw { message: 'unexpected response' };
          }
        })
        .catch(function (error) {
          if (error && error.needKey) {
            askKey(error.rejected);
            return;
          }
          showAlert(formError, t('failed').replace('{message}', error && error.message ? error.message : String(error)));
          setStatus('bad', 'stFailed');
        })
        .then(function () {
          busy = false;
          submit.disabled = false;
          submit.classList.remove('busy');
        });
    });

    keyForm.addEventListener('submit', function (event) {
      event.preventDefault();
      memoryKey = keyInput.value.trim();
      keyInput.value = '';
      if (!memoryKey) {
        return;
      }
      showForm();
      var first = form.querySelector('input[name="method"]:checked');
      if (first) {
        first.focus();
      }
    });

    document.addEventListener('click', function (event) {
      var toggle = event.target && event.target.closest ? event.target.closest('[data-reveal]') : null;
      if (!toggle) {
        return;
      }
      var input = document.getElementById(toggle.getAttribute('data-reveal'));
      var shown = input.type === 'password';
      input.type = shown ? 'text' : 'password';
      toggle.setAttribute('aria-pressed', shown ? 'true' : 'false');
      toggle.setAttribute('aria-label', t(shown ? 'hide' : 'show'));
    });

    document.getElementById('kiro-copy-link').addEventListener('click', function () {
      copy(urlText.textContent);
    });
    document.getElementById('kiro-copy-code').addEventListener('click', function () {
      copy(codeText.textContent);
    });

    localise();
    labelControls();
    syncFields();
    if (!/^[0-9a-f]{16,128}$/i.test(signInState)) {
      showAlert(notice, t('noState'));
      setStatus('bad', 'stSession');
      return;
    }
    if (currentKey()) {
      showForm();
    } else {
      askKey(false);
    }
  }

  whenReady(start);
`
