package main

import "html/template"

// usagePageTemplate is parsed once with placeholder helpers; renderUsagePage
// clones it per response and rebinds the helpers to the requested language.
var usagePageTemplate = template.Must(
	template.New("kiro-usage").Funcs(usagePageFuncs(usagePageTextPacks[usageLangEN])).Parse(usageViewHTML),
)

// The usage page is split in two so the resource route serves nothing but
// static bytes. CPA serves resource routes without the management key, so the
// shell holds only the layout and its script; the account data comes from
// usageViewHTML on the authenticated view route, and enable/disable is a POST
// to the credential route. The shell's CSP admits exactly its own script by
// hash, so the fragment it injects can never run one.
var (
	usageShell       = newShellPage("Kiro Usage", usageShellCSS, usageShellScript, usageShellBody)
	usageShellPage   = usageShell.Body
	usageShellPolicy = usageShell.Policy
	usageShellCSP    = usageShell.Header
	usageShellScript = shellScript(usageShellText, usageShellLogic)
)

const usageShellBody = `<main>
  <section id="kiro-gate" class="page-head gate">
    <h1 data-t="title">Kiro Usage</h1>
    <p id="kiro-state" class="intro" role="status" aria-live="polite"></p>
    <noscript><p class="intro">This page needs JavaScript.</p></noscript>
    <form id="kiro-key-form" class="keyform" hidden>
      <label for="kiro-key" data-t="keyLabel">Management key</label>
      <span class="keyrow"><input id="kiro-key" type="password" autocomplete="off" spellcheck="false"><button class="act" type="submit" data-t="keySubmit">Open</button></span>
      <p class="keyhint" data-t="keyHint"></p>
    </form>
  </section>
  <p id="kiro-flash" class="notice flash" role="alert" hidden></p>
  <div id="kiro-usage" aria-busy="false"></div>
</main>
`

// The page is a comparison table with a fleet totals strip above it and a totals
// row below it, so the same figure of every account reads down one column and
// the whole fleet reads in one line.
//
// Three constraints were measured rather than assumed:
//
//   - The CLIProxyAPI panel serves this page inside an iframe and paints its own
//     .main-header{position:fixed;top:0;left:0;right:0;z-index:1001} over it, with
//     .header-actions{top:24px;right:24px} holding 36-38px buttons. The top right
//     corner of the viewport is therefore not ours: roughly 176x86px. The title
//     row reserves that width and nothing clickable is placed there.
//   - getUsageLimits returns exactly one bucket (resourceType CREDIT) on both
//     credential kinds, and answers HTTP 400 "Improperly formed request" for
//     SPEC_REQUEST, VIBE_REQUEST, CODE_REVIEW, TRANSFORMATION and
//     INLINE_SUGGESTION, with or without the resourceType parameter. There is no
//     second quota to show; what the page was missing is the nine money and plan
//     figures AWS reports beside the counters.
//   - AWS reported daysUntilReset = 0 on both accounts while nextDateReset was 26
//     days out, so the reset column counts down from the timestamp and the AWS
//     field is shown as its own labelled fact instead of as the headline.
const usageShellCSS = `main{width:min(1560px,100%);margin:0 auto;padding:18px 20px 26px}
/* The panel's floating button cluster sits over this page's top right corner
   (fixed, 24px inset, 36-38px controls). The title row keeps that corner clear
   instead of putting text under it. */
.page-head{padding-right:clamp(150px,17vw,208px)}

/* Fleet totals: the same figures as the last table row, read before the detail. */
/* Wrapping flex rather than a grid: a grid left the unfilled end of its last
   row showing the border colour as a dark block. Here the last row's items
   grow to fill it. */
.totals{
  display:flex;flex-wrap:wrap;gap:1px;margin:14px 0 0;overflow:hidden;
  border:1px solid var(--border);border-radius:10px;background:var(--border);
}
.total{flex:1 1 168px;padding:9px 13px 10px;background:var(--surface)}
.total dt{margin:0;color:var(--muted);font-size:12px;font-weight:400}
.total dd{margin:1px 0 0;font-size:15px;font-weight:600;letter-spacing:-.01em;font-variant-numeric:tabular-nums}
.total dd small{color:var(--muted);font-size:12px;font-weight:400;letter-spacing:0}
.total.wide{flex-basis:337px}

/* A table narrower than its columns scrolls sideways instead of squeezing the
   text columns; the squeeze is what broke "Kiro Free" into one letter a line. */
.table-wrap{
  margin:12px 0 0;border:1px solid var(--border);border-radius:10px;
  background:var(--surface);box-shadow:var(--shadow);overflow-x:auto;
}
table{width:100%;border-collapse:separate;border-spacing:0;font-variant-numeric:tabular-nums}
/* Vertical centring is the fix for the misaligned rows: figures of different
   line counts sat on the top edge of their cell before. */
th,td{padding:11px 14px;text-align:left;vertical-align:middle}
/* A cell spanning an account's pool rows aligns with the first of them instead of
   floating in the middle of the group. */
td[rowspan]{vertical-align:top}
/* Column heads are sentence case at the body's own letter spacing. Uppercase
   with tracking is what made them read as cramped and shouted. */
thead th{
  position:sticky;top:0;z-index:2;background:var(--surface-2);color:var(--muted);
  font-size:12px;font-weight:500;line-height:1.35;white-space:nowrap;
  border-bottom:1px solid var(--border);
}
thead th small{display:block;color:var(--muted);font-size:12px;font-weight:400;opacity:.85}
th.num,td.num{text-align:right}
tbody.account>tr>td{border-top:1px solid var(--border)}
tbody.account:first-of-type>tr:first-child>td{border-top:0}
tbody.account:nth-of-type(even)>tr.row>td{background:var(--zebra)}
tr.row{cursor:pointer}
/* The account is the unit, not the row: its name and plan live in cells that span
   the whole pool group, so a per-row hover left those cells untouched and a pool
   row looked like it belonged to nothing. Hover, focus and the open state all
   light the whole group instead. */
tbody.account:hover>tr.row>td,
tbody.account:focus-within>tr.row>td,
tbody.account:has(tr.row[aria-expanded="true"])>tr.row>td{background:var(--surface-2)}
tr.row:focus-visible{outline:2px solid var(--accent);outline-offset:-2px}
/* Fallback where :has() is unavailable: the open row still lights, its group's
   spanning cells simply keep the row colour they had. */
tr.row[aria-expanded="true"]>td{background:var(--surface-2)}

.name{display:flex;align-items:flex-start;gap:8px;min-width:0}
.name .chev,.name .dot{margin-top:5px}
/* The file name is the identity shared with the panel's auth file list; the
   operator's note sits under it, smaller, and only when there is one. */
.who{display:flex;flex-direction:column;min-width:0}
.note{color:var(--muted);font-size:12px;overflow-wrap:break-word}
.status-line{color:var(--bad);font-size:12px;overflow-wrap:break-word}
.actions{display:flex;flex-wrap:wrap;gap:6px;margin:7px 0 0 27px}
.page-actions{margin:8px 0 0}
.chev{flex:none;width:12px;color:var(--muted);font-size:12px;line-height:1;transition:transform .12s ease-out}
tr.row[aria-expanded="true"] .chev{transform:rotate(90deg);color:var(--accent)}
/* break-word, not anywhere: a name wraps only when it cannot fit, and never
   mid-word when a line is merely tight. */
.label{font-weight:500;overflow-wrap:break-word}
td.account{min-width:250px}
.dot{flex:none;width:7px;height:7px;border-radius:50%;background:var(--ok);box-shadow:0 0 0 3px var(--ok-bg)}
.dot.error{background:var(--bad);box-shadow:0 0 0 3px var(--bad-bg)}
.dot.disabled{background:var(--muted);box-shadow:none}
.plan{font-weight:500;white-space:nowrap}
.pair{white-space:nowrap}
.sep,.cap{color:var(--muted);font-weight:400}
.strong{font-weight:600}
/* The rail sits on the figure's own line. Stacking the percentage above a rail
   gave the cell two lines where every other cell had one, and the row then read
   as vertically misaligned even though each cell was centred. */
.gauge{display:flex;align-items:center;justify-content:flex-end;gap:9px;min-width:132px}
.track{flex:1 1 auto;max-width:96px;height:4px;border-radius:999px;background:var(--track);overflow:hidden}
.fill{display:block;height:100%;border-radius:inherit;background:var(--accent)}
.fill.high{background:var(--warn)}
.fill.full{background:var(--bad)}
.pct.high{color:var(--warn)}
.pct.full{color:var(--bad)}
/* A pool row names which money it is; the plan row is the ceiling, the rest are
   grants, so the grants read one step quieter than the ceiling they sit under. */
.pool{font-weight:500}
.pool.trial,.pool.bonus,.pool.overage_credit{color:var(--text-2);font-weight:400}
.triple{white-space:nowrap;color:var(--text-2)}
.triple b{color:var(--text);font-weight:600}
.charged{color:var(--warn)}
.reset{white-space:nowrap}
/* Countdown and pool status stay on the figure's line. Any cell that grows to a
   second line makes the whole row read as misaligned, which is what the stacked
   percentage and the stacked countdown both did. */
.countdown,.pool-status{color:var(--muted);font-size:12px;white-space:nowrap}
.countdown::before,.pool-status::before{content:"·";margin:0 5px;color:var(--muted)}
.countdown.soon{color:var(--warn)}
.none{color:var(--muted)}
.flag{
  display:inline-block;margin-left:6px;padding:0 6px;border:1px solid var(--warn-line);
  border-radius:999px;background:var(--warn-bg);color:var(--warn);font-size:12px;font-weight:500;
}
tfoot td{border-top:1px solid var(--border-2);background:var(--surface-2);font-weight:600}
tfoot .sub{color:var(--muted);font-size:12px;font-weight:400}
tfoot .sub::before{content:"·";margin:0 5px}

tr.meta>td{padding:0 14px 14px;background:var(--surface-2);border-top:0}
tr.meta[hidden]{display:none}
.meta-body{display:grid;gap:11px;padding:11px 0 0;border-top:1px dashed var(--border-2)}
.facts{
  display:grid;grid-template-columns:repeat(auto-fit,minmax(min(100%,344px),1fr));
  gap:4px 26px;margin:0;
}
.fact{display:flex;gap:10px;margin:0;min-width:0;font-size:12px;line-height:1.55}
.fact dt{flex:none;width:11.5em;color:var(--muted)}
.fact dd{margin:0;min-width:0;color:var(--text);font-weight:500;overflow-wrap:anywhere}
.meta-updated{margin:0;color:var(--muted);font-size:12px}
.empty{
  margin:14px 0 0;max-width:620px;padding:16px;border:1px solid var(--border);
  border-radius:10px;background:var(--surface);color:var(--muted);
}

/* The eight-column table needs a 1240px viewport (measured 2026-09-25: at
   1180px it overflowed by 60px), and the panel frames this page narrower than
   that on a laptop, where it broke. Under this width each account becomes its
   own block and every value keeps the column head as its label. */
@media(max-width:1240px){
  main{padding:14px 12px 22px}
  /* The panel keeps its cluster at a 12px gutter on a narrow viewport, so the
     reserved corner grows as a share of the width rather than shrinking. */
  .page-head{padding-right:clamp(150px,44vw,196px)}
  .total,.total.wide{flex-basis:150px}
  thead{position:absolute;width:1px;height:1px;overflow:hidden;clip:rect(0 0 0 0);white-space:nowrap}
  table,tbody,tfoot,tr,td{display:block;width:auto}
  tbody.account,tfoot tr{padding:6px 14px 10px}
  tbody.account>tr>td,tfoot td{border-top:0;background:transparent}
  tbody.account:nth-of-type(even)>tr.row>td{background:transparent}
  tbody.account+tbody.account{border-top:1px solid var(--border)}
  tfoot tr{border-top:1px solid var(--border-2);background:var(--surface-2)}
  th,td{padding:3px 0}
  td[data-label]{display:flex;align-items:baseline;justify-content:space-between;gap:14px}
  td[data-label]::before{content:attr(data-label);flex:none;color:var(--muted);font-weight:400}
  td.account[data-label]::before{content:none}
  /* The account cell stacks: name, note, then its actions on their own line,
     so the name keeps the full card width. */
  td.account[data-label]{display:block;min-width:0}
  .actions{margin:8px 0 2px 27px}
  .gauge{min-width:120px}
  tr.meta>td{padding:0}
  .fact dt{width:10.5em}
}
`

// usageShellText holds the shell's own strings; the key prompt shares the
// rest with the sign-in page through shellCommonScript.
const usageShellText = `  var TEXT = {
    en: {
      title: 'Kiro Usage',
      loading: 'Loading Kiro usage…',
      keyPrompt: 'Enter the CLIProxyAPI management key to view Kiro usage.',
      loadFailed: 'Kiro usage could not be loaded: {message}',
      actionFailed: 'The credential could not be changed: {message}'
    },
    vi: {
      title: 'Hạn mức Kiro',
      loading: 'Đang tải hạn mức Kiro…',
      keyPrompt: 'Nhập management key của CLIProxyAPI để xem hạn mức Kiro.',
      loadFailed: 'Không tải được hạn mức Kiro: {message}',
      actionFailed: 'Không đổi được credential: {message}'
    },
    'zh-CN': {
      title: 'Kiro 用量',
      loading: '正在加载 Kiro 用量…',
      keyPrompt: '请输入 CLIProxyAPI 管理密钥以查看 Kiro 用量。',
      loadFailed: '无法加载 Kiro 用量：{message}',
      actionFailed: '无法更改凭证：{message}'
    }
  };
`

// usageShellLogic loads the fragment with the panel's management key, asks for
// the key when the panel did not keep one, and drives the row toggles,
// countdowns and account actions through event delegation on the fragment.
const usageShellLogic = `  var busy = false;

  function start() {
    var gate = document.getElementById('kiro-gate');
    var state = document.getElementById('kiro-state');
    var form = document.getElementById('kiro-key-form');
    var input = document.getElementById('kiro-key');
    var flash = document.getElementById('kiro-flash');
    var host = document.getElementById('kiro-usage');

    function showGate(message, askKey) {
      host.textContent = '';
      flash.hidden = true;
      gate.hidden = false;
      state.textContent = message;
      form.hidden = !askKey;
      if (askKey) {
        input.focus();
      }
    }

    function showFlash(message) {
      flash.textContent = message;
      flash.hidden = !message;
    }

    function failWith(template) {
      return function (error) {
        if (error && error.needKey) {
          showGate(error.rejected ? t('keyRejected') : t('keyPrompt'), true);
          return;
        }
        var message = template.replace('{message}', error && error.message ? error.message : String(error));
        if (host.firstChild) {
          showFlash(message);
        } else {
          showGate(message, false);
        }
      };
    }

    function setBusy(flag) {
      busy = flag;
      host.setAttribute('aria-busy', flag ? 'true' : 'false');
      var controls = host.querySelectorAll('button[data-refresh], button[data-credential]');
      for (var index = 0; index < controls.length; index += 1) {
        controls[index].disabled = flag;
      }
    }

    function toggle(row, force) {
      var id = row.getAttribute('data-target');
      var detail = document.getElementById(id);
      if (!detail) {
        return;
      }
      var open = typeof force === 'boolean' ? force : detail.hidden;
      // An account can own several credit rows; they share one detail, so every
      // row of that account reports the same state.
      var rows = host.querySelectorAll('tr.row[data-target="' + id + '"]');
      for (var index = 0; index < rows.length; index += 1) {
        rows[index].setAttribute('aria-expanded', open ? 'true' : 'false');
      }
      detail.hidden = !open;
    }

    function openDetails() {
      var open = [];
      var details = host.querySelectorAll('tr.meta[id]:not([hidden])');
      for (var index = 0; index < details.length; index += 1) {
        open.push(details[index].id);
      }
      return open;
    }

    function formatTimes() {
      var formatter = null;
      try {
        formatter = new Intl.DateTimeFormat(lang, { dateStyle: 'medium', timeStyle: 'short' });
      } catch (error) {
        formatter = null;
      }
      var stamps = host.querySelectorAll('time[datetime]');
      for (var index = 0; index < stamps.length; index += 1) {
        var node = stamps[index];
        var parsed = new Date(node.getAttribute('datetime'));
        if (isNaN(parsed.getTime())) {
          continue;
        }
        node.setAttribute('data-ms', String(parsed.getTime()));
        node.setAttribute('title', parsed.toISOString());
        node.textContent = formatter ? formatter.format(parsed) : parsed.toLocaleString();
      }
    }

    function spell(ms) {
      var total = Math.floor(ms / 1000);
      var days = Math.floor(total / 86400);
      var hours = Math.floor((total % 86400) / 3600);
      var minutes = Math.floor((total % 3600) / 60);
      if (days > 0) {
        return days + 'd ' + hours + 'h';
      }
      if (hours > 0) {
        return hours + 'h ' + minutes + 'm';
      }
      return Math.max(minutes, 1) + 'm';
    }

    function tick() {
      var view = host.querySelector('.kiro-view');
      if (!view) {
        return;
      }
      var inPattern = view.getAttribute('data-text-in') || 'in {duration}';
      var duePattern = view.getAttribute('data-text-due') || 'due now';
      var now = Date.now();
      var pending = host.querySelectorAll('time[data-countdown][data-ms]');
      for (var index = 0; index < pending.length; index += 1) {
        var node = pending[index];
        var holder = node.parentNode;
        if (!holder) {
          continue;
        }
        var badge = holder.querySelector('.countdown');
        if (!badge) {
          badge = document.createElement('span');
          holder.appendChild(badge);
        }
        var left = Number(node.getAttribute('data-ms')) - now;
        if (left <= 0) {
          badge.textContent = duePattern;
          badge.className = 'countdown';
          continue;
        }
        badge.textContent = inPattern.replace('{duration}', spell(left));
        badge.className = left < 3600000 ? 'countdown soon' : 'countdown';
      }
    }

    function load(refresh) {
      if (busy) {
        return;
      }
      var open = openDetails();
      if (!host.firstChild) {
        showGate(t('loading'), false);
      }
      setBusy(true);
      var query = new URLSearchParams({ lang: lang });
      if (refresh) {
        query.set('refresh', refresh);
      }
      request('usage/view?' + query.toString(), { method: 'GET' })
        .then(function (response) {
          return response.text();
        })
        .then(function (fragment) {
          // The fragment is rendered by html/template on the authenticated
          // route; it carries no script, and this page's CSP would not run one.
          host.innerHTML = fragment;
          gate.hidden = true;
          showFlash('');
          formatTimes();
          for (var index = 0; index < open.length; index += 1) {
            var row = host.querySelector('tr.row[data-target="' + open[index] + '"]');
            if (row) {
              toggle(row, true);
            }
          }
          tick();
        })
        .catch(failWith(t('loadFailed')))
        .then(function () {
          setBusy(false);
        });
    }

    function setCredential(file, disabled) {
      setBusy(true);
      request('usage/credential', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ file: file, disabled: disabled })
      })
        .then(function () {
          setBusy(false);
          load('');
        })
        .catch(function (error) {
          setBusy(false);
          failWith(t('actionFailed'))(error);
        });
    }

    host.addEventListener('click', function (event) {
      var target = event.target;
      if (!target || !target.closest) {
        return;
      }
      var control = target.closest('button[data-refresh], button[data-credential]');
      if (control) {
        if (busy) {
          return;
        }
        if (control.hasAttribute('data-refresh')) {
          load(control.getAttribute('data-refresh'));
          return;
        }
        var question = control.getAttribute('data-confirm');
        if (question && !window.confirm(question)) {
          return;
        }
        setCredential(control.getAttribute('data-credential'), control.getAttribute('data-disabled') === 'true');
        return;
      }
      if (target.closest('a,button,input,select,textarea')) {
        return;
      }
      var row = target.closest('tr.row[data-target]');
      if (!row) {
        return;
      }
      // Dragging across the row to select a figure is not a click on the row.
      var selection = window.getSelection && window.getSelection();
      if (selection && selection.type === 'Range' && String(selection).length > 0) {
        return;
      }
      toggle(row);
    });

    host.addEventListener('keydown', function (event) {
      var row = event.target;
      if (!row || !row.matches || !row.matches('tr.row[data-target]')) {
        return;
      }
      if (event.key === 'Enter' || event.key === ' ' || event.key === 'Spacebar') {
        event.preventDefault();
        toggle(row);
      }
    });

    form.addEventListener('submit', function (event) {
      event.preventDefault();
      memoryKey = input.value.trim();
      input.value = '';
      if (memoryKey) {
        load('');
      }
    });

    var panel = panelRoot();
    if (panel && window.MutationObserver) {
      new MutationObserver(function () {
        root.setAttribute('data-theme', pickTheme());
        var next = pickLang();
        if (next !== lang) {
          lang = next;
          root.setAttribute('lang', lang);
          localise();
          load('');
        }
      }).observe(panel, { attributes: true, attributeFilter: ['data-theme', 'lang'] });
    }

    localise();
    load('');
    window.setInterval(tick, 30000);
  }

  whenReady(start);
`

// usageViewHTML is the authenticated fragment the shell injects. It holds no
// script and no inline handler; its controls are plain buttons the shell
// script handles.
const usageViewHTML = `<div class="kiro-view" lang="{{.Options.Lang}}" data-text-in="{{text "countdown_in"}}" data-text-due="{{text "countdown_due"}}">
{{emailOff}}
  <header class="page-head">
    <h1>{{text "title"}}</h1>
    <p class="intro">{{text "intro"}}</p>
    {{if not .Empty}}<p class="page-actions"><button type="button" class="act" data-refresh="all">{{text "action_refresh_all"}}</button></p>{{end}}
  </header>
  {{if .Empty}}<div class="empty" role="status">{{text "empty"}}</div>{{else}}
  {{$totals := .Totals}}
  <dl class="totals">
    <div class="total wide">
      <dt>{{text "total_used"}}</dt>
      <dd>{{formatNumber $totals.Used}} <small>{{text "bucket_of"}} {{formatNumber $totals.Limit}}{{if $totals.Unit}} {{lower $totals.Unit}}{{end}}{{if $totals.MixedUnits}} ({{text "total_mixed_units"}}){{end}}</small></dd>
    </div>
    <div class="total">
      <dt>{{text "total_remaining"}}</dt>
      <dd>{{formatNumber $totals.Remaining}}</dd>
    </div>
    <div class="total">
      <dt>{{text "total_share"}}</dt>
      <dd class="pct {{percentClass $totals.Percent}}">{{printf "%.1f%%" $totals.Percent}}</dd>
    </div>
    <div class="total">
      <dt>{{text "total_charged"}}</dt>
      <dd{{if gt $totals.OverageCharges 0.0}} class="charged"{{end}}>{{formatNumber $totals.OverageCharges}}{{if $totals.Currency}} <small>{{$totals.Currency}}</small>{{end}}</dd>
    </div>
    {{if gt $totals.TrialLimit 0.0}}
    <div class="total">
      <dt>{{text "total_trial"}}</dt>
      <dd>{{formatNumber $totals.TrialUsed}} <small>{{text "bucket_of"}} {{formatNumber $totals.TrialLimit}}</small></dd>
    </div>
    {{end}}
    {{if gt $totals.BonusTotal 0.0}}
    <div class="total">
      <dt>{{text "total_bonus"}}</dt>
      <dd>{{formatNumber $totals.BonusTotal}}</dd>
    </div>
    {{end}}
    {{if gt $totals.OverageCredit 0.0}}
    <div class="total">
      <dt>{{text "total_overage_credit"}}</dt>
      <dd>{{formatNumber $totals.OverageCredit}}</dd>
    </div>
    {{end}}
    <div class="total">
      <dt>{{text "stat_accounts"}}</dt>
      <dd>{{.Summary.Accounts}} <small>{{text "stat_active"}} {{.Summary.Reporting}}{{if .Summary.Attention}} · {{text "stat_attention"}} {{.Summary.Attention}}{{end}}</small></dd>
    </div>
  </dl>
  <div class="table-wrap">
    <table aria-label="{{text "table_caption"}}">
      <thead>
        <tr>
          <th scope="col">{{text "col_account"}}</th>
          <th scope="col">{{text "col_plan"}}</th>
          <th scope="col">{{text "col_quota"}}</th>
          <th scope="col" class="num">{{text "col_used"}}</th>
          <th scope="col" class="num">{{text "col_percent"}}</th>
          <th scope="col" class="num">{{text "col_remaining"}}</th>
          <th scope="col" class="num">{{text "col_overage"}}<small>{{text "col_overage_sub"}}</small></th>
          <th scope="col">{{text "col_reset"}}</th>
        </tr>
      </thead>
      {{range $index, $account := .Accounts}}
      {{$metaID := printf "kiro-detail-%d" $index}}
      {{$rows := len $account.Buckets}}{{if eq $rows 0}}{{$rows = 1}}{{end}}
      <tbody class="account">
        {{range $row, $bucket := $account.Buckets}}
        <tr class="row" tabindex="0" role="button" aria-expanded="false" aria-controls="{{$metaID}}" data-target="{{$metaID}}">
          {{if eq $row 0}}
          <td class="account" rowspan="{{$rows}}" data-label="{{text "col_account"}}">
            {{template "account-cell" $account}}
          </td>
          <td rowspan="{{$rows}}" data-label="{{text "col_plan"}}"><span class="plan">{{if $account.Plan}}{{$account.Plan}}{{else}}{{text "plan_unknown"}}{{end}}</span></td>
          {{end}}
          <td data-label="{{text "col_quota"}}"><span class="pool {{$bucket.Kind}}">{{bucketName $bucket}}</span></td>
          {{if $bucket.HasShare}}
          <td class="num" data-label="{{text "col_used"}}"><span class="pair"><span class="strong">{{formatNumber $bucket.Used}}</span><span class="sep"> / {{formatNumber $bucket.Limit}}</span>{{if $bucket.Unit}}<span class="cap"> {{lower $bucket.Unit}}</span>{{end}}</span></td>
          <td class="num" data-label="{{text "col_percent"}}">
            <span class="gauge">
              <span class="track" role="meter" aria-label="{{bucketName $bucket}}" aria-valuemin="0" aria-valuemax="{{formatNumber $bucket.Limit}}" aria-valuenow="{{formatNumber $bucket.Used}}"><span class="fill {{percentClass $bucket.Percent}}" style="width:{{printf "%.2f" $bucket.Percent}}%"></span></span>
              <span class="strong pct {{percentClass $bucket.Percent}}">{{printf "%.1f%%" $bucket.Percent}}</span>
            </span>
          </td>
          <td class="num" data-label="{{text "col_remaining"}}"><span class="strong">{{formatNumber $bucket.Remaining}}</span>{{if gt $bucket.Overage 0.0}}<span class="flag">+{{formatNumber $bucket.Overage}}</span>{{end}}</td>
          {{else}}
          <td class="num" data-label="{{text "col_used"}}"><span class="none">{{grantsCount $bucket.Grants}}</span></td>
          <td class="num" data-label="{{text "col_percent"}}"><span class="none">&mdash;</span></td>
          <td class="num" data-label="{{text "col_remaining"}}">{{if $bucket.AmountKnown}}<span class="strong">{{formatNumber $bucket.Remaining}}</span>{{if $bucket.Unit}}<span class="cap"> {{lower $bucket.Unit}}</span>{{end}}{{else}}<span class="none">{{text "amount_unknown"}}</span>{{end}}</td>
          {{end}}
          {{if eq $bucket.Kind "plan"}}
          <td class="num" data-label="{{text "col_overage"}}"><span class="triple">{{if gt $bucket.OverageCap 0.0}}{{formatNumber $bucket.OverageCap}}{{else}}<span class="none">&mdash;</span>{{end}}<span class="sep"> · </span>{{if gt $bucket.OverageRate 0.0}}{{formatNumber $bucket.OverageRate}}{{if $bucket.Currency}} {{$bucket.Currency}}{{end}}{{else}}<span class="none">&mdash;</span>{{end}}<span class="sep"> · </span><b{{if gt $bucket.OverageCharges 0.0}} class="charged"{{end}}>{{formatNumber $bucket.OverageCharges}}</b></span></td>
          {{else}}
          <td class="num" data-label="{{text "col_overage"}}"><span class="none">&mdash;</span></td>
          {{end}}
          <td data-label="{{text "col_reset"}}">
            {{if $bucket.Reset}}<span class="reset"><time datetime="{{$bucket.Reset}}" data-countdown>{{$bucket.Reset}}</time></span>{{else}}{{if $bucket.Expiry}}<span class="reset"><time datetime="{{$bucket.Expiry}}" data-countdown>{{$bucket.Expiry}}</time></span>{{else}}<span class="none">&mdash;</span>{{end}}{{end}}
            {{if $bucket.StatusRaw}}<span class="pool-status">{{trialLabel $bucket.StatusRaw}}</span>{{end}}
          </td>
        </tr>
        {{end}}
        {{if eq (len $account.Buckets) 0}}
        <tr class="row" tabindex="0" role="button" aria-expanded="false" aria-controls="{{$metaID}}" data-target="{{$metaID}}">
          <td class="account" data-label="{{text "col_account"}}">
            {{template "account-cell" $account}}
          </td>
          <td data-label="{{text "col_plan"}}"><span class="plan">{{if $account.Plan}}{{$account.Plan}}{{else}}{{text "plan_unknown"}}{{end}}</span></td>
          <td data-label="{{text "col_quota"}}"><span class="none">&mdash;</span></td>
          <td class="num" data-label="{{text "col_used"}}"><span class="none">&mdash;</span></td>
          <td class="num" data-label="{{text "col_percent"}}"><span class="none">&mdash;</span></td>
          <td class="num" data-label="{{text "col_remaining"}}"><span class="none">&mdash;</span></td>
          <td class="num" data-label="{{text "col_overage"}}"><span class="none">&mdash;</span></td>
          <td data-label="{{text "col_reset"}}"><span class="none">&mdash;</span></td>
        </tr>
        {{end}}
        <tr class="meta" id="{{$metaID}}" hidden>
          <td colspan="8">
            <div class="meta-body">
              {{$message := errorMessage $account}}
              {{if $message}}<p class="notice" role="status">{{$message}}</p>{{end}}
              <dl class="facts">
                <div class="fact"><dt>{{text "label_state"}}</dt><dd>{{stateLabel $account.StateKey}}</dd></div>
                {{if isAddress $account.Account}}<div class="fact"><dt>{{text "label_account"}}</dt><dd>{{$account.Account}}</dd></div>{{end}}
                {{if $account.PlanType}}<div class="fact"><dt>{{text "col_plan"}}</dt><dd>{{$account.PlanType}}</dd></div>{{end}}
                {{if $account.AuthMethod}}<div class="fact"><dt>{{text "label_auth_method"}}</dt><dd>{{authMethodLabel $account.AuthMethod}}</dd></div>{{end}}
                {{if $account.Region}}<div class="fact"><dt>{{text "label_region"}}</dt><dd>{{$account.Region}}</dd></div>{{end}}
                {{if $account.Directory}}<div class="fact"><dt>{{text "label_directory"}}</dt><dd>{{$account.Directory}}</dd></div>{{else}}{{if eq $account.AuthMethod "builder-id"}}<div class="fact"><dt>{{text "label_directory"}}</dt><dd>{{text "directory_builder_id"}}</dd></div>{{end}}{{end}}
                {{if $account.AWSAccountID}}<div class="fact"><dt>{{text "label_aws_account"}}</dt><dd>{{$account.AWSAccountID}}</dd></div>{{end}}
                {{if $account.ProfileName}}<div class="fact"><dt>{{text "label_profile"}}</dt><dd>{{$account.ProfileName}}</dd></div>{{end}}
                {{if $account.FileName}}<div class="fact"><dt>{{text "label_file"}}</dt><dd>{{$account.FileName}}</dd></div>{{end}}
                {{if $account.TokenExpiresAt}}<div class="fact"><dt>{{text "label_token_expires"}}</dt><dd><time datetime="{{$account.TokenExpiresAt}}" data-countdown>{{$account.TokenExpiresAt}}</time></dd></div>{{end}}
                {{if $account.LastRefresh}}<div class="fact"><dt>{{text "label_last_refresh"}}</dt><dd><time datetime="{{$account.LastRefresh}}">{{$account.LastRefresh}}</time></dd></div>{{end}}
                {{if $account.OverageStatus}}<div class="fact"><dt>{{text "label_overage_status"}}</dt><dd>{{$account.OverageStatus}}</dd></div>{{end}}
                {{if $account.HasOverageLimit}}<div class="fact"><dt>{{text "label_overage_limit"}}</dt><dd>{{formatNumber $account.OverageLimit}}</dd></div>{{end}}
                {{if $account.OverageCapability}}<div class="fact"><dt>{{text "label_overage_allowed"}}</dt><dd>{{capabilityLabel $account.OverageCapability}}</dd></div>{{end}}
                {{if $account.UpgradeCapability}}<div class="fact"><dt>{{text "label_upgrade"}}</dt><dd>{{capabilityLabel $account.UpgradeCapability}}</dd></div>{{end}}
                {{if $account.ManagementTarget}}<div class="fact"><dt>{{text "label_manage"}}</dt><dd>{{capabilityLabel $account.ManagementTarget}}</dd></div>{{end}}
                {{if $account.HasDaysUntilReset}}<div class="fact"><dt>{{text "label_days_reset"}}</dt><dd>{{formatNumber $account.DaysUntilReset}}</dd></div>{{end}}
                {{range $bucket := $account.Buckets}}
                {{if eq $bucket.Kind "plan"}}
                {{if gt $bucket.OverageCap 0.0}}<div class="fact"><dt>{{text "label_overage_cap"}}</dt><dd>{{formatNumber $bucket.OverageCap}}{{if $bucket.Unit}} {{lower $bucket.Unit}}{{end}}</dd></div>{{end}}
                {{if gt $bucket.OverageRate 0.0}}<div class="fact"><dt>{{text "label_overage_rate"}}</dt><dd>{{formatNumber $bucket.OverageRate}}{{if $bucket.Currency}} {{$bucket.Currency}}{{end}} {{perUnit $bucket.Unit}}</dd></div>{{end}}
                <div class="fact"><dt>{{text "label_overage_charges"}}</dt><dd>{{formatNumber $bucket.OverageCharges}}{{if $bucket.Currency}} {{$bucket.Currency}}{{end}}</dd></div>
                {{if gt $bucket.FreeTrialLimit 0.0}}<div class="fact"><dt>{{text "label_free_trial"}}</dt><dd>{{formatNumber $bucket.FreeTrialUsed}} {{text "bucket_of"}} {{formatNumber $bucket.FreeTrialLimit}}{{if $bucket.FreeTrialStatus}} · {{trialLabel $bucket.FreeTrialStatus}}{{end}}</dd></div>{{end}}
                {{if $bucket.FreeTrialExpiry}}<div class="fact"><dt>{{text "label_trial_expiry"}}</dt><dd><time datetime="{{$bucket.FreeTrialExpiry}}">{{$bucket.FreeTrialExpiry}}</time></dd></div>{{end}}
                {{if gt $bucket.BonusTotal 0.0}}<div class="fact"><dt>{{text "label_bonus"}}</dt><dd>{{formatNumber $bucket.BonusTotal}}</dd></div>{{end}}
                {{if gt $bucket.OverageCredit 0.0}}<div class="fact"><dt>{{text "label_overage_credit"}}</dt><dd>{{formatNumber $bucket.OverageCredit}}</dd></div>{{end}}
                {{if $bucket.Unit}}<div class="fact"><dt>{{text "label_unit"}}</dt><dd>{{$bucket.Unit}}</dd></div>{{end}}
                {{if $bucket.Currency}}<div class="fact"><dt>{{text "label_currency"}}</dt><dd>{{$bucket.Currency}}</dd></div>{{end}}
                {{else}}
                <div class="fact"><dt>{{bucketName $bucket}}</dt><dd>{{if $bucket.HasShare}}{{formatNumber $bucket.Used}} {{text "bucket_of"}} {{formatNumber $bucket.Limit}}{{else}}{{if $bucket.AmountKnown}}{{formatNumber $bucket.Limit}}{{else}}{{text "amount_unknown"}}{{end}} · {{grantsCount $bucket.Grants}}{{end}}{{if $bucket.StatusRaw}} · {{trialLabel $bucket.StatusRaw}}{{end}}</dd></div>
                {{if $bucket.Expiry}}<div class="fact"><dt>{{text "label_expiry"}}</dt><dd><time datetime="{{$bucket.Expiry}}">{{$bucket.Expiry}}</time></dd></div>{{end}}
                {{end}}
                {{end}}
                {{if $account.StatusMessage}}<div class="fact"><dt>{{text "label_status"}}</dt><dd>{{$account.StatusMessage}}</dd></div>{{end}}
              </dl>
              {{if $account.UpdatedAt}}<p class="meta-updated">{{text "updated_prefix"}} <time datetime="{{$account.UpdatedAt}}">{{$account.UpdatedAt}}</time></p>{{end}}
            </div>
          </td>
        </tr>
      </tbody>
      {{end}}
      {{if $totals.HasFigures}}
      <tfoot>
        <tr>
          <td data-label="{{text "total_row"}}">{{text "total_row"}}<span class="sub">{{accountCount $totals.Accounts}}</span></td>
          <td data-label="{{text "col_plan"}}"><span class="none">&mdash;</span></td>
          <td data-label="{{text "col_quota"}}">{{text "quota_plan"}}</td>
          <td class="num" data-label="{{text "col_used"}}"><span class="pair">{{formatNumber $totals.Used}}<span class="sep"> / {{formatNumber $totals.Limit}}</span></span></td>
          <td class="num" data-label="{{text "col_percent"}}">
            <span class="gauge">
              <span class="track" role="meter" aria-label="{{text "total_share"}}" aria-valuemin="0" aria-valuemax="{{formatNumber $totals.Limit}}" aria-valuenow="{{formatNumber $totals.Used}}"><span class="fill {{percentClass $totals.Percent}}" style="width:{{printf "%.2f" $totals.Percent}}%"></span></span>
              <span class="pct {{percentClass $totals.Percent}}">{{printf "%.1f%%" $totals.Percent}}</span>
            </span>
          </td>
          <td class="num" data-label="{{text "col_remaining"}}">{{formatNumber $totals.Remaining}}</td>
          <td class="num" data-label="{{text "col_overage"}}"><span class="triple">{{formatNumber $totals.OverageCap}}<span class="sep"> · </span><span class="none">&mdash;</span><span class="sep"> · </span><b{{if gt $totals.OverageCharges 0.0}} class="charged"{{end}}>{{formatNumber $totals.OverageCharges}}</b></span></td>
          <td data-label="{{text "col_reset"}}"><span class="none">&mdash;</span></td>
        </tr>
      </tfoot>
      {{end}}
    </table>
  </div>{{end}}
{{emailOn}}
</div>
{{define "account-cell"}}{{$account := .}}
<span class="name">
  <span class="chev" aria-hidden="true">&#9656;</span>
  <span class="dot {{$account.StateClass}}" aria-hidden="true"></span>
  <span class="who">
    <span class="label">{{displayName $account}}</span>
    {{if $account.Note}}<span class="note">{{$account.Note}}</span>{{end}}
    {{if and (needsAttention $account) $account.StatusMessage}}<span class="status-line">{{$account.StatusMessage}}</span>{{end}}
  </span>
  <span class="sr">{{text "label_state"}}: {{stateLabel $account.StateKey}}. {{text "row_hint"}}</span>
</span>
{{if $account.FileName}}<span class="actions">
  {{if needsAttention $account}}<a class="act primary" href="/management.html#/oauth" target="_top">{{text "action_relogin"}}</a>{{end}}
  {{if ne $account.StateKey "disabled"}}<button type="button" class="act" data-refresh="{{$account.FileName}}">{{text "action_refresh"}}</button>{{end}}
  {{if eq $account.StateKey "disabled"}}<button type="button" class="act" data-credential="{{$account.FileName}}" data-disabled="false">{{text "action_enable"}}</button>{{else}}<button type="button" class="act" data-credential="{{$account.FileName}}" data-disabled="true" data-confirm="{{text "confirm_disable"}}">{{text "action_disable"}}</button>{{end}}
</span>{{end}}
{{end}}`
