package main

import "strings"

// The sign-in form is drawn twice: alone on the sign-in page the panel's OAuth
// card opens in a tab, and in the Usage page's Add account dialog. Both post
// the same body to /connect, so the dialog chrome, the method form, the device
// steps, their strings and their script live here once and each page adds only
// what differs: where the sign-in state comes from and who finishes the flow.

// shellIcons are Lucide outlines, the icon set the Management Center draws
// with. They are inline because the pages' policy loads nothing.
var shellIcons = map[string]string{
	"user":     `<circle cx="12" cy="8" r="4"/><path d="M4 21v-1a6 6 0 0 1 6-6h4a6 6 0 0 1 6 6v1"/>`,
	"building": `<path d="M6 22V4a2 2 0 0 1 2-2h8a2 2 0 0 1 2 2v18Z"/><path d="M6 12H4a2 2 0 0 0-2 2v6a2 2 0 0 0 2 2h2"/><path d="M18 9h2a2 2 0 0 1 2 2v9a2 2 0 0 1-2 2h-2"/><path d="M10 6h4M10 10h4M10 14h4M10 18h4"/>`,
	"key":      `<path d="M2.586 17.414A2 2 0 0 0 2 18.828V21a1 1 0 0 0 1 1h3a1 1 0 0 0 1-1v-1a1 1 0 0 1 1-1h1a1 1 0 0 0 1-1v-1a1 1 0 0 1 1-1h.172a2 2 0 0 0 1.414-.586l.814-.814a6.5 6.5 0 1 0-4-4z"/><circle cx="16.5" cy="7.5" r=".5"/>`,
	"refresh":  `<path d="M3 12a9 9 0 0 1 9-9 9.75 9.75 0 0 1 6.74 2.74L21 8"/><path d="M21 3v5h-5"/><path d="M21 12a9 9 0 0 1-9 9 9.75 9.75 0 0 1-6.74-2.74L3 16"/><path d="M8 16H3v5"/>`,
	"json":     `<path d="M15 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V7Z"/><path d="M14 2v4a2 2 0 0 0 2 2h4"/><path d="M10 12a1 1 0 0 0-1 1v1a1 1 0 0 1-1 1 1 1 0 0 1 1 1v1a1 1 0 0 0 1 1"/><path d="M14 18a1 1 0 0 0 1-1v-1a1 1 0 0 1 1-1 1 1 0 0 1-1-1v-1a1 1 0 0 0-1-1"/>`,
	"external": `<path d="M15 3h6v6"/><path d="M10 14 21 3"/><path d="M18 13v6a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V8a2 2 0 0 1 2-2h6"/>`,
	"copy":     `<rect width="14" height="14" x="8" y="8" rx="2"/><path d="M4 16c-1.1 0-2-.9-2-2V4c0-1.1.9-2 2-2h10c1.1 0 2 .9 2 2"/>`,
	"eye":      `<path d="M2.062 12.348a1 1 0 0 1 0-.696 10.75 10.75 0 0 1 19.876 0 1 1 0 0 1 0 .696 10.75 10.75 0 0 1-19.876 0"/><circle cx="12" cy="12" r="3"/>`,
	"check":    `<circle cx="12" cy="12" r="10"/><path d="m9 12 2 2 4-4"/>`,
	"plus":     `<path d="M5 12h14"/><path d="M12 5v14"/>`,
	"search":   `<circle cx="11" cy="11" r="8"/><path d="m21 21-4.3-4.3"/>`,
	"x":        `<path d="M18 6 6 18"/><path d="m6 6 12 12"/>`,
	"chevron":  `<path d="m6 9 6 6 6-6"/>`,
}

// fillShellSlots fills a page body's {{logo}}, {{icon:name}} and
// {{signin-form}} slots.
func fillShellSlots(body string) string {
	pairs := []string{"{{logo}}", pluginLogoDataURI()}
	for name, paths := range shellIcons {
		pairs = append(pairs, "{{icon:"+name+"}}", `<svg class="i" viewBox="0 0 24 24" aria-hidden="true" focusable="false">`+paths+`</svg>`)
	}
	replacer := strings.NewReplacer(pairs...)
	return replacer.Replace(strings.ReplaceAll(body, "{{signin-form}}", signInFormBody))
}

// signInCSS draws the panel's OAuth dialog (OAuthFlowDialog.module.scss of the
// Management Center): a rounded card, a logo glyph beside the title, the body,
// and a status bar along the bottom.
const signInCSS = `.dialog{
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
.dialog-title{margin:0;font-size:17px;font-weight:650;line-height:1.25;letter-spacing:-.015em}
.caption{font-family:var(--font-mono);font-size:11.5px;font-weight:500;letter-spacing:.02em;color:var(--text-tertiary)}
.body{display:flex;flex-direction:column;gap:18px;padding:14px 24px 24px}
.hint{margin:0;font-size:13px;line-height:1.65;color:var(--text-secondary)}
.stack{display:flex;flex-direction:column;gap:16px;margin:0}
.legend,.field label{font-size:13px;font-weight:600;color:var(--text-primary)}
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
.secret{position:relative;display:block}
.secret input{padding-right:46px}
.reveal{
  position:absolute;top:50%;right:4px;transform:translateY(-50%);
  display:flex;align-items:center;justify-content:center;width:36px;height:36px;padding:0;
  border:0;border-radius:8px;background:transparent;color:var(--text-tertiary);cursor:pointer;
}
.reveal[aria-pressed="true"]{color:var(--text-primary)}
.reveal:focus-visible,.icon-btn:focus-visible{outline:2px solid var(--primary-color);outline-offset:1px}
.icon-btn{
  display:flex;align-items:center;justify-content:center;flex-shrink:0;width:30px;height:30px;padding:0;
  border:1px solid transparent;border-radius:8px;background:transparent;color:var(--text-secondary);cursor:pointer;
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
.btn-row{display:flex;flex-wrap:wrap;gap:8px}
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
.tone{flex-shrink:0;width:7px;height:7px;border-radius:50%;background:var(--text-tertiary)}
.tone[data-tone="wait"]{background:var(--text-primary);animation:pulse 1.6s ease-in-out infinite}
.tone[data-tone="ok"]{background:var(--viz-success)}
.tone[data-tone="bad"]{background:var(--viz-failure)}
@keyframes pulse{50%{opacity:.35}}
@media (hover:hover) and (pointer:fine){
  .icon-btn:hover{border-color:var(--border-hover);background:var(--bg-tertiary)}
  .method>span:hover{border-color:var(--border-hover);color:var(--text-primary)}
  .reveal:hover{color:var(--text-primary)}
}
@media (max-width:480px){
  .head{padding:18px 18px 4px}
  .body{padding:12px 18px 20px}
  .bar{padding:8px 18px}
  .btn-row .pill{flex:1 1 100%}
}
@media (pointer:coarse){
  .reveal{width:40px;height:40px}
  .icon-btn{width:40px;height:40px}
}
@media (prefers-reduced-motion:reduce){
  .dialog,.tone{animation:none}
}
`

// signInFormBody is the method form, the AWS device steps and the done note.
// Step two and the done note are worded by each page (stepReturn, returnHint,
// connected, doneHint), because only the page knows who finishes the flow.
const signInFormBody = `<form id="kiro-login" class="stack" hidden>
        <fieldset class="methods">
          <legend class="legend" data-t="methodLabel">Authentication method</legend>
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
          <div class="btn-row"><a id="kiro-open" class="pill primary" target="_blank" rel="noopener noreferrer"><span data-t="openAWS">Open AWS authorization</span>{{icon:external}}</a><button type="button" class="pill secondary" id="kiro-copy-code">{{icon:copy}}<span data-t="copyCode">Copy device code</span></button></div>
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
      </div>`

// signInStatusBar is the dialog's footer: a dot and the step the flow is on.
const signInStatusBar = `<footer class="bar"><p class="status" role="status" aria-live="polite"><span id="kiro-tone" class="tone" data-tone="idle"></span><span id="kiro-status"></span></p></footer>`

// signInText holds the form's strings in the three languages the pages ship;
// the method names and hints follow the Management Center's Kiro card.
const signInText = `  var SIGNIN_TEXT = {
    en: {
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
      failed: 'Sign-in failed: {message}',
      show: 'Show',
      hide: 'Hide',
      stChoose: 'Choose a sign-in method',
      stDevice: 'Waiting for approval on AWS…',
      stDone: 'Account added',
      stFailed: 'Sign-in failed'
    },
    vi: {
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
      failed: 'Đăng nhập thất bại: {message}',
      show: 'Hiện',
      hide: 'Ẩn',
      stChoose: 'Chọn phương thức đăng nhập',
      stDevice: 'Đang chờ chấp thuận trên AWS…',
      stDone: 'Đã thêm tài khoản',
      stFailed: 'Đăng nhập thất bại'
    },
    'zh-CN': {
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
      failed: '登录失败：{message}',
      show: '显示',
      hide: '隐藏',
      stChoose: '选择登录方式',
      stDevice: '等待在 AWS 上批准…',
      stDone: '账户已添加',
      stFailed: '登录失败'
    }
  };
`

// signInScript shows the fields of the chosen method, builds the /connect body
// and draws the device steps and the done note. Each page calls connectSignIn
// with its own sign-in state and decides what happens after it.
const signInScript = `  function byId(id) {
    return document.getElementById(id);
  }

  // Rewriting a node's text replaces its text node. Doing that to the submit
  // button between mousedown and mouseup makes WebKit drop the click, so a
  // node is only written when its text actually changes.
  function setText(node, text) {
    if (node.textContent !== text) {
      node.textContent = text;
    }
  }

  function showAlert(node, message) {
    node.textContent = message;
    node.hidden = !message;
  }

  // tone is idle, wait, ok or bad: the colour of the status bar's dot.
  function setStatus(tone, key) {
    byId('kiro-tone').setAttribute('data-tone', tone);
    setText(byId('kiro-status'), t(key));
  }

  function formMethod() {
    var checked = byId('kiro-login').querySelector('input[name="method"]:checked');
    return checked ? checked.value : 'builder-id';
  }

  function chooseMethod(value) {
    var radio = byId('kiro-login').querySelector('input[name="method"][value="' + value + '"]') ||
      byId('kiro-login').querySelector('input[name="method"]');
    radio.checked = true;
  }

  function formValue(id) {
    return byId(id).value.trim();
  }

  // Each field names the methods it belongs to; the refresh-token Start URL
  // only applies when the token was issued through IAM Identity Center.
  function syncFields() {
    var current = formMethod();
    var active = [current];
    if (current === 'refresh_token' && byId('f-rtype').value === 'idc') {
      active.push('refresh-idc');
    }
    var fields = byId('kiro-login').querySelectorAll('[data-for]');
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
    setText(byId('kiro-method-hint'), t('h_' + current));
    setText(byId('kiro-submit-label'), t(current === 'builder-id' || current === 'idc' ? 'authorize' : 'add'));
  }

  function buildRequest(state) {
    var current = formMethod();
    var body = { state: state, method: current, region: formValue('f-region') || 'us-east-1' };
    if (current === 'idc') {
      body.start_url = formValue('f-start');
    } else if (current === 'api_key') {
      body.api_key = formValue('f-apikey');
    } else if (current === 'refresh_token') {
      body.refresh_auth_method = byId('f-rtype').value === 'idc' ? 'idc' : 'builder-id';
      body.refresh_token = formValue('f-rtoken');
      body.client_id = formValue('f-clientid');
      body.client_secret = formValue('f-clientsecret');
      if (body.refresh_auth_method === 'idc') {
        body.start_url = formValue('f-start');
      }
    } else if (current === 'external_idp') {
      body.credential_json = formValue('f-json');
    }
    return body;
  }

  // Once /connect answered, the secrets typed into the form are no longer
  // needed on the page.
  function clearSecrets() {
    var secrets = byId('kiro-login').querySelectorAll('.secret input, textarea');
    for (var index = 0; index < secrets.length; index += 1) {
      secrets[index].value = '';
      if (secrets[index].tagName === 'INPUT') {
        secrets[index].type = 'password';
      }
    }
    var toggles = byId('kiro-login').querySelectorAll('[data-reveal]');
    for (var step = 0; step < toggles.length; step += 1) {
      toggles[step].setAttribute('aria-pressed', 'false');
      toggles[step].setAttribute('aria-label', t('show'));
    }
  }

  // showForm returns the dialog to the method form, keeping what was typed.
  function showForm() {
    byId('kiro-login').hidden = false;
    byId('kiro-device').hidden = true;
    byId('kiro-done').hidden = true;
    setStatus('idle', 'stChoose');
    syncFields();
  }

  function showDevice(url, code) {
    // The plugin validated the URL as an AWS authorization page; this only
    // refuses anything that is not https before it becomes a link.
    var open = byId('kiro-open');
    var text = byId('kiro-url');
    open.removeAttribute('href');
    text.textContent = '';
    if (/^https:\/\//i.test(url)) {
      open.href = url;
      text.textContent = url;
      text.title = url;
    }
    byId('kiro-code').textContent = code || '';
    byId('kiro-code-row').hidden = !code;
    byId('kiro-copy-code').hidden = !code;
    setText(byId('kiro-copy-status'), '');
    byId('kiro-login').hidden = true;
    clearSecrets();
    byId('kiro-device').hidden = false;
    setStatus('wait', 'stDevice');
  }

  function showDone() {
    byId('kiro-login').hidden = true;
    byId('kiro-device').hidden = true;
    clearSecrets();
    byId('kiro-done').hidden = false;
    setStatus('ok', 'stDone');
  }

  function copyText(text) {
    var note = byId('kiro-copy-status');
    var finish = function (ok) {
      note.className = ok ? 'copy-status' : 'copy-status bad';
      note.textContent = t(ok ? 'copied' : 'copyFailed');
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

  function labelSignIn() {
    byId('kiro-copy-link').setAttribute('aria-label', t('copyLink'));
    byId('kiro-copy-link').title = t('copyLink');
    var toggles = document.querySelectorAll('[data-reveal]');
    for (var index = 0; index < toggles.length; index += 1) {
      var shown = toggles[index].getAttribute('aria-pressed') === 'true';
      toggles[index].setAttribute('aria-label', t(shown ? 'hide' : 'show'));
    }
  }

  var signing = false;

  // connectSignIn posts the form to /connect under state. It resolves with
  // 'device' once the AWS steps are on screen, or 'connected' when the plugin
  // already holds the credential; a refusal is shown in the form.
  function connectSignIn(state) {
    var submit = byId('kiro-submit');
    signing = true;
    submit.disabled = true;
    submit.classList.add('busy');
    showAlert(byId('kiro-error'), '');
    setStatus('wait', 'working');
    return request('connect', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(buildRequest(state))
    })
      .then(function (response) {
        return response.json();
      })
      .then(function (answer) {
        if (answer && answer.status === 'authorization_required') {
          showDevice(String(answer.url || ''), String(answer.user_code || ''));
          return 'device';
        }
        if (answer && answer.status === 'connected') {
          return 'connected';
        }
        throw { message: 'unexpected response' };
      })
      .then(function (step) {
        endSigning();
        return step;
      }, function (error) {
        endSigning();
        throw error;
      });
  }

  function endSigning() {
    signing = false;
    byId('kiro-submit').disabled = false;
    byId('kiro-submit').classList.remove('busy');
  }

  function signInFailed(error) {
    showForm();
    showAlert(byId('kiro-error'), t('failed').replace('{message}', error && error.message ? error.message : String(error)));
    setStatus('bad', 'stFailed');
  }

  // Only the method and the token type change which fields apply. A text
  // field's change event fires on blur, which is the press on the button.
  function bindSignIn() {
    var form = byId('kiro-login');
    form.addEventListener('change', function (event) {
      if (event.target && (event.target.name === 'method' || event.target.id === 'f-rtype')) {
        showAlert(byId('kiro-error'), '');
        syncFields();
      }
    });
    document.addEventListener('click', function (event) {
      var toggle = event.target && event.target.closest ? event.target.closest('[data-reveal]') : null;
      if (!toggle) {
        return;
      }
      var input = byId(toggle.getAttribute('data-reveal'));
      var shown = input.type === 'password';
      input.type = shown ? 'text' : 'password';
      toggle.setAttribute('aria-pressed', shown ? 'true' : 'false');
      toggle.setAttribute('aria-label', t(shown ? 'hide' : 'show'));
    });
    byId('kiro-copy-link').addEventListener('click', function () {
      copyText(byId('kiro-url').textContent);
    });
    byId('kiro-copy-code').addEventListener('click', function () {
      copyText(byId('kiro-code').textContent);
    });
  }
`
