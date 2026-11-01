package main

import "html/template"

// usagePageTemplate is parsed once with placeholder helpers; renderUsagePage
// clones it per response and rebinds the helpers to the requested language.
var usagePageTemplate = template.Must(
	template.New("kiro-usage").Funcs(usagePageFuncs(usagePageTextPacks[usageLangEN])).Parse(usageViewHTML),
)

// The usage page is split in two so the resource route serves nothing but
// static bytes. CPA serves resource routes without the management key, so the
// shell holds only the layout, its script and the Add account dialog; the
// account data comes from usageViewHTML on the authenticated view route. The
// shell's CSP admits exactly its own script by hash, so the fragment it
// injects can never run one.
var (
	usageShell       = newShellPage("Kiro Usage", panelBaseCSS+signInCSS+usageShellCSS, usageShellScript, fillShellSlots(usageShellBody))
	usageShellPage   = usageShell.Body
	usageShellPolicy = usageShell.Policy
	usageShellCSP    = usageShell.Header
	usageShellScript = shellScript(usageShellText, usageShellLogic)
)

// The toolbar lives in the shell, not the fragment, so the filter, search and
// sort a reader chose survive every reload of the data; the script moves it
// into the fragment's .tools-slot, between the fleet cards and the table.
const usageShellBody = `<main id="kiro-page" class="page">
  <header class="page-head">
    <div class="head-copy">
      <h1 class="page-title" data-t="title">Kiro Usage</h1>
      <p class="intro" data-t="intro"></p>
    </div>
    <div id="kiro-actions" class="head-actions" hidden>
      <button type="button" class="pill primary" data-signin="">{{icon:plus}}<span data-t="addAccount">Add account</span></button>
      <button type="button" class="pill secondary" data-refresh="all"><span class="spinner" aria-hidden="true"></span>{{icon:refresh}}<span data-t="refreshAll">Reload all quotas</span></button>
    </div>
  </header>
  <section id="kiro-gate" class="gate">
    <p id="kiro-state" class="gate-text" role="status" aria-live="polite"></p>
    <noscript><p class="alert">This page needs JavaScript.</p></noscript>
    <form id="kiro-key-form" class="keyform" hidden>
      <label for="kiro-key" data-t="keyLabel">Management key</label>
      <span class="keyrow"><span class="secret"><input id="kiro-key" type="password" autocomplete="off" spellcheck="false"><button type="button" class="reveal" data-reveal="kiro-key" aria-pressed="false">{{icon:eye}}</button></span><button class="pill primary" type="submit" data-t="keySubmit">Continue</button></span>
      <p class="field-hint" data-t="keyHint"></p>
    </form>
  </section>
  <div id="kiro-skeleton" class="skeleton" aria-hidden="true" hidden>
    <div class="sk-cards"><span></span><span></span><span></span><span></span></div>
    <div class="sk-rows"><span></span><span></span><span></span></div>
  </div>
  <p id="kiro-flash" class="alert flash" role="alert" hidden></p>
  <div id="kiro-tools" class="tools" hidden>
    <div class="filters" role="group">
      <button type="button" class="filter" data-filter="all" aria-pressed="true"><span data-t="f_all">All</span><span class="count" data-count="all"></span></button>
      <button type="button" class="filter" data-filter="active" aria-pressed="false"><span data-t="f_active">Active</span><span class="count" data-count="active"></span></button>
      <button type="button" class="filter" data-filter="attention" aria-pressed="false"><span data-t="f_attention">Needs attention</span><span class="count" data-count="attention"></span></button>
      <button type="button" class="filter" data-filter="disabled" aria-pressed="false"><span data-t="f_disabled">Disabled</span><span class="count" data-count="disabled"></span></button>
    </div>
    <div class="finders">
      <span class="search">{{icon:search}}<input id="kiro-search" type="search" autocomplete="off" spellcheck="false"><button type="button" class="icon-btn clear" id="kiro-clear" hidden>{{icon:x}}</button></span>
      <label class="sort"><span data-t="sortLabel">Sort</span><span class="select"><select id="kiro-sort">
        <option value="default" data-t="s_default">Connection order</option>
        <option value="share" data-t="s_share">Share used, highest first</option>
        <option value="remaining" data-t="s_remaining">Remaining, lowest first</option>
        <option value="reset" data-t="s_reset">Resets soonest</option>
        <option value="name" data-t="s_name">Name</option>
      </select>{{icon:chevron}}</span></label>
    </div>
    <p class="shown"><span id="kiro-shown" role="status"></span> <button type="button" class="link" id="kiro-reset-filters" hidden data-t="clearFilters">Clear filters</button></p>
  </div>
  <div id="kiro-usage" aria-busy="false"></div>
</main>
<div id="kiro-modal" class="overlay" hidden>
  <section class="dialog" role="dialog" aria-modal="true" aria-labelledby="kiro-modal-title">
    <header class="head">
      <span class="glyph"><img src="{{logo}}" alt="" width="22" height="22"></span>
      <span class="head-text"><h2 id="kiro-modal-title" class="dialog-title" data-t="addTitle">Add Kiro account</h2><span class="caption">Plugin</span></span>
      <button type="button" class="icon-btn close" id="kiro-close">{{icon:x}}</button>
    </header>
    <div class="body">
      {{signin-form}}
      <button type="button" id="kiro-finish" class="pill primary wide" data-t="finish" hidden>Done</button>
    </div>
    ` + signInStatusBar + `
  </section>
</div>
`

// The page is a comparison table with fleet cards above it and a totals row
// below it, so the same figure of every account reads down one column and the
// whole fleet reads at a glance.
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
const usageShellCSS = `.page{width:min(1560px,100%);margin:0 auto;padding:22px 24px 32px}
/* The panel's floating button cluster sits over this page's top right corner
   (fixed, 24px inset, 36-38px controls). The title row keeps that corner clear
   instead of putting text or a button under it. */
.page-head{display:flex;flex-wrap:wrap;align-items:flex-start;justify-content:space-between;gap:12px 20px;padding-right:clamp(150px,17vw,208px)}
.head-copy{min-width:0}
.page-title{margin:0;font-size:22px;font-weight:650;line-height:1.25;letter-spacing:-.02em}
.intro{margin:4px 0 0;color:var(--text-secondary);font-size:13px}
.head-actions{display:flex;flex-wrap:wrap;gap:8px}
.head-actions .busy .i{display:none}

/* Shell states: loading, the key prompt and failures, shown before the
   authenticated fragment arrives or in place of it. */
.gate{margin:18px 0 0}
.gate-text{margin:0;color:var(--text-secondary);font-size:13px}
.gate-text:empty{display:none}
.keyform{
  display:grid;gap:8px;max-width:540px;margin:12px 0 0;padding:18px;
  border:1px solid var(--border-color);border-radius:14px;background:var(--bg-primary);box-shadow:var(--shadow);
}
.keyform label{font-size:13px;font-weight:600}
.keyrow{display:flex;flex-wrap:wrap;gap:8px}
.keyrow .secret{flex:1 1 220px;min-width:0}
.flash{margin:16px 0 0}
.skeleton{margin:18px 0 0}
.sk-cards{display:grid;grid-template-columns:repeat(auto-fit,minmax(min(100%,210px),1fr));gap:12px}
.sk-cards span,.sk-rows span{display:block;border-radius:14px;background:var(--bg-tertiary);animation:pulse 1.6s ease-in-out infinite}
.sk-cards span{height:108px}
.sk-rows{display:grid;gap:8px;margin:62px 0 0}
.sk-rows span{height:52px;border-radius:10px}

/* Fleet cards: the same figures as the totals row, read before the detail. */
.totals{display:grid;grid-template-columns:repeat(auto-fit,minmax(min(100%,210px),1fr));gap:12px;margin:18px 0 0}
.total{
  display:flex;flex-direction:column;gap:6px;min-width:0;margin:0;padding:14px 16px;
  border:1px solid var(--border-color);border-radius:14px;background:var(--bg-primary);box-shadow:var(--shadow);
}
.total dt{display:flex;align-items:center;justify-content:space-between;gap:8px;margin:0;color:var(--text-secondary);font-size:12px;font-weight:500}
.total dd{margin:0;min-width:0}
.figure{color:var(--text-secondary);font-size:12px;font-variant-numeric:tabular-nums}
.big{color:var(--text-primary);font-size:24px;font-weight:650;line-height:1.2;letter-spacing:-.02em}
.figure.charged .big{color:var(--failure-text)}
.total .track{display:block;max-width:none;height:6px}
.extras{display:flex;flex-wrap:wrap;gap:2px 12px;color:var(--text-secondary);font-size:12px;font-variant-numeric:tabular-nums}
.extras b{color:var(--text-primary);font-weight:600}
.extras .attention b{color:var(--failure-text)}
.badge{
  display:inline-flex;align-items:center;padding:1px 8px;border:1px solid var(--border-color);border-radius:999px;
  background:var(--bg-secondary);color:var(--text-primary);font-size:12px;font-weight:600;font-variant-numeric:tabular-nums;
}
.badge.high{border-color:color-mix(in srgb,var(--amber) 40%,transparent);background:color-mix(in srgb,var(--amber) 10%,transparent);color:var(--amber-text)}
.badge.full{border-color:color-mix(in srgb,var(--viz-failure) 40%,transparent);background:color-mix(in srgb,var(--viz-failure) 10%,transparent);color:var(--failure-text)}

/* Toolbar, drawn as the panel's quota page draws its own. */
.tools{display:flex;flex-wrap:wrap;align-items:center;justify-content:space-between;gap:10px 16px;margin:18px 0 0}
.filters{display:inline-flex;flex-wrap:wrap;gap:2px;max-width:100%;padding:3px;border:1px solid var(--border-color);border-radius:12px;background:var(--bg-primary)}
.filter{
  display:inline-flex;align-items:center;gap:7px;height:30px;padding:0 10px;border:0;border-radius:9px;
  background:transparent;color:var(--text-secondary);font:inherit;font-size:13px;font-weight:500;white-space:nowrap;cursor:pointer;
}
.filter[aria-pressed="true"]{background:var(--bg-secondary);color:var(--text-primary);font-weight:600;box-shadow:var(--shadow)}
.filter:focus-visible,.link:focus-visible{outline:2px solid var(--primary-color);outline-offset:1px}
.count{min-width:20px;padding:0 6px;border-radius:999px;background:var(--bg-tertiary);font-size:11px;font-weight:600;text-align:center;font-variant-numeric:tabular-nums}
.finders{display:flex;flex-wrap:wrap;justify-content:flex-end;gap:8px;flex:1 1 380px}
.search{position:relative;display:flex;align-items:center;flex:0 1 320px;min-width:200px}
.search>.i{position:absolute;left:11px;color:var(--text-tertiary);pointer-events:none}
.search input{min-height:36px;padding:0 40px 0 34px;border-radius:10px;font-size:13px}
.search input::-webkit-search-cancel-button{-webkit-appearance:none;appearance:none}
.search .clear{position:absolute;right:3px}
.sort{display:flex;align-items:center;gap:8px;color:var(--text-secondary);font-size:13px;white-space:nowrap}
.select{position:relative;display:flex;align-items:center}
.select select{min-height:36px;padding:0 34px 0 12px;border-radius:10px;font-size:13px;-webkit-appearance:none;appearance:none;cursor:pointer}
.select .i{position:absolute;right:11px;color:var(--text-secondary);pointer-events:none}
.shown{flex:1 1 100%;margin:0;color:var(--text-secondary);font-size:12px}
.link{padding:0;border:0;background:none;color:var(--text-primary);font:inherit;font-weight:600;text-decoration:underline;text-underline-offset:2px;cursor:pointer}

/* A table narrower than its columns scrolls sideways instead of squeezing the
   text columns; the squeeze is what broke "Kiro Free" into one letter a line. */
.table-wrap{margin:8px 0 0;overflow-x:auto;border:1px solid var(--border-color);border-radius:14px;background:var(--bg-primary);box-shadow:var(--shadow)}
table{width:100%;border-collapse:separate;border-spacing:0;font-size:13px;font-variant-numeric:tabular-nums}
/* Vertical centring is the fix for the misaligned rows: figures of different
   line counts sat on the top edge of their cell before. */
th,td{padding:11px 12px;text-align:left;vertical-align:middle}
th:first-child,td:first-child{padding-left:16px}
th:last-child,td:last-child{padding-right:16px}
/* A cell spanning an account's pool rows aligns with the first of them instead of
   floating in the middle of the group. */
td[rowspan]{vertical-align:top}
/* Column heads are sentence case at the body's own letter spacing, as the
   panel's tables write them. Uppercase with tracking is what made them read
   as cramped and shouted. */
thead th{background:var(--bg-tertiary);color:var(--text-secondary);font-size:12px;font-weight:500;line-height:1.35;white-space:nowrap}
thead th small{display:block;color:var(--text-secondary);font-size:11px;font-weight:400;opacity:.8}
th.num,td.num{text-align:right}
/* Every group draws its own top rule, so the first group still shown after a
   filter or a sort is separated from the head like any other. */
tbody.account>tr>td{border-top:1px solid var(--border-color)}
tr.row{cursor:pointer}
/* The account is the unit, not the row: its name and plan live in cells that span
   the whole pool group, so a per-row hover left those cells untouched and a pool
   row looked like it belonged to nothing. Hover, focus and the open state all
   light the whole group instead. */
tbody.account:hover>tr.row>td,
tbody.account:focus-within>tr.row>td,
tbody.account:has(tr.row[aria-expanded="true"])>tr.row>td{background:color-mix(in srgb,var(--bg-tertiary) 60%,transparent)}
tr.row:focus-visible{outline:2px solid var(--primary-color);outline-offset:-2px}
/* Fallback where :has() is unavailable: the open row still lights, its group's
   spanning cells simply keep the row colour they had. */
tr.row[aria-expanded="true"]>td{background:color-mix(in srgb,var(--bg-tertiary) 60%,transparent)}

.name{display:flex;align-items:flex-start;gap:8px;min-width:0}
.name .chev{margin-top:1px}
.name .dot{margin-top:7px}
/* The file name is the identity shared with the panel's auth file list; the
   operator's note sits under it, smaller, and only when there is one. */
.who{display:flex;flex-direction:column;min-width:0}
.note{color:var(--text-secondary);font-size:12px;overflow-wrap:break-word}
.status-line{color:var(--failure-text);font-size:12px;overflow-wrap:break-word}
.actions{display:flex;flex-wrap:wrap;gap:6px;margin:8px 0 0 30px}
.chev{flex:none;width:14px;color:var(--text-tertiary);font-size:12px;line-height:20px;text-align:center;transition:transform .15s var(--ease)}
tr.row[aria-expanded="true"] .chev{transform:rotate(90deg);color:var(--text-primary)}
/* break-word, not anywhere: a name wraps only when it cannot fit, and never
   mid-word when a line is merely tight. */
.label{font-weight:600;overflow-wrap:break-word}
td.account{min-width:210px}
.dot{flex:none;width:8px;height:8px;border-radius:50%;background:var(--viz-success);box-shadow:0 0 0 3px color-mix(in srgb,var(--viz-success) 18%,transparent)}
.dot.error{background:var(--viz-failure);box-shadow:0 0 0 3px color-mix(in srgb,var(--viz-failure) 18%,transparent)}
.dot.muted{background:var(--text-tertiary);box-shadow:none}
.act{
  display:inline-flex;align-items:center;min-height:28px;padding:0 11px;border:1px solid var(--border-primary);border-radius:999px;
  background:var(--bg-secondary);color:var(--text-primary);font:inherit;font-size:12px;font-weight:600;line-height:1;white-space:nowrap;cursor:pointer;
}
.act:focus-visible{outline:2px solid var(--primary-color);outline-offset:2px}
.act:disabled{cursor:progress;opacity:.6}
.act.attention{border-color:color-mix(in srgb,var(--viz-failure) 40%,transparent);background:color-mix(in srgb,var(--viz-failure) 9%,transparent);color:var(--failure-text)}
/* The plan pool row is classed "pool plan" by its kind; only the plan name
   in the second column is drawn as a badge. */
.plan:not(.pool){display:inline-block;padding:2px 8px;border:1px solid var(--border-primary);border-radius:6px;background:var(--bg-secondary);font-size:12px;font-weight:600;white-space:nowrap}
.pair{white-space:nowrap}
.sep,.cap{color:var(--text-secondary);font-weight:400}
.strong{font-weight:600}
/* The rail sits on the figure's own line. Stacking the percentage above a rail
   gave the cell two lines where every other cell had one, and the row then read
   as vertically misaligned even though each cell was centred. */
.gauge{display:flex;align-items:center;justify-content:flex-end;gap:9px;min-width:118px}
.track{flex:1 1 auto;max-width:72px;height:5px;border-radius:999px;background:var(--bg-tertiary);overflow:hidden}
.fill{display:block;height:100%;border-radius:inherit;background:var(--viz-success)}
.fill.high{background:var(--amber)}
.fill.full{background:var(--viz-failure)}
.pct.high{color:var(--amber-text)}
.pct.full{color:var(--failure-text)}
/* A pool row names which money it is; the plan row is the ceiling, the rest are
   grants, so the grants read one step quieter than the ceiling they sit under. */
.pool{font-weight:500}
.pool.trial,.pool.bonus,.pool.overage_credit{color:var(--text-secondary);font-weight:400}
.triple{white-space:nowrap;color:var(--text-secondary)}
.triple b{color:var(--text-primary);font-weight:600}
.charged{color:var(--failure-text)}
.triple b.charged{color:var(--failure-text)}
.reset{white-space:nowrap}
/* Countdown and pool status stay on the figure's line. Any cell that grows to a
   second line makes the whole row read as misaligned, which is what the stacked
   percentage and the stacked countdown both did. */
.countdown,.pool-status{color:var(--text-secondary);font-size:12px;white-space:nowrap}
.countdown::before,.pool-status::before{content:"·";margin:0 5px;color:var(--text-tertiary)}
.countdown.soon{color:var(--amber-text)}
.none{color:var(--text-tertiary)}
.flag{
  display:inline-block;margin-left:6px;padding:0 6px;border:1px solid color-mix(in srgb,var(--viz-failure) 40%,transparent);
  border-radius:999px;background:color-mix(in srgb,var(--viz-failure) 9%,transparent);color:var(--failure-text);font-size:12px;font-weight:600;
}
tfoot td{border-top:1px solid var(--border-primary);background:var(--bg-tertiary);font-weight:600}
tfoot .sub{color:var(--text-secondary);font-size:12px;font-weight:400}
tfoot .sub::before{content:"·";margin:0 5px}

tr.meta>td{padding:0 16px 16px 46px;background:color-mix(in srgb,var(--bg-tertiary) 35%,transparent);border-top:0}
tr.meta[hidden]{display:none}
.meta-body{display:grid;gap:12px;padding:14px 0 0;border-top:1px dashed var(--border-primary)}
.facts{display:grid;grid-template-columns:repeat(auto-fill,minmax(min(100%,230px),1fr));gap:12px 24px;margin:0}
.fact{display:flex;flex-direction:column;gap:2px;margin:0;min-width:0;font-size:12px;line-height:1.5}
.fact dt{color:var(--text-secondary)}
.fact dd{margin:0;min-width:0;color:var(--text-primary);font-weight:500;overflow-wrap:anywhere}
.meta-updated{margin:0;color:var(--text-secondary);font-size:12px}
.empty{
  display:flex;flex-direction:column;align-items:center;gap:14px;margin:18px 0 0;padding:44px 20px;
  border:1px dashed var(--border-primary);border-radius:14px;background:var(--bg-primary);color:var(--text-secondary);text-align:center;
}
.empty p{margin:0;max-width:440px}

/* The Add account dialog is the sign-in page's dialog over a dimmed page. */
.overlay{
  position:fixed;inset:0;z-index:50;display:flex;padding:24px 16px;overflow-y:auto;
  background:color-mix(in srgb,#000 42%,transparent);
}
.overlay .dialog{margin:auto}
.head .close{margin-left:auto;align-self:flex-start}

@media (hover:hover) and (pointer:fine){
  .act:hover:not(:disabled){border-color:var(--border-hover);background:var(--bg-tertiary)}
  .act.attention:hover:not(:disabled){border-color:var(--viz-failure);background:color-mix(in srgb,var(--viz-failure) 14%,transparent)}
  .filter:hover{color:var(--text-primary)}
}
@media (pointer:coarse){
  .act{min-height:40px}
  .filter{height:40px}
  .search input,.select select{min-height:44px}
}
@media (prefers-reduced-motion:reduce){
  .sk-cards span,.sk-rows span{animation:none}
  .chev{transition:none}
}
/* A laptop frames this page at about 1040-1200px. There the table stays a
   table and its text columns give up their single line instead: the money
   triple, the reset date and its countdown wrap, the rails shorten. Measured
   with six accounts: 1093px of columns at 960 before, no overflow after. */
@media (max-width:1180px){
  th,td{padding:10px 8px}
  td.account{min-width:168px}
  .gauge{min-width:92px;gap:6px}
  .track{max-width:44px}
  .triple,.reset{white-space:normal}
  .countdown,.pool-status{display:block}
  .countdown::before,.pool-status::before{content:none}
  .pair .cap{display:none}
}
@media (max-width:760px){
  .page{padding:16px 14px 24px}
  /* The panel keeps its cluster at a 12px gutter on a narrow viewport, so the
     reserved corner grows as a share of the width rather than shrinking. */
  .page-head{padding-right:clamp(150px,44vw,196px)}
  .finders{flex-basis:100%;justify-content:stretch}
  .search{flex:1 1 200px}
}
/* Only a phone loses the table. Down to 641px the eight columns stay a
   comparison table, scrolling sideways if they must; below it each account
   becomes its own block and every value keeps the column head as its label. */
@media (max-width:640px){
  .page-head{padding-right:0}
  .head-copy{padding-right:clamp(150px,44vw,196px)}
  .head-actions{width:100%}
  .head-actions .pill{flex:1 1 auto}
  thead{position:absolute;width:1px;height:1px;overflow:hidden;clip:rect(0 0 0 0);white-space:nowrap}
  table,tbody,tfoot,tr,td{display:block;width:auto}
  tbody.account,tfoot tr{padding:8px 14px 12px}
  tbody.account>tr>td,tfoot td{border-top:0;background:transparent}
  tbody.account+tbody.account{border-top:1px solid var(--border-color)}
  tfoot tr{border-top:1px solid var(--border-primary);background:var(--bg-tertiary)}
  th,td,th:first-child,td:first-child,th:last-child,td:last-child{padding:3px 0}
  td[data-label]{display:flex;flex-wrap:wrap;align-items:baseline;justify-content:flex-end;gap:2px 14px}
  /* The label pushes the cell's values to the right as one group; a cell with
     several values (a date, its countdown, a pool status) keeps them together. */
  td[data-label]::before{content:attr(data-label);flex:none;margin-right:auto;color:var(--text-secondary);font-weight:400}
  tfoot td:first-child::before{content:none}
  tfoot td:first-child{justify-content:flex-start}
  td.account[data-label]::before{content:none}
  /* The account cell stacks: name, note, then its actions on their own line,
     so the name keeps the full card width. */
  td.account[data-label]{display:block;min-width:0}
  .actions{margin:8px 0 4px 30px}
  .gauge{min-width:110px}
  tr.meta>td{padding:0 0 4px}
  .triple{white-space:normal;text-align:right}
}
`

// usageShellText holds the shell's own strings; the key prompt shares the
// rest with the sign-in page through shellCommonScript, the dialog's form
// through signInText.
const usageShellText = `  var TEXT = {
    en: {
      title: 'Kiro Usage',
      intro: 'Subscription usage reported by Kiro for each connected account.',
      loading: 'Loading Kiro usage…',
      keyPrompt: 'Enter the CLIProxyAPI management key to view Kiro usage.',
      loadFailed: 'Kiro usage could not be loaded: {message}',
      addAccount: 'Add account',
      refreshAll: 'Reload all quotas',
      f_all: 'All',
      f_active: 'Active',
      f_attention: 'Needs attention',
      f_disabled: 'Disabled',
      filters: 'Show accounts',
      search: 'Search accounts',
      searchHint: 'File, note, email or plan',
      clearSearch: 'Clear search',
      sortLabel: 'Sort',
      s_default: 'Connection order',
      s_share: 'Share used, highest first',
      s_remaining: 'Remaining, lowest first',
      s_reset: 'Resets soonest',
      s_name: 'Name',
      shown: 'Showing {n} of {total} accounts',
      noMatch: 'No account matches.',
      clearFilters: 'Clear filters',
      addTitle: 'Add Kiro account',
      close: 'Close',
      finish: 'Done',
      stStarting: 'Opening a sign-in session…',
      stSaving: 'Saving the account…',
      stepReturn: 'Keep this dialog open',
      returnHint: 'Once AWS approves, CPA saves the account and the list reloads on its own. Nothing needs to be pasted back.',
      connected: 'Kiro account added.',
      doneHint: 'CPA saved the account and the list has been reloaded.'
    },
    vi: {
      title: 'Hạn mức Kiro',
      intro: 'Mức sử dụng gói Kiro báo về cho từng tài khoản đã kết nối.',
      loading: 'Đang tải hạn mức Kiro…',
      keyPrompt: 'Nhập management key của CLIProxyAPI để xem hạn mức Kiro.',
      loadFailed: 'Không tải được hạn mức Kiro: {message}',
      addAccount: 'Thêm tài khoản',
      refreshAll: 'Tải lại tất cả',
      f_all: 'Tất cả',
      f_active: 'Hoạt động',
      f_attention: 'Cần xem lại',
      f_disabled: 'Đã tắt',
      filters: 'Hiện tài khoản',
      search: 'Tìm tài khoản',
      searchHint: 'Tên file, ghi chú, email hoặc gói',
      clearSearch: 'Xoá ô tìm',
      sortLabel: 'Sắp xếp',
      s_default: 'Thứ tự kết nối',
      s_share: 'Đã dùng nhiều nhất',
      s_remaining: 'Còn lại ít nhất',
      s_reset: 'Sắp đặt lại nhất',
      s_name: 'Tên',
      shown: 'Đang hiện {n} trên {total} tài khoản',
      noMatch: 'Không có tài khoản nào khớp.',
      clearFilters: 'Bỏ lọc',
      addTitle: 'Thêm tài khoản Kiro',
      close: 'Đóng',
      finish: 'Xong',
      stStarting: 'Đang mở phiên đăng nhập…',
      stSaving: 'Đang lưu tài khoản…',
      stepReturn: 'Giữ hộp thoại này mở',
      returnHint: 'Khi AWS chấp thuận, CPA tự lưu tài khoản và danh sách tự tải lại. Không cần dán gì vào đây.',
      connected: 'Đã thêm tài khoản Kiro.',
      doneHint: 'CPA đã lưu tài khoản và danh sách đã được tải lại.'
    },
    'zh-CN': {
      title: 'Kiro 用量',
      intro: 'Kiro 为每个已连接账户报告的订阅用量。',
      loading: '正在加载 Kiro 用量…',
      keyPrompt: '请输入 CLIProxyAPI 管理密钥以查看 Kiro 用量。',
      loadFailed: '无法加载 Kiro 用量：{message}',
      addAccount: '添加账户',
      refreshAll: '全部刷新',
      f_all: '全部',
      f_active: '正常',
      f_attention: '需要处理',
      f_disabled: '已停用',
      filters: '显示账户',
      search: '搜索账户',
      searchHint: '文件名、备注、邮箱或套餐',
      clearSearch: '清除搜索',
      sortLabel: '排序',
      s_default: '连接顺序',
      s_share: '使用占比从高到低',
      s_remaining: '剩余从少到多',
      s_reset: '最近重置',
      s_name: '名称',
      shown: '显示 {n} / {total} 个账户',
      noMatch: '没有匹配的账户。',
      clearFilters: '清除筛选',
      addTitle: '添加 Kiro 账户',
      close: '关闭',
      finish: '完成',
      stStarting: '正在创建登录会话…',
      stSaving: '正在保存账户…',
      stepReturn: '请保持此对话框打开',
      returnHint: 'AWS 批准后，CPA 会自动保存账户，列表也会自动刷新。无需粘贴任何内容。',
      connected: 'Kiro 账户已添加。',
      doneHint: 'CPA 已保存账户，列表已刷新。'
    }
  };
`

// usageShellLogic loads the fragment with the panel's management key, asks for
// the key when the panel did not keep one, drives the row toggles, countdowns,
// filters and account actions through event delegation, and runs the Add
// account dialog.
//
// The dialog finishes the sign-in itself. It asks CPA for a sign-in session
// (GET /v8/management/oauth/auth-url, which calls the plugin's StartLogin and
// registers the state), posts the form to the plugin's /connect under that
// state, then polls GET /v8/management/oauth/status: that poll is what calls
// the plugin's PollLogin and saves the credential, exactly as the panel's
// OAuth card does. Closing the dialog cancels the session.
const usageShellLogic = `  var POLL_MS = 3000;
  var busy = false;
  var filter = 'all';

  function start() {
    var page = byId('kiro-page');
    var gate = byId('kiro-gate');
    var state = byId('kiro-state');
    var keyForm = byId('kiro-key-form');
    var keyInput = byId('kiro-key');
    var flash = byId('kiro-flash');
    var host = byId('kiro-usage');
    var actions = byId('kiro-actions');
    var skeleton = byId('kiro-skeleton');
    var tools = byId('kiro-tools');
    var search = byId('kiro-search');
    var clear = byId('kiro-clear');
    var sort = byId('kiro-sort');
    var modal = byId('kiro-modal');

    function showGate(message, askKey, failed) {
      host.textContent = '';
      tools.hidden = true;
      skeleton.hidden = true;
      showAlert(flash, '');
      gate.hidden = false;
      state.className = failed ? 'alert' : 'gate-text';
      state.textContent = message;
      keyForm.hidden = !askKey;
      actions.hidden = askKey;
      if (askKey) {
        keyInput.focus();
      }
    }

    function failWith(template) {
      return function (error) {
        if (error && error.needKey) {
          showGate(error.rejected ? t('keyRejected') : t('keyPrompt'), true, false);
          return;
        }
        var message = template.replace('{message}', error && error.message ? error.message : String(error));
        if (host.firstChild) {
          showAlert(flash, message);
        } else {
          showGate(message, false, true);
        }
      };
    }

    function setBusy(flag) {
      busy = flag;
      host.setAttribute('aria-busy', flag ? 'true' : 'false');
      var controls = document.querySelectorAll('button[data-refresh]');
      for (var index = 0; index < controls.length; index += 1) {
        controls[index].disabled = flag;
        controls[index].classList.toggle('busy', flag && controls[index].getAttribute('data-refresh') === 'all');
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

    // A reset cell shows the day only; the detail row and every tooltip carry
    // the full timestamp.
    function formatTimes() {
      var long = null;
      var short = null;
      try {
        long = new Intl.DateTimeFormat(lang, { dateStyle: 'medium', timeStyle: 'short' });
        short = new Intl.DateTimeFormat(lang, { month: 'short', day: 'numeric' });
      } catch (error) {
        long = null;
      }
      var stamps = host.querySelectorAll('time[datetime]');
      for (var index = 0; index < stamps.length; index += 1) {
        var node = stamps[index];
        var parsed = new Date(node.getAttribute('datetime'));
        if (isNaN(parsed.getTime())) {
          continue;
        }
        node.setAttribute('data-ms', String(parsed.getTime()));
        node.setAttribute('title', long ? long.format(parsed) : parsed.toISOString());
        var formatter = node.hasAttribute('data-short') ? short : long;
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

    // Sort keys a group does not carry go last, whichever way the sort runs.
    function sortKey(group, order) {
      var raw = group.getAttribute('data-' + order);
      if (order === 'reset') {
        var when = raw ? Date.parse(raw) : NaN;
        return isNaN(when) ? Infinity : when;
      }
      var number = raw === null ? NaN : Number(raw);
      if (isNaN(number)) {
        return Infinity;
      }
      return order === 'share' ? -number : number;
    }

    function compare(order) {
      return function (left, right) {
        var byIndex = Number(left.getAttribute('data-index')) - Number(right.getAttribute('data-index'));
        if (order === 'name') {
          return left.getAttribute('data-name').localeCompare(right.getAttribute('data-name'), lang) || byIndex;
        }
        if (order === 'default') {
          return byIndex;
        }
        var a = sortKey(left, order);
        var b = sortKey(right, order);
        return a === b ? byIndex : (a < b ? -1 : 1);
      };
    }

    // applyView orders the account groups and hides those the filter or the
    // search leaves out. The totals row keeps the whole fleet.
    function applyView() {
      var table = host.querySelector('table');
      var groups = table ? Array.prototype.slice.call(table.querySelectorAll('tbody.account')) : [];
      var foot = table ? table.querySelector('tfoot') : null;
      groups.sort(compare(sort.value));
      var query = search.value.trim().toLowerCase();
      var counts = { all: 0, active: 0, attention: 0, disabled: 0 };
      var shown = 0;
      for (var index = 0; index < groups.length; index += 1) {
        var group = groups[index];
        table.insertBefore(group, foot);
        var kind = group.getAttribute('data-group');
        counts.all += 1;
        counts[kind] = (counts[kind] || 0) + 1;
        var visible = (filter === 'all' || filter === kind) &&
          (!query || (group.getAttribute('data-search') || '').indexOf(query) >= 0);
        group.hidden = !visible;
        if (visible) {
          shown += 1;
        }
      }
      var buttons = tools.querySelectorAll('[data-filter]');
      for (var b = 0; b < buttons.length; b += 1) {
        var name = buttons[b].getAttribute('data-filter');
        buttons[b].setAttribute('aria-pressed', name === filter ? 'true' : 'false');
        buttons[b].querySelector('.count').textContent = String(counts[name] || 0);
      }
      clear.hidden = !search.value;
      var narrowed = filter !== 'all' || query !== '';
      byId('kiro-shown').textContent = shown === 0 && groups.length > 0 ? t('noMatch') :
        t('shown').replace('{n}', String(shown)).replace('{total}', String(groups.length));
      byId('kiro-reset-filters').hidden = !narrowed;
    }

    function labelPage() {
      tools.querySelector('.filters').setAttribute('aria-label', t('filters'));
      search.setAttribute('aria-label', t('search'));
      search.placeholder = t('searchHint');
      clear.setAttribute('aria-label', t('clearSearch'));
      clear.title = t('clearSearch');
      byId('kiro-close').setAttribute('aria-label', t('close'));
      byId('kiro-close').title = t('close');
    }

    function load(refresh) {
      if (busy) {
        return;
      }
      var open = openDetails();
      if (!host.firstChild) {
        gate.hidden = false;
        keyForm.hidden = true;
        state.className = 'gate-text';
        state.textContent = t('loading');
        skeleton.hidden = false;
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
          actions.hidden = false;
          showAlert(flash, '');
          var slot = host.querySelector('.tools-slot');
          if (slot) {
            slot.appendChild(tools);
          }
          tools.hidden = !slot;
          formatTimes();
          for (var index = 0; index < open.length; index += 1) {
            var row = host.querySelector('tr.row[data-target="' + open[index] + '"]');
            if (row) {
              toggle(row, true);
            }
          }
          applyView();
          tick();
        })
        .catch(failWith(t('loadFailed')))
        .then(function () {
          skeleton.hidden = true;
          setBusy(false);
        });
    }

    // ---- Add account dialog ----
    var session = '';
    var round = 0;
    var pollTimer = 0;
    var opener = null;

    function cancelSession(stale) {
      if (stale) {
        request('/v8/management/oauth/session?state=' + encodeURIComponent(stale), { method: 'DELETE' })
          .catch(function () {});
      }
    }

    // dropSession stops the poll and tells CPA the session is abandoned.
    function dropSession() {
      window.clearTimeout(pollTimer);
      cancelSession(session);
      session = '';
    }

    function openSignIn(method, from) {
      if (!currentKey()) {
        showGate(t('keyPrompt'), true, false);
        return;
      }
      round += 1;
      opener = from || null;
      chooseMethod(method || 'builder-id');
      showAlert(byId('kiro-error'), '');
      byId('kiro-finish').hidden = true;
      showForm();
      modal.hidden = false;
      page.inert = true;
      var checked = byId('kiro-login').querySelector('input[name="method"]:checked');
      if (checked) {
        checked.focus();
      }
    }

    function closeSignIn() {
      if (modal.hidden) {
        return;
      }
      round += 1;
      dropSession();
      endSigning();
      clearSecrets();
      modal.hidden = true;
      page.inert = false;
      if (opener && document.contains(opener)) {
        opener.focus();
      }
      opener = null;
    }

    function failSignIn(error) {
      if (error && error.needKey) {
        closeSignIn();
        showGate(error.rejected ? t('keyRejected') : t('keyPrompt'), true, false);
        return;
      }
      round += 1;
      dropSession();
      signInFailed(error);
    }

    // watch polls CPA until it has saved the credential. A poll answered after
    // the dialog closed or restarted belongs to a flow nobody is waiting for.
    function watch(current, delay) {
      pollTimer = window.setTimeout(function () {
        request('/v8/management/oauth/status?state=' + encodeURIComponent(session), { method: 'GET' })
          .then(function (response) {
            return response.json();
          })
          .then(function (answer) {
            if (current !== round) {
              return;
            }
            if (answer && answer.status === 'ok') {
              session = '';
              showDone();
              byId('kiro-finish').hidden = false;
              byId('kiro-finish').focus();
              load('');
              return;
            }
            if (answer && answer.status === 'error') {
              throw { message: answer.error || 'Authentication failed' };
            }
            watch(current, POLL_MS);
          })
          .catch(function (error) {
            if (current === round) {
              failSignIn(error);
            }
          });
      }, delay);
    }

    byId('kiro-login').addEventListener('submit', function (event) {
      event.preventDefault();
      if (signing) {
        return;
      }
      var current = round;
      signing = true;
      byId('kiro-submit').disabled = true;
      byId('kiro-submit').classList.add('busy');
      showAlert(byId('kiro-error'), '');
      setStatus('wait', 'stStarting');
      request('/v8/management/oauth/auth-url?provider=kiro', { method: 'GET' })
        .then(function (response) {
          return response.json();
        })
        .then(function (answer) {
          if (current !== round) {
            cancelSession(answer && answer.state);
            return 'abandoned';
          }
          if (!answer || !answer.state) {
            throw { message: 'CPA did not open a sign-in session' };
          }
          session = String(answer.state);
          return connectSignIn(session);
        })
        .then(function (step) {
          if (step === 'abandoned' || current !== round) {
            return;
          }
          if (step === 'connected') {
            setStatus('wait', 'stSaving');
          }
          watch(current, step === 'device' ? POLL_MS : 0);
        })
        .catch(function (error) {
          endSigning();
          if (current === round) {
            failSignIn(error);
          }
        });
    });

    document.addEventListener('click', function (event) {
      var target = event.target;
      if (!target || !target.closest) {
        return;
      }
      var control = target.closest('button[data-refresh]');
      if (control) {
        if (!busy) {
          load(control.getAttribute('data-refresh'));
        }
        return;
      }
      var signIn = target.closest('button[data-signin]');
      if (signIn) {
        openSignIn(signIn.getAttribute('data-signin'), signIn);
        return;
      }
      var pick = target.closest('button[data-filter]');
      if (pick) {
        filter = pick.getAttribute('data-filter');
        applyView();
        return;
      }
      if (!host.contains(target) || target.closest('a,button,input,select,textarea')) {
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

    document.addEventListener('keydown', function (event) {
      if (event.key === 'Escape' && !modal.hidden) {
        event.preventDefault();
        closeSignIn();
        return;
      }
      var row = event.target;
      if (!row || !row.matches || !row.matches('tr.row[data-target]')) {
        return;
      }
      if (event.key === 'Enter' || event.key === ' ' || event.key === 'Spacebar') {
        event.preventDefault();
        toggle(row);
      }
    });

    search.addEventListener('input', applyView);
    sort.addEventListener('change', applyView);
    clear.addEventListener('click', function () {
      search.value = '';
      applyView();
      search.focus();
    });
    byId('kiro-reset-filters').addEventListener('click', function () {
      filter = 'all';
      search.value = '';
      applyView();
    });
    byId('kiro-close').addEventListener('click', closeSignIn);
    byId('kiro-finish').addEventListener('click', closeSignIn);

    keyForm.addEventListener('submit', function (event) {
      event.preventDefault();
      memoryKey = keyInput.value.trim();
      keyInput.value = '';
      if (memoryKey) {
        load('');
      }
    });

    var panel = panelRoot();
    if (panel && window.MutationObserver) {
      new MutationObserver(function () {
        root.setAttribute('data-panel-theme', pickPanelTheme());
        var next = pickLang();
        if (next !== lang) {
          lang = next;
          root.setAttribute('lang', lang);
          localise();
          labelPage();
          labelSignIn();
          syncFields();
          load('');
        }
      }).observe(panel, { attributes: true, attributeFilter: ['data-theme', 'lang'] });
    }

    bindSignIn();
    localise();
    labelPage();
    labelSignIn();
    syncFields();
    load('');
    window.setInterval(tick, 30000);
  }

  whenReady(start);
`

// usageViewHTML is the authenticated fragment the shell injects. It holds no
// script and no inline handler; its controls are plain buttons the shell
// script handles. Each account group carries what the toolbar filters, searches
// and sorts on, so the shell never parses a figure out of the text.
const usageViewHTML = `<div class="kiro-view" lang="{{.Options.Lang}}" data-text-in="{{text "countdown_in"}}" data-text-due="{{text "countdown_due"}}">
{{emailOff}}
  {{if .Empty}}<div class="empty" role="status"><p>{{text "empty"}}</p><button type="button" class="act" data-signin="">{{text "action_add"}}</button></div>{{else}}
  {{$totals := .Totals}}
  <dl class="totals">
    <div class="total">
      <dt><span>{{text "total_used"}}</span><span class="badge {{percentClass $totals.Percent}}"><span class="sr">{{text "total_share"}}: </span>{{printf "%.1f%%" $totals.Percent}}</span></dt>
      <dd class="figure"><span class="big">{{formatNumber $totals.Used}}</span> {{text "bucket_of"}} {{formatNumber $totals.Limit}}{{if $totals.Unit}} {{lower $totals.Unit}}{{end}}{{if $totals.MixedUnits}} ({{text "total_mixed_units"}}){{end}}</dd>
      <dd><span class="track" role="meter" aria-label="{{text "total_share"}}" aria-valuemin="0" aria-valuemax="{{formatNumber $totals.Limit}}" aria-valuenow="{{formatNumber $totals.Used}}"><span class="fill {{percentClass $totals.Percent}}" style="width:{{printf "%.2f" $totals.Percent}}%"></span></span></dd>
    </div>
    <div class="total">
      <dt>{{text "total_remaining"}}</dt>
      <dd class="figure"><span class="big">{{formatNumber $totals.Remaining}}</span>{{if $totals.Unit}} {{lower $totals.Unit}}{{end}}</dd>
    </div>
    <div class="total">
      <dt>{{text "total_charged"}}</dt>
      <dd class="figure{{if gt $totals.OverageCharges 0.0}} charged{{end}}"><span class="big">{{formatNumber $totals.OverageCharges}}</span>{{if $totals.Currency}} {{$totals.Currency}}{{end}}</dd>
      {{if or (gt $totals.TrialLimit 0.0) (gt $totals.BonusTotal 0.0) (gt $totals.OverageCredit 0.0)}}<dd class="extras">
        {{if gt $totals.TrialLimit 0.0}}<span>{{text "total_trial"}} <b>{{formatNumber $totals.TrialUsed}}</b> {{text "bucket_of"}} {{formatNumber $totals.TrialLimit}}</span>{{end}}
        {{if gt $totals.BonusTotal 0.0}}<span>{{text "total_bonus"}} <b>{{formatNumber $totals.BonusTotal}}</b></span>{{end}}
        {{if gt $totals.OverageCredit 0.0}}<span>{{text "total_overage_credit"}} <b>{{formatNumber $totals.OverageCredit}}</b></span>{{end}}
      </dd>{{end}}
    </div>
    <div class="total">
      <dt>{{text "stat_accounts"}}</dt>
      <dd class="figure"><span class="big">{{.Summary.Accounts}}</span></dd>
      <dd class="extras"><span>{{text "stat_active"}} <b>{{.Summary.Reporting}}</b></span>{{if .Summary.Attention}}<span class="attention">{{text "stat_attention"}} <b>{{.Summary.Attention}}</b></span>{{end}}{{if .Summary.Disabled}}<span>{{text "stat_disabled"}} <b>{{.Summary.Disabled}}</b></span>{{end}}</dd>
    </div>
  </dl>
  <div class="tools-slot"></div>
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
      <tbody class="account" data-index="{{$index}}" data-group="{{accountGroup $account}}" data-name="{{displayName $account}}" data-search="{{searchText $account}}"{{with planBucket $account}} data-share="{{printf "%.4f" .Percent}}" data-remaining="{{printf "%.4f" .Remaining}}"{{if .Reset}} data-reset="{{.Reset}}"{{end}}{{end}}>
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
            {{if $bucket.Reset}}<span class="reset"><time datetime="{{$bucket.Reset}}" data-countdown data-short>{{$bucket.Reset}}</time></span>{{else}}{{if $bucket.Expiry}}<span class="reset"><time datetime="{{$bucket.Expiry}}" data-countdown data-short>{{$bucket.Expiry}}</time></span>{{else}}<span class="none">&mdash;</span>{{end}}{{end}}
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
              {{if $message}}<p class="alert" role="status">{{$message}}</p>{{end}}
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
  {{if needsAttention $account}}<button type="button" class="act attention" data-signin="{{signInMethod $account.AuthMethod}}">{{text "action_relogin"}}</button>{{end}}
  {{if ne $account.StateKey "disabled"}}<button type="button" class="act" data-refresh="{{$account.FileName}}">{{text "action_refresh"}}</button>{{end}}
</span>{{end}}
{{end}}`
