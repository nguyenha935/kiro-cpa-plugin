package main

// The sign-in page lets the stock Management Center add a Kiro account. Its
// OAuth page shows a plugin card that calls StartLogin and polls the returned
// state, but it has no Kiro form: the method and its fields go to the
// authenticated /connect route. StartLogin therefore returns this page as the
// card's URL, with the state in the fragment, and the page posts the chosen
// method to /connect with the management key. The card's own polling then
// completes the sign-in; this page never polls, because a successful poll
// consumes the flow and the card would then report it as lost.

const loginResourcePath = "/login"

var loginShell = newShellPage("Kiro sign-in", loginShellCSS, shellScript(loginShellText, loginShellLogic), loginShellBody)

// loginURL is relative: CPA hands StartLogin only its loopback callback base,
// and the Management Center opens the link from its own origin, which is the
// CPA that serves this page.
func loginURL(state string) string {
	return resourceBasePath + loginResourcePath + "#state=" + state
}

func handleLoginShell() ([]byte, error) {
	return shellResponse(loginShell)
}

const loginShellCSS = `main{width:min(720px,100%);margin:0 auto;padding:28px 20px 40px}
.login{display:grid;gap:14px;margin:18px 0 0}
.methods{display:flex;flex-wrap:wrap;gap:6px;margin:0;padding:0;border:0;min-width:0}
.method{position:relative}
.method input{position:absolute;opacity:0;pointer-events:none}
.method span{
  display:inline-flex;align-items:center;min-height:30px;padding:0 12px;border:1px solid var(--border-2);
  border-radius:999px;background:var(--surface);color:var(--text-2);font-size:12px;font-weight:500;cursor:pointer;
}
.method input:checked+span{border-color:var(--accent);color:var(--text);box-shadow:inset 0 0 0 1px var(--accent)}
.method input:focus-visible+span{outline:2px solid var(--accent);outline-offset:1px}
.method-hint{margin:0;color:var(--muted);font-size:12px}
.field{display:grid;gap:5px}
.field label{color:var(--text-2);font-size:12px;font-weight:500}
.field input,.field select,.field textarea{
  min-height:32px;padding:6px 10px;border:1px solid var(--border-2);border-radius:8px;
  background:var(--surface);color:var(--text);font:inherit;
}
.field textarea{font-family:ui-monospace,SFMono-Regular,Menlo,Consolas,monospace;font-size:12px;resize:vertical}
.field input:focus-visible,.field select:focus-visible,.field textarea:focus-visible{outline:2px solid var(--accent);outline-offset:1px}
.login-actions{margin:4px 0 0}
/* The page colour on the accent measures 5.3:1 (light) and 5.9:1 (dark);
   white on the dark theme's lighter accent was 3.2:1. */
.act.go{border-color:var(--accent);background:var(--accent);color:var(--page)}
.act.go:hover{color:var(--page);filter:brightness(1.08)}
.status{margin:14px 0 0;color:var(--text-2);font-size:12px}
.notice{margin-top:14px}
.result{display:grid;gap:12px;margin:18px 0 0;padding:14px 16px;border:1px solid var(--border);border-radius:10px;background:var(--surface)}
.result-text{margin:0;font-weight:500}
.device{display:grid;gap:10px}
.row{display:flex;flex-wrap:wrap;align-items:center;gap:8px;margin:0}
.code{font-size:20px;font-weight:600;letter-spacing:.12em;font-variant-numeric:tabular-nums}
.url{margin:0;color:var(--muted);font-size:12px;overflow-wrap:anywhere}
.copy-status{color:var(--muted);font-size:12px}
`

const loginShellBody = `<main>
  <header class="page-head">
    <h1 data-t="title">Kiro sign-in</h1>
    <p class="intro" data-t="intro"></p>
  </header>
  <noscript><p class="notice">This page needs JavaScript.</p></noscript>
  <p id="kiro-notice" class="status" role="status" aria-live="polite" hidden></p>
  <form id="kiro-key-form" class="keyform" hidden>
    <p id="kiro-key-state" class="intro"></p>
    <label for="kiro-key" data-t="keyLabel">Management key</label>
    <span class="keyrow"><input id="kiro-key" type="password" autocomplete="off" spellcheck="false"><button class="act" type="submit" data-t="keySubmit">Continue</button></span>
    <p class="keyhint" data-t="keyHint"></p>
  </form>
  <form id="kiro-login" class="login" hidden>
    <fieldset class="methods">
      <legend class="sr" data-t="methodLabel">Authentication method</legend>
      <label class="method"><input type="radio" name="method" value="builder-id" checked><span data-t="m_builder-id">AWS Builder ID</span></label>
      <label class="method"><input type="radio" name="method" value="idc"><span data-t="m_idc">IAM Identity Center</span></label>
      <label class="method"><input type="radio" name="method" value="api_key"><span data-t="m_api_key">API key</span></label>
      <label class="method"><input type="radio" name="method" value="refresh_token"><span data-t="m_refresh_token">Refresh token</span></label>
      <label class="method"><input type="radio" name="method" value="external_idp"><span data-t="m_external_idp">External IDP JSON</span></label>
    </fieldset>
    <p id="kiro-method-hint" class="method-hint"></p>
    <div class="field" data-for="refresh_token"><label for="f-rtype" data-t="refreshType">Account type</label><select id="f-rtype"><option value="builder-id">AWS Builder ID</option><option value="idc">AWS IAM Identity Center</option></select></div>
    <div class="field" data-for="idc refresh-idc"><label for="f-start" data-t="startURL">Start URL</label><input id="f-start" type="url" inputmode="url" placeholder="https://company.awsapps.com/start" autocomplete="off" spellcheck="false"></div>
    <div class="field" data-for="idc api_key refresh_token"><label for="f-region" data-t="region">AWS Region</label><input id="f-region" value="us-east-1" placeholder="us-east-1" autocomplete="off" spellcheck="false"></div>
    <div class="field" data-for="api_key"><label for="f-apikey" data-t="apiKey">Kiro API key</label><input id="f-apikey" type="password" autocomplete="off" spellcheck="false"></div>
    <div class="field" data-for="refresh_token"><label for="f-rtoken" data-t="refreshToken">Refresh token</label><input id="f-rtoken" type="password" autocomplete="off" spellcheck="false"></div>
    <div class="field" data-for="refresh_token"><label for="f-clientid" data-t="clientID">Client ID</label><input id="f-clientid" autocomplete="off" spellcheck="false"></div>
    <div class="field" data-for="refresh_token"><label for="f-clientsecret" data-t="clientSecret">Client secret</label><input id="f-clientsecret" type="password" autocomplete="off" spellcheck="false"></div>
    <div class="field" data-for="external_idp"><label for="f-json" data-t="externalJSON">Authentication JSON</label><textarea id="f-json" rows="9" maxlength="65536" spellcheck="false"></textarea></div>
    <p class="login-actions"><button id="kiro-submit" class="act go" type="submit"></button></p>
  </form>
  <section id="kiro-result" class="result" aria-live="polite" hidden>
    <p id="kiro-result-text" class="result-text"></p>
    <div id="kiro-device" class="device" hidden>
      <p id="kiro-code-row" class="row"><span data-t="userCode">Device code</span><strong id="kiro-code" class="code"></strong></p>
      <p class="row"><a id="kiro-open" class="act go" target="_blank" rel="noopener noreferrer" data-t="openAWS">Open AWS authorization</a><button type="button" class="act" id="kiro-copy-link" data-t="copyLink">Copy link</button><button type="button" class="act" id="kiro-copy-code" data-t="copyCode">Copy device code</button><span id="kiro-copy-status" class="copy-status" role="status"></span></p>
      <p id="kiro-url" class="url"></p>
    </div>
    <p class="keyhint" data-t="returnHint"></p>
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
      keyPrompt: 'Enter the CLIProxyAPI management key to add a Kiro account.',
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
      deviceIntro: 'Open the AWS page, check that it shows the device code below, and approve access.',
      userCode: 'Device code',
      openAWS: 'Open AWS authorization',
      copyLink: 'Copy link',
      copyCode: 'Copy device code',
      copied: 'Copied.',
      copyFailed: 'Copy failed; select the text instead.',
      connected: 'Kiro account added.',
      returnHint: 'Return to the CPA tab: the Kiro card finishes on its own. Nothing needs to be pasted back, and this page can be closed.',
      failed: 'Sign-in failed: {message}'
    },
    vi: {
      title: 'Đăng nhập Kiro',
      intro: 'Thêm tài khoản Kiro vào CLIProxyAPI. CPA lưu mỗi tài khoản thành một file xác thực.',
      noState: 'Trang này cần một phiên đăng nhập do CPA tạo. Hãy mở nó từ thẻ Kiro ở trang OAuth của Management Center.',
      keyPrompt: 'Nhập management key của CLIProxyAPI để thêm tài khoản Kiro.',
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
      deviceIntro: 'Mở trang AWS, kiểm tra mã hiển thị trùng với mã thiết bị bên dưới rồi chấp thuận.',
      userCode: 'Mã thiết bị',
      openAWS: 'Mở trang xác thực AWS',
      copyLink: 'Sao chép liên kết',
      copyCode: 'Sao chép mã thiết bị',
      copied: 'Đã sao chép.',
      copyFailed: 'Không sao chép được; hãy bôi đen để chép.',
      connected: 'Đã thêm tài khoản Kiro.',
      returnHint: 'Quay lại tab CPA: thẻ Kiro sẽ tự hoàn tất. Không cần dán gì vào CPA, có thể đóng trang này.',
      failed: 'Đăng nhập thất bại: {message}'
    },
    'zh-CN': {
      title: 'Kiro 登录',
      intro: '将 Kiro 账户添加到 CLIProxyAPI。CPA 会把每个账户保存为认证文件。',
      noState: '此页面需要由 CPA 创建的登录会话。请从管理中心 OAuth 页面的 Kiro 卡片打开。',
      keyPrompt: '请输入 CLIProxyAPI 管理密钥以添加 Kiro 账户。',
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
      deviceIntro: '打开 AWS 页面，确认显示的代码与下方设备代码一致，然后批准访问。',
      userCode: '设备代码',
      openAWS: '打开 AWS 授权页面',
      copyLink: '复制链接',
      copyCode: '复制设备码',
      copied: '已复制。',
      copyFailed: '复制失败，请手动选择文本。',
      connected: 'Kiro 账户已添加。',
      returnHint: '请返回 CPA 标签页：Kiro 卡片会自动完成。无需粘贴任何内容，可以关闭此页面。',
      failed: '登录失败：{message}'
    }
  };
`

// loginShellLogic shows the fields of the chosen method, posts them to
// /connect with the sign-in state, and shows the AWS link and device code or
// the connected result.
const loginShellLogic = `  // The sign-in state CPA issued travels in the fragment, so it never reaches
  // a server log; only the management-key POST to /connect carries it.
  var signInState = new URLSearchParams(window.location.hash.slice(1)).get('state') || '';
  var busy = false;

  function start() {
    var notice = document.getElementById('kiro-notice');
    var keyForm = document.getElementById('kiro-key-form');
    var keyInput = document.getElementById('kiro-key');
    var keyState = document.getElementById('kiro-key-state');
    var form = document.getElementById('kiro-login');
    var hint = document.getElementById('kiro-method-hint');
    var submit = document.getElementById('kiro-submit');
    var refreshType = document.getElementById('f-rtype');
    var result = document.getElementById('kiro-result');
    var resultText = document.getElementById('kiro-result-text');
    var device = document.getElementById('kiro-device');
    var openLink = document.getElementById('kiro-open');
    var urlText = document.getElementById('kiro-url');
    var codeText = document.getElementById('kiro-code');
    var copyStatus = document.getElementById('kiro-copy-status');

    function method() {
      var checked = form.querySelector('input[name="method"]:checked');
      return checked ? checked.value : 'builder-id';
    }

    function value(id) {
      return document.getElementById(id).value.trim();
    }

    // An error reads as a notice; progress reads as plain status text.
    function showNotice(message, isError) {
      notice.textContent = message;
      notice.className = isError ? 'notice' : 'status';
      notice.hidden = !message;
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
      setText(submit, t(current === 'builder-id' || current === 'idc' ? 'authorize' : 'add'));
    }

    // Rewriting a node's text replaces its text node. Doing that to the submit
    // button between mousedown and mouseup makes WebKit drop the click, so a
    // node is only written when its text actually changes.
    function setText(node, text) {
      if (node.textContent !== text) {
        node.textContent = text;
      }
    }

    function askKey(message) {
      keyState.textContent = message;
      keyForm.hidden = false;
      form.hidden = true;
      keyInput.focus();
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

    function finish(message) {
      form.hidden = true;
      keyForm.hidden = true;
      showNotice('', false);
      resultText.textContent = message;
      result.hidden = false;
    }

    function showDevice(url, code) {
      // The plugin validated the URL as an AWS authorization page; this only
      // refuses anything that is not https before it becomes a link.
      if (/^https:\/\//i.test(url)) {
        openLink.href = url;
        urlText.textContent = url;
      }
      codeText.textContent = code || '';
      document.getElementById('kiro-code-row').hidden = !code;
      document.getElementById('kiro-copy-code').hidden = !code;
      device.hidden = false;
      finish(t('deviceIntro'));
    }

    function copy(text) {
      var done = function (ok) {
        copyStatus.textContent = t(ok ? 'copied' : 'copyFailed');
      };
      if (navigator.clipboard && navigator.clipboard.writeText) {
        navigator.clipboard.writeText(text).then(function () {
          done(true);
        }, function () {
          done(false);
        });
      } else {
        done(false);
      }
    }

    // Only the method and the token type change which fields apply. A text
    // field's change event fires on blur, which is the press on the button.
    form.addEventListener('change', function (event) {
      if (event.target && (event.target.name === 'method' || event.target === refreshType)) {
        syncFields();
      }
    });

    form.addEventListener('submit', function (event) {
      event.preventDefault();
      if (busy) {
        return;
      }
      if (!currentKey()) {
        askKey(t('keyPrompt'));
        return;
      }
      busy = true;
      submit.disabled = true;
      showNotice(t('working'), false);
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
            finish(t('connected'));
          } else {
            throw { message: 'unexpected response' };
          }
        })
        .catch(function (error) {
          if (error && error.needKey) {
            showNotice('', false);
            askKey(error.rejected ? t('keyRejected') : t('keyPrompt'));
            return;
          }
          showNotice(t('failed').replace('{message}', error && error.message ? error.message : String(error)), true);
        })
        .then(function () {
          busy = false;
          submit.disabled = false;
        });
    });

    keyForm.addEventListener('submit', function (event) {
      event.preventDefault();
      memoryKey = keyInput.value.trim();
      keyInput.value = '';
      if (!memoryKey) {
        return;
      }
      keyForm.hidden = true;
      form.hidden = false;
      var first = form.querySelector('input[name="method"]:checked');
      if (first) {
        first.focus();
      }
    });

    document.getElementById('kiro-copy-link').addEventListener('click', function () {
      copy(urlText.textContent);
    });
    document.getElementById('kiro-copy-code').addEventListener('click', function () {
      copy(codeText.textContent);
    });

    localise();
    syncFields();
    if (!/^[0-9a-f]{16,128}$/i.test(signInState)) {
      showNotice(t('noState'), true);
      return;
    }
    if (currentKey()) {
      form.hidden = false;
    } else {
      askKey(t('keyPrompt'));
    }
  }

  whenReady(start);
`
