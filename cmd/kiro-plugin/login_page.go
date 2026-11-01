package main

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

var loginShell = newShellPage("Kiro sign-in", panelBaseCSS+signInCSS+loginShellCSS, shellScript(loginShellText, loginShellLogic), fillShellSlots(loginShellBody))

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

// loginShellCSS centres the dialog on the page, as the panel's own dialog
// sits in the middle of its window.
const loginShellCSS = `html,body{height:100%}
/* The card scrolls with the page when it is taller than the window. */
.stage{display:flex;align-items:center;justify-content:center;min-height:100%;padding:32px 16px}
@media (max-width:480px){
  .stage{align-items:flex-start;padding:16px 12px}
}
`

const loginShellBody = `<main class="stage">
  <section class="dialog" aria-labelledby="kiro-title">
    <header class="head">
      <span class="glyph"><img src="{{logo}}" alt="" width="22" height="22"></span>
      <span class="head-text"><h1 id="kiro-title" class="dialog-title" data-t="title">Kiro sign-in</h1><span class="caption">Plugin</span></span>
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
      {{signin-form}}
    </div>
    ` + signInStatusBar + `
  </section>
</main>
`

// loginShellText holds what only this page says: it opens in a tab of its
// own, and the panel's OAuth card, not this page, finishes the sign-in.
const loginShellText = `  var TEXT = {
    en: {
      title: 'Kiro sign-in',
      intro: 'Add a Kiro account to CLIProxyAPI. CPA stores each account as an authentication file.',
      noState: 'This page needs a sign-in session from CPA. Open it from the Kiro card on the OAuth page of the Management Center.',
      stepReturn: 'Return to the CPA tab',
      returnHint: 'The Kiro card there finishes on its own once AWS approves. Nothing needs to be pasted back, and this page can be closed.',
      connected: 'Kiro account added.',
      doneHint: 'Return to the CPA tab: the Kiro card finishes on its own. This page can be closed.',
      stSession: 'No sign-in session',
      stKey: 'Waiting for the management key'
    },
    vi: {
      title: 'Đăng nhập Kiro',
      intro: 'Thêm tài khoản Kiro vào CLIProxyAPI. CPA lưu mỗi tài khoản thành một file xác thực.',
      noState: 'Trang này cần một phiên đăng nhập do CPA tạo. Hãy mở nó từ thẻ Kiro ở trang OAuth của Management Center.',
      stepReturn: 'Quay lại tab CPA',
      returnHint: 'Thẻ Kiro ở đó sẽ tự hoàn tất sau khi AWS chấp thuận. Không cần dán gì vào CPA, có thể đóng trang này.',
      connected: 'Đã thêm tài khoản Kiro.',
      doneHint: 'Quay lại tab CPA: thẻ Kiro sẽ tự hoàn tất. Có thể đóng trang này.',
      stSession: 'Thiếu phiên đăng nhập',
      stKey: 'Đang chờ management key'
    },
    'zh-CN': {
      title: 'Kiro 登录',
      intro: '将 Kiro 账户添加到 CLIProxyAPI。CPA 会把每个账户保存为认证文件。',
      noState: '此页面需要由 CPA 创建的登录会话。请从管理中心 OAuth 页面的 Kiro 卡片打开。',
      stepReturn: '返回 CPA 标签页',
      returnHint: 'AWS 批准后，那里的 Kiro 卡片会自动完成。无需粘贴任何内容，可以关闭此页面。',
      connected: 'Kiro 账户已添加。',
      doneHint: '请返回 CPA 标签页：Kiro 卡片会自动完成。可以关闭此页面。',
      stSession: '缺少登录会话',
      stKey: '等待管理密钥'
    }
  };
`

// loginShellLogic posts the form to /connect with the state CPA issued and
// shows the AWS link and device code or the connected result. The status bar
// names the step the page is on.
const loginShellLogic = `  // The sign-in state CPA issued travels in the fragment, so it never reaches
  // a server log; only the management-key POST to /connect carries it.
  var signInState = new URLSearchParams(window.location.hash.slice(1)).get('state') || '';

  function start() {
    var notice = byId('kiro-notice');
    var keyForm = byId('kiro-key-form');
    var keyInput = byId('kiro-key');
    var form = byId('kiro-login');

    // The key prompt reads as an alert only when CPA rejected a typed key.
    function askKey(rejected) {
      showAlert(notice, rejected ? t('keyRejected') : '');
      keyForm.hidden = false;
      form.hidden = true;
      setStatus('wait', 'stKey');
      keyInput.focus();
    }

    function openForm() {
      showAlert(notice, '');
      keyForm.hidden = true;
      showForm();
    }

    form.addEventListener('submit', function (event) {
      event.preventDefault();
      if (signing) {
        return;
      }
      if (!currentKey()) {
        askKey(false);
        return;
      }
      connectSignIn(signInState)
        .then(function (step) {
          if (step === 'connected') {
            showDone();
          }
        })
        .catch(function (error) {
          if (error && error.needKey) {
            askKey(error.rejected);
            return;
          }
          signInFailed(error);
        });
    });

    keyForm.addEventListener('submit', function (event) {
      event.preventDefault();
      memoryKey = keyInput.value.trim();
      keyInput.value = '';
      if (!memoryKey) {
        return;
      }
      openForm();
      var first = form.querySelector('input[name="method"]:checked');
      if (first) {
        first.focus();
      }
    });

    bindSignIn();
    localise();
    labelSignIn();
    syncFields();
    if (!/^[0-9a-f]{16,128}$/i.test(signInState)) {
      showAlert(notice, t('noState'));
      setStatus('bad', 'stSession');
      return;
    }
    if (currentKey()) {
      openForm();
    } else {
      askKey(false);
    }
  }

  whenReady(start);
`
