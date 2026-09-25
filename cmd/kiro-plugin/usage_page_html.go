package main

import "html/template"

// usagePageTemplate is parsed once with placeholder helpers; renderUsagePage
// clones it per response and rebinds the helpers to the requested language.
var usagePageTemplate = template.Must(
	template.New("kiro-usage").Funcs(usagePageFuncs(usagePageTextPacks[usageLangEN])).Parse(usagePageHTML),
)

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
const usagePageHTML = `<!doctype html>
<html lang="{{.Options.Lang}}" data-theme="{{.Options.Theme}}">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<meta http-equiv="Content-Security-Policy" content="default-src 'none'; style-src 'unsafe-inline'; script-src 'nonce-{{.Options.Nonce}}'; base-uri 'none'">
<title>{{text "title"}}</title>
<style>
html[data-theme="dark"]{
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
main{width:min(1560px,100%);margin:0 auto;padding:18px 20px 26px}
/* The panel's floating button cluster sits over this page's top right corner
   (fixed, 24px inset, 36-38px controls). The title row keeps that corner clear
   instead of putting text under it. */
.page-head{padding-right:clamp(150px,17vw,208px)}
h1{margin:0;font-size:17px;line-height:1.3;font-weight:600;letter-spacing:-.01em}
.intro{margin:2px 0 0;color:var(--muted);font-size:12px}

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
.act{
  display:inline-flex;align-items:center;min-height:26px;padding:0 10px;
  border:1px solid var(--border-2);border-radius:999px;background:var(--surface);
  color:var(--text-2);font-size:12px;font-weight:500;text-decoration:none;white-space:nowrap;
}
.act:hover{border-color:var(--accent);color:var(--text)}
.act:focus-visible{outline:2px solid var(--accent);outline-offset:1px}
.act.primary{border-color:var(--bad-line);background:var(--bad-bg);color:var(--bad)}
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
.notice{
  margin:0;padding:8px 11px;border:1px solid var(--bad-line);border-radius:8px;
  background:var(--bad-bg);color:var(--bad);font-size:12px;
}
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
.sr{position:absolute;width:1px;height:1px;overflow:hidden;clip:rect(0 0 0 0);white-space:nowrap}

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
</style>
</head>
<body data-text-in="{{text "countdown_in"}}" data-text-due="{{text "countdown_due"}}">
<noscript><style>tr.meta[hidden]{display:table-row}.chev{display:none}tr.row{cursor:default}</style></noscript>
{{emailOff}}
<main>
  <header class="page-head">
    <h1>{{text "title"}}</h1>
    <p class="intro">{{text "intro"}}</p>
    {{if not .Empty}}<p class="page-actions"><a class="act" href="?refresh=all&amp;theme={{.Options.Theme}}&amp;lang={{.Options.Lang}}">{{text "action_refresh_all"}}</a></p>{{end}}
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
            {{template "account-cell" (cell $account $)}}
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
            {{template "account-cell" (cell $account $)}}
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
</main>
{{emailOn}}
<script nonce="{{.Options.Nonce}}">
(function () {
  var root = document.documentElement;
  var lang = root.getAttribute('lang') || 'en';
  var inPattern = document.body.getAttribute('data-text-in') || 'in {duration}';
  var duePattern = document.body.getAttribute('data-text-due') || 'due now';
  var formatter = null;
  try {
    formatter = new Intl.DateTimeFormat(lang, { dateStyle: 'medium', timeStyle: 'short' });
  } catch (error) {
    formatter = null;
  }
  var stamps = document.querySelectorAll('time[datetime]');
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
  // The row itself is the control: there is no separate button to hunt for. It
  // keeps role=button and tabindex so a keyboard reaches it the way a pointer does.
  var rows = document.querySelectorAll('tr.row[data-target]');
  for (var r = 0; r < rows.length; r += 1) {
    (function (row) {
      var detail = document.getElementById(row.getAttribute('data-target'));
      if (!detail) {
        row.removeAttribute('role');
        row.removeAttribute('tabindex');
        row.removeAttribute('aria-expanded');
        row.style.cursor = 'default';
        return;
      }
      // An account can own several credit rows. Clicking any of them opens the one
      // detail they share, so every row of that account reports the same state.
      var siblings = document.querySelectorAll('tr.row[data-target="' + row.getAttribute('data-target') + '"]');
      function flip() {
        var open = row.getAttribute('aria-expanded') === 'true';
        for (var s = 0; s < siblings.length; s += 1) {
          siblings[s].setAttribute('aria-expanded', open ? 'false' : 'true');
        }
        detail.hidden = open;
      }
      row.addEventListener('click', function (event) {
        // Dragging across the row to select a figure is not a click on the row.
        var selection = window.getSelection && window.getSelection();
        if (selection && selection.type === 'Range' && String(selection).length > 0) {
          return;
        }
        if (event.target && event.target.closest && event.target.closest('a,button,input,select,textarea')) {
          return;
        }
        flip();
      });
      row.addEventListener('keydown', function (event) {
        if (event.key === 'Enter' || event.key === ' ' || event.key === 'Spacebar') {
          event.preventDefault();
          flip();
        }
      });
    })(rows[r]);
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
    var now = Date.now();
    var pending = document.querySelectorAll('time[data-countdown][data-ms]');
    for (var index = 0; index < pending.length; index += 1) {
      var node = pending[index];
      var holder = node.parentNode;
      if (!holder) {
        continue;
      }
      var badge = holder.querySelector('.countdown');
      if (!badge) {
        badge = document.createElement('span');
        badge.className = 'countdown';
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
  var confirmLinks = document.querySelectorAll('a[data-confirm]');
  for (var c = 0; c < confirmLinks.length; c += 1) {
    confirmLinks[c].addEventListener('click', function (event) {
      if (!window.confirm(this.getAttribute('data-confirm'))) {
        event.preventDefault();
      }
    });
  }
  tick();
  setInterval(tick, 30000);
})();
</script>
</body>
</html>
{{define "account-cell"}}{{$account := .Account}}{{$view := .View}}
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
  {{if ne $account.StateKey "disabled"}}<a class="act" href="?refresh={{$account.FileName}}&amp;theme={{$view.Options.Theme}}&amp;lang={{$view.Options.Lang}}">{{text "action_refresh"}}</a>{{end}}
  {{if $view.ActionPath}}{{if eq $account.StateKey "disabled"}}<a class="act" href="{{$view.ActionPath}}?op=enable&amp;file={{$account.FileName}}&amp;theme={{$view.Options.Theme}}&amp;lang={{$view.Options.Lang}}">{{text "action_enable"}}</a>{{else}}<a class="act" data-confirm="{{text "confirm_disable"}}" href="{{$view.ActionPath}}?op=disable&amp;file={{$account.FileName}}&amp;theme={{$view.Options.Theme}}&amp;lang={{$view.Options.Lang}}">{{text "action_disable"}}</a>{{end}}{{end}}
</span>{{end}}
{{end}}`
