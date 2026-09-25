package main

import (
	cryptorand "crypto/rand"
	"encoding/base64"
	"fmt"
	"html/template"
	"io"
	"math"
	"strings"
)

// Kiro usage page presentation. Kept apart from the transport in usage.go so
// the layout, its text packs and its CSP nonce can be exercised on their own.
//
// Every localized string is resolved at render time, never when an account view
// is built: views are cached per credential (usageCacheTTL) and a cached view
// must be renderable in any language the next request asks for.

const (
	usageThemeDark  = "dark"
	usageThemeLight = "light"
	usageLangEN     = "en"
	usageLangVI     = "vi"
)

// usagePageText maps a text key to its localized string.
type usagePageText map[string]string

// get returns the localized string, or a visible marker so a missing key fails
// loudly in a rendered page instead of silently printing nothing.
func (text usagePageText) get(key string) string {
	if value, ok := text[key]; ok && strings.TrimSpace(value) != "" {
		return value
	}
	return "!" + key
}

var usagePageTextPacks = map[string]usagePageText{
	usageLangEN: {
		"title":                 "Kiro Usage",
		"intro":                 "Subscription usage reported by Kiro for each connected account.",
		"empty":                 "No Kiro accounts are connected. Add one from OAuth Login.",
		"col_overage":           "Overage",
		"col_overage_sub":       "cap · rate · charged",
		"total_row":             "Total",
		"total_accounts":        "{n} accounts",
		"total_used":            "Used of limit",
		"total_remaining":       "Remaining",
		"total_share":           "Share used",
		"total_charged":         "Overage charged",
		"total_mixed_units":     "mixed units",
		"row_hint":              "Open account detail",
		"label_overage_cap":     "Overage cap",
		"label_overage_rate":    "Overage rate",
		"label_overage_charges": "Overage charged",
		"label_overage_limit":   "Overage limit set",
		"label_free_trial":      "Free trial",
		"label_trial_expiry":    "Trial ends",
		"label_bonus":           "Bonus credit",
		"label_overage_credit":  "Overage credit",
		"label_days_reset":      "Days until reset",
		"label_overage_allowed": "Overage allowed",
		"label_upgrade":         "Upgrade",
		"label_manage":          "Plan managed by",
		"label_state":           "State",
		"value_yes":             "Yes",
		"value_no":              "No",
		"value_purchase":        "Self purchase",
		"value_manage":          "Organisation",
		"trial_expired":         "expired",
		"trial_active":          "active",
		"per_unit":              "per {unit}",
		"col_quota":             "Credit pool",
		"quota_plan":            "Plan credits",
		"quota_trial":           "Free trial",
		"quota_bonus":           "Bonus credits",
		"quota_overage_credit":  "Overage credits",
		"total_trial":           "Free trial",
		"total_bonus":           "Bonus credits",
		"total_overage_credit":  "Overage credits",
		"grants_count":          "{n} grants",
		"amount_unknown":        "amount not reported",
		"label_expiry":          "Expires",
		"col_account":           "Account",
		"col_plan":              "Plan",
		"col_state":             "State",
		"col_bucket":            "Quota",
		"col_used":              "Used / limit",
		"col_percent":           "Share",
		"col_remaining":         "Remaining",
		"col_reset":             "Resets",
		"table_caption":         "Kiro credit pools per account",
		"toggle_details":        "Show credential details",
		"stat_accounts":         "Accounts",
		"stat_active":           "Reporting",
		"stat_attention":        "Needs attention",
		"stat_generated":        "Generated",
		"state_active":          "Active",
		"state_disabled":        "Disabled",
		"state_unavailable":     "Unavailable",
		"plan_unknown":          "Unknown plan",
		"bucket_of":             "of",
		"label_remaining":       "Remaining",
		"label_overage":         "Overage",
		"label_renews":          "Renews",
		"label_unit":            "Unit",
		"label_currency":        "Currency",
		"label_auth_method":     "Sign-in method",
		"label_region":          "Region",
		"label_identity":        "Identity",
		"label_account":         "Account",
		"label_directory":       "Identity store",
		"label_aws_account":     "AWS account",
		"label_profile":         "Profile",
		"directory_builder_id":  "AWS Builder ID",
		"label_file":            "Credential file",
		"label_token_expires":   "Session expires",
		"label_last_refresh":    "Last refresh",
		"label_overage_status":  "Overage billing",
		"label_status":          "Status detail",
		"credential_heading":    "Credential",
		"updated_prefix":        "Usage read",
		"countdown_in":          "in {duration}",
		"countdown_due":         "due now",
		"auth_idc":              "IAM Identity Center",
		"auth_builder_id":       "AWS Builder ID",
		"auth_api_key":          "API key",
		"auth_external_idp":     "External identity provider",
		"auth_imported":         "Imported credential",
		"err_no_buckets":        "Kiro did not return a usage bucket for this account.",
		"err_list_failed":       "CLIProxyAPI could not list connected accounts.",
		"err_unavailable":       "This credential is currently unavailable.",
		"err_rejected":          "Kiro rejected this session. Sign in again from OAuth Login.",
		"err_rate_limited":      "Kiro rate-limited the usage request. Try again later.",
		"err_upstream":          "Kiro usage is temporarily unavailable.",
		"err_generic":           "Usage could not be loaded for this account.",
		"action_refresh":        "Reload quota",
		"action_refresh_all":    "Reload all quotas",
		"action_relogin":        "Sign in again",
		"action_disable":        "Disable",
		"action_enable":         "Enable",
		"confirm_disable":       "Disable this Kiro credential? CLIProxyAPI stops routing requests to it until it is enabled again.",
	},
	usageLangVI: {
		"title":                 "Hạn mức Kiro",
		"intro":                 "Mức sử dụng gói Kiro báo về cho từng tài khoản đã kết nối.",
		"empty":                 "Chưa có tài khoản Kiro nào được kết nối. Thêm ở trang OAuth Login.",
		"col_overage":           "Vượt hạn",
		"col_overage_sub":       "trần · đơn giá · đã tính",
		"total_row":             "Tổng",
		"total_accounts":        "{n} tài khoản",
		"total_used":            "Đã dùng trên trần",
		"total_remaining":       "Còn lại",
		"total_share":           "Tỷ lệ đã dùng",
		"total_charged":         "Phí vượt hạn",
		"total_mixed_units":     "khác đơn vị",
		"row_hint":              "Mở chi tiết tài khoản",
		"label_overage_cap":     "Trần vượt hạn",
		"label_overage_rate":    "Đơn giá vượt hạn",
		"label_overage_charges": "Phí vượt hạn đã tính",
		"label_overage_limit":   "Hạn vượt hạn đã đặt",
		"label_free_trial":      "Dùng thử miễn phí",
		"label_trial_expiry":    "Dùng thử hết hạn",
		"label_bonus":           "Credit tặng",
		"label_overage_credit":  "Credit vượt hạn",
		"label_days_reset":      "Số ngày tới hạn",
		"label_overage_allowed": "Cho phép vượt hạn",
		"label_upgrade":         "Nâng cấp gói",
		"label_manage":          "Gói do ai quản lý",
		"label_state":           "Trạng thái",
		"value_yes":             "Được",
		"value_no":              "Không",
		"value_purchase":        "Tự mua",
		"value_manage":          "Tổ chức quản lý",
		"trial_expired":         "đã hết hạn",
		"trial_active":          "đang chạy",
		"per_unit":              "mỗi {unit}",
		"col_quota":             "Loại credit",
		"quota_plan":            "Credit của gói",
		"quota_trial":           "Dùng thử miễn phí",
		"quota_bonus":           "Credit được tặng",
		"quota_overage_credit":  "Credit vượt hạn",
		"total_trial":           "Dùng thử miễn phí",
		"total_bonus":           "Credit được tặng",
		"total_overage_credit":  "Credit vượt hạn",
		"grants_count":          "{n} lần cấp",
		"amount_unknown":        "AWS không báo số lượng",
		"label_expiry":          "Hết hạn",
		"col_account":           "Tài khoản",
		"col_plan":              "Gói",
		"col_state":             "Trạng thái",
		"col_bucket":            "Hạng mức",
		"col_used":              "Đã dùng / trần",
		"col_percent":           "Tỷ lệ",
		"col_remaining":         "Còn lại",
		"col_reset":             "Đặt lại",
		"table_caption":         "Các loại credit Kiro theo từng tài khoản",
		"toggle_details":        "Xem thông tin xác thực",
		"stat_accounts":         "Tài khoản",
		"stat_active":           "Đang báo về",
		"stat_attention":        "Cần xem lại",
		"stat_generated":        "Kết xuất lúc",
		"state_active":          "Hoạt động",
		"state_disabled":        "Đã tắt",
		"state_unavailable":     "Không khả dụng",
		"plan_unknown":          "Chưa rõ gói",
		"bucket_of":             "trên",
		"label_remaining":       "Còn lại",
		"label_overage":         "Vượt hạn",
		"label_renews":          "Đặt lại",
		"label_unit":            "Đơn vị",
		"label_currency":        "Tiền tệ",
		"label_auth_method":     "Cách đăng nhập",
		"label_region":          "Vùng",
		"label_identity":        "Danh tính",
		"label_account":         "Tài khoản",
		"label_directory":       "Kho danh tính",
		"label_aws_account":     "Tài khoản AWS",
		"label_profile":         "Hồ sơ",
		"directory_builder_id":  "AWS Builder ID",
		"label_file":            "File xác thực",
		"label_token_expires":   "Phiên hết hạn",
		"label_last_refresh":    "Làm mới lần cuối",
		"label_overage_status":  "Tính phí vượt hạn",
		"label_status":          "Chi tiết trạng thái",
		"credential_heading":    "Thông tin xác thực",
		"updated_prefix":        "Đọc hạn mức lúc",
		"countdown_in":          "sau {duration}",
		"countdown_due":         "đến hạn",
		"auth_idc":              "IAM Identity Center",
		"auth_builder_id":       "AWS Builder ID",
		"auth_api_key":          "API key",
		"auth_external_idp":     "Nhà cung cấp danh tính ngoài",
		"auth_imported":         "Credential nhập vào",
		"err_no_buckets":        "Kiro không trả về hạn mức nào cho tài khoản này.",
		"err_list_failed":       "CLIProxyAPI không liệt kê được các tài khoản đã kết nối.",
		"err_unavailable":       "Credential này hiện không khả dụng.",
		"err_rejected":          "Kiro từ chối phiên này. Đăng nhập lại ở trang OAuth Login.",
		"err_rate_limited":      "Kiro đã chặn vì gọi quá nhiều. Thử lại sau.",
		"err_upstream":          "Hạn mức Kiro tạm thời không đọc được.",
		"err_generic":           "Không tải được hạn mức cho tài khoản này.",
		"action_refresh":        "Tải lại quota",
		"action_refresh_all":    "Tải lại tất cả",
		"action_relogin":        "Đăng nhập lại",
		"action_disable":        "Tắt",
		"action_enable":         "Bật",
		"confirm_disable":       "Tắt credential Kiro này? CLIProxyAPI sẽ ngừng chuyển request tới nó cho tới khi bật lại.",
	},
}

var usageAuthMethodTextKeys = map[string]string{
	"idc":          "auth_idc",
	"builder-id":   "auth_builder_id",
	"api_key":      "auth_api_key",
	"external_idp": "auth_external_idp",
	"imported":     "auth_imported",
}

// resolveUsageTheme and resolveUsageLang accept only known values: the page is
// embedded by the management panel, and an unvalidated value would reach the
// rendered document as an attribute.
func resolveUsageTheme(value string) string {
	if strings.EqualFold(strings.TrimSpace(value), usageThemeLight) {
		return usageThemeLight
	}
	return usageThemeDark
}

func resolveUsageLang(value string) string {
	normalized := strings.ToLower(strings.TrimSpace(value))
	if normalized == usageLangVI || strings.HasPrefix(normalized, usageLangVI+"-") {
		return usageLangVI
	}
	return usageLangEN
}

func usageTexts(lang string) usagePageText {
	return usagePageTextPacks[resolveUsageLang(lang)]
}

// newUsageNonce returns a per-response CSP nonce so the page keeps
// default-src 'none' while still running its own countdown script.
func newUsageNonce() string {
	random := make([]byte, 16)
	if _, err := io.ReadFull(cryptorand.Reader, random); err != nil {
		panic(fmt.Sprintf("generate Kiro usage nonce: %v", err))
	}
	return base64.RawURLEncoding.EncodeToString(random)
}

type usagePageOptions struct {
	Theme string
	Lang  string
	Nonce string
}

type usagePageSummary struct {
	Accounts  int
	Reporting int
	Attention int
}

// usagePageTotals adds the same-named figure across every account, so the page
// answers "how much of everything is left" without the reader adding columns in
// their head. Unit and Currency are only carried when every reporting account
// agrees on them: summing credits with dollars would produce a number that
// looks right and means nothing.
type usagePageTotals struct {
	Accounts       int
	Used           float64
	Limit          float64
	Remaining      float64
	Percent        float64
	Overage        float64
	OverageCap     float64
	OverageCharges float64
	// The granted pools are kept apart from the plan ceiling. Adding a trial
	// allowance or a bonus grant into Limit would state a ceiling nobody has.
	TrialUsed     float64
	TrialLimit    float64
	BonusTotal    float64
	OverageCredit float64
	Unit          string
	Currency      string
	MixedUnits    bool
	HasFigures    bool
}

type usagePageView struct {
	Accounts    []usageAccountView
	Empty       bool
	Summary     usagePageSummary
	Totals      usagePageTotals
	Options     usagePageOptions
	GeneratedAt string
	// ActionPath is the absolute resource path of the enable/disable action.
	// Empty hides the toggle, as on a page rendered outside the host.
	ActionPath string
}

// usageAccountCell carries one account together with the page it renders on,
// because the account cell needs the page's options to build its links.
type usageAccountCell struct {
	Account usageAccountView
	View    usagePageView
}

// newUsageTotals folds every bucket of every account into one row.
func newUsageTotals(accounts []usageAccountView) usagePageTotals {
	totals := usagePageTotals{}
	for _, account := range accounts {
		if len(account.Buckets) == 0 {
			continue
		}
		totals.Accounts++
		for _, bucket := range account.Buckets {
			// A row that is not the plan ceiling adds to its own pool only. An
			// empty Kind is a plan bucket: that is what a caller building a view
			// by hand means, and what every stored view carried before the pools
			// became their own rows.
			switch bucket.Kind {
			case usageKindTrial:
				totals.TrialUsed += bucket.Used
				totals.TrialLimit += bucket.Limit
				continue
			case usageKindBonus:
				totals.BonusTotal += bucket.Limit
				continue
			case usageKindOverageCredit:
				totals.OverageCredit += bucket.Limit
				continue
			}
			totals.HasFigures = true
			totals.Used += bucket.Used
			totals.Limit += bucket.Limit
			totals.Remaining += bucket.Remaining
			totals.Overage += bucket.Overage
			totals.OverageCap += bucket.OverageCap
			totals.OverageCharges += bucket.OverageCharges
			// The pools are summed from their own rows above, never from the copy
			// the plan bucket keeps for its detail block, or a granted credit
			// would be counted twice.
			if unit := strings.TrimSpace(bucket.Unit); unit != "" {
				switch {
				case totals.Unit == "":
					totals.Unit = unit
				case totals.Unit != unit:
					totals.MixedUnits = true
				}
			}
			if currency := strings.TrimSpace(bucket.Currency); currency != "" && totals.Currency == "" {
				totals.Currency = currency
			}
		}
	}
	if totals.MixedUnits {
		totals.Unit = ""
	}
	if totals.Limit > 0 {
		totals.Percent = math.Max(0, math.Min(100, totals.Used/totals.Limit*100))
	}
	return totals
}

// normalizeUsageBuckets fills in what a bucket built before the credit pools
// existed, or built by hand, leaves empty: an unnamed kind is the plan ceiling,
// and a plan ceiling always has a share and a known amount. The account and its
// bucket slice are copied, because the views this runs on are cached per
// credential and must not be rewritten under another request.
func normalizeUsageBuckets(accounts []usageAccountView) []usageAccountView {
	normalized := make([]usageAccountView, 0, len(accounts))
	for _, account := range accounts {
		if len(account.Buckets) > 0 {
			buckets := make([]usageBucketView, len(account.Buckets))
			copy(buckets, account.Buckets)
			for index := range buckets {
				if strings.TrimSpace(buckets[index].Kind) == "" {
					buckets[index].Kind = usageKindPlan
					buckets[index].HasShare = true
					buckets[index].AmountKnown = true
				}
			}
			account.Buckets = buckets
		}
		normalized = append(normalized, account)
	}
	return normalized
}

func newUsagePageView(accounts []usageAccountView, options usagePageOptions, generatedAt string) usagePageView {
	options.Theme = resolveUsageTheme(options.Theme)
	options.Lang = resolveUsageLang(options.Lang)
	if strings.TrimSpace(options.Nonce) == "" {
		options.Nonce = newUsageNonce()
	}
	accounts = normalizeUsageBuckets(accounts)
	summary := usagePageSummary{Accounts: len(accounts)}
	for _, account := range accounts {
		switch {
		case usageNeedsAttention(account):
			summary.Attention++
		case len(account.Buckets) > 0:
			summary.Reporting++
		}
	}
	return usagePageView{
		Accounts:    accounts,
		Empty:       len(accounts) == 0,
		Summary:     summary,
		Totals:      newUsageTotals(accounts),
		Options:     options,
		GeneratedAt: generatedAt,
	}
}

// renderUsagePage binds the language-dependent template helpers to this
// response, so one cached account view renders correctly in either language.
func renderUsagePage(view usagePageView) ([]byte, error) {
	text := usageTexts(view.Options.Lang)
	document, err := usagePageTemplate.Clone()
	if err != nil {
		return nil, fmt.Errorf("clone Kiro usage template: %w", err)
	}
	document = document.Funcs(usagePageFuncs(text))
	var page strings.Builder
	if err := document.Execute(&page, view); err != nil {
		return nil, fmt.Errorf("render Kiro usage page: %w", err)
	}
	return []byte(page.String()), nil
}

func usagePageFuncs(text usagePageText) template.FuncMap {
	return template.FuncMap{
		"formatNumber": formatUsageNumber,
		// percentClass names the reading, so colour is a second signal on top of the
		// figure rather than the only one; a share at or over the limit is a
		// different fact from one merely close to it.
		"percentClass": func(percent float64) string {
			switch {
			case percent >= 100:
				return "full"
			case percent >= 80:
				return "high"
			default:
				return ""
			}
		},
		"text": text.get,
		"stateLabel": func(key string) string {
			if strings.TrimSpace(key) == "" {
				return text.get("state_" + usageStateActive)
			}
			return text.get("state_" + key)
		},
		"authMethodLabel": func(method string) string {
			if key, ok := usageAuthMethodTextKeys[strings.ToLower(strings.TrimSpace(method))]; ok {
				return text.get(key)
			}
			return method
		},
		// capabilityLabel turns the AWS enum into a word. An unknown value is
		// shown as AWS wrote it rather than dropped, so a new enum member is
		// visible instead of silently missing.
		"capabilityLabel": func(value string) string {
			switch strings.ToUpper(strings.TrimSpace(value)) {
			case "OVERAGE_CAPABLE", "UPGRADE_CAPABLE":
				return text.get("value_yes")
			case "OVERAGE_INCAPABLE", "UPGRADE_INCAPABLE":
				return text.get("value_no")
			case "PURCHASE":
				return text.get("value_purchase")
			case "MANAGE":
				return text.get("value_manage")
			default:
				return value
			}
		},
		"trialLabel": func(status string) string {
			switch strings.ToUpper(strings.TrimSpace(status)) {
			case "EXPIRED":
				return text.get("trial_expired")
			case "ACTIVE", "IN_PROGRESS":
				return text.get("trial_active")
			default:
				return status
			}
		},
		"perUnit": func(unit string) string {
			if strings.TrimSpace(unit) == "" {
				return ""
			}
			return strings.ReplaceAll(text.get("per_unit"), "{unit}", strings.ToLower(unit))
		},
		"accountCount": func(count int) string {
			return strings.ReplaceAll(text.get("total_accounts"), "{n}", fmt.Sprintf("%d", count))
		},
		// firstBucket returns the only bucket AWS reports, or nil so the row can
		// render dashes instead of zeros it never measured.
		"firstBucket": func(account usageAccountView) *usageBucketView {
			if len(account.Buckets) == 0 {
				return nil
			}
			return &account.Buckets[0]
		},
		"lower":          strings.ToLower,
		"displayName":    usageDisplayName,
		"needsAttention": usageNeedsAttention,
		// isAddress keeps the account fact to a real address AWS reported; a bare
		// user key says nothing a reader can recognise.
		"isAddress": looksLikeEmail,
		// The panel is reached through Cloudflare, whose Email Address
		// Obfuscation rewrites every address in an HTML page to "[email protected]"
		// and injects a decoder script. This page's CSP runs only its own
		// nonce script, so the decoder never ran and every note read
		// "[email protected]" (reported 2026-09-25). The email_off markers are
		// Cloudflare's per-page opt-out. They are returned as template.HTML
		// because html/template drops comments written in the template.
		"emailOff": func() template.HTML { return "<!--email_off-->" },
		"emailOn":  func() template.HTML { return "<!--/email_off-->" },
		"cell": func(account usageAccountView, view usagePageView) usageAccountCell {
			return usageAccountCell{Account: account, View: view}
		},
		// bucketName localises the pool rows this code creates and leaves the plan
		// row named the way AWS named it.
		"bucketName": func(bucket usageBucketView) string {
			if key := strings.TrimSpace(bucket.NameKey); key != "" {
				return text.get(key)
			}
			if name := strings.TrimSpace(bucket.Name); name != "" {
				return name
			}
			return text.get("quota_plan")
		},
		"grantsCount": func(count int) string {
			return strings.ReplaceAll(text.get("grants_count"), "{n}", fmt.Sprintf("%d", count))
		},
		"errorMessage": func(account usageAccountView) string {
			if strings.TrimSpace(account.ErrorKey) != "" {
				return text.get(account.ErrorKey)
			}
			return account.Error
		},
	}
}
