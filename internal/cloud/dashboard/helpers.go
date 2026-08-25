package dashboard

import (
	"fmt"
	"html"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Gentleman-Programming/engram/internal/cloud/cloudstore"
	"github.com/Gentleman-Programming/engram/internal/timeutil"
)

// ─── Pagination ─────────────────────────────────────────────────────────────

const (
	defaultPageSize          = 10
	minPageSize              = 10
	maxPageSize              = 100
	maxPills                 = 8
	defaultDashboardHomePath = "/dashboard/"
)

// allowedPageSizes are the page size options shown in the UI selector.
var allowedPageSizes = []int{10, 25, 50, 100}

// Pagination holds everything a templ component needs to render page controls.
type Pagination struct {
	Page       int // current page (1-indexed)
	PageSize   int // items per page
	TotalItems int // total rows from COUNT query
	TotalPages int // computed from TotalItems/PageSize
}

// Offset returns the SQL OFFSET value for the current page.
func (p Pagination) Offset() int {
	return (p.Page - 1) * p.PageSize
}

// HasPrev returns true if there is a previous page.
func (p Pagination) HasPrev() bool { return p.Page > 1 }

// HasNext returns true if there is a next page.
func (p Pagination) HasNext() bool { return p.Page < p.TotalPages }

// ShowPagination returns true if the result set exceeds one page.
func (p Pagination) ShowPagination() bool { return p.TotalPages > 1 }

// Start returns the 1-indexed position of the first item on this page.
func (p Pagination) Start() int {
	if p.TotalItems == 0 {
		return 0
	}
	return p.Offset() + 1
}

// End returns the 1-indexed position of the last item on this page.
func (p Pagination) End() int {
	end := p.Page * p.PageSize
	if end > p.TotalItems {
		end = p.TotalItems
	}
	return end
}

// PageNumbers returns a slice of page numbers to render, with -1 as ellipsis.
func (p Pagination) PageNumbers() []int {
	if p.TotalPages <= 7 {
		pages := make([]int, p.TotalPages)
		for i := range pages {
			pages[i] = i + 1
		}
		return pages
	}
	// Show: 1 ... (current-1) current (current+1) ... last
	pages := []int{1}
	if p.Page > 3 {
		pages = append(pages, -1)
	}
	for i := p.Page - 1; i <= p.Page+1; i++ {
		if i > 1 && i < p.TotalPages {
			pages = append(pages, i)
		}
	}
	if p.Page < p.TotalPages-2 {
		pages = append(pages, -1)
	}
	pages = append(pages, p.TotalPages)
	return pages
}

// parsePagination extracts page/pageSize from the request query params.
func parsePagination(r *http.Request, totalItems int) Pagination {
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	pageSize, _ := strconv.Atoi(r.URL.Query().Get("pageSize"))

	if pageSize < minPageSize {
		pageSize = defaultPageSize
	}
	if pageSize > maxPageSize {
		pageSize = maxPageSize
	}

	totalPages := totalItems / pageSize
	if totalItems%pageSize > 0 {
		totalPages++
	}
	if totalPages < 1 {
		totalPages = 1
	}

	if page < 1 {
		page = 1
	}
	if page > totalPages {
		page = totalPages
	}

	return Pagination{
		Page:       page,
		PageSize:   pageSize,
		TotalItems: totalItems,
		TotalPages: totalPages,
	}
}

// parsePaginationRaw extracts page/pageSize without clamping page to a totalPages
// computed from a potentially-unknown total. Use this when the real total is not yet
// known (e.g. before the first store call). Call reclampPagination after the store
// returns the real total.
func parsePaginationRaw(r *http.Request) (page, pageSize int) {
	page, _ = strconv.Atoi(r.URL.Query().Get("page"))
	pageSize, _ = strconv.Atoi(r.URL.Query().Get("pageSize"))
	if pageSize < minPageSize {
		pageSize = defaultPageSize
	}
	if pageSize > maxPageSize {
		pageSize = maxPageSize
	}
	if page < 1 {
		page = 1
	}
	return page, pageSize
}

// reclampPagination computes the correct Pagination after the real total is known.
// If the requested page exceeds the real totalPages, it clamps to totalPages.
// Returns the clamped Pagination and whether the page was clamped (i.e. a re-fetch is needed).
func reclampPagination(page, pageSize, totalItems int) (Pagination, bool) {
	totalPages := totalItems / pageSize
	if totalItems%pageSize > 0 {
		totalPages++
	}
	if totalPages < 1 {
		totalPages = 1
	}
	clamped := page
	if clamped > totalPages {
		clamped = totalPages
	}
	return Pagination{
		Page:       clamped,
		PageSize:   pageSize,
		TotalItems: totalItems,
		TotalPages: totalPages,
	}, clamped != page
}

// paginationURL builds a URL with page, pageSize, and extra params preserved.
func paginationURL(base string, page, pageSize int, extra map[string]string) string {
	v := url.Values{}
	for k, val := range extra {
		if val != "" {
			v.Set(k, val)
		}
	}
	v.Set("page", strconv.Itoa(page))
	v.Set("pageSize", strconv.Itoa(pageSize))
	return fmt.Sprintf("%s?%s", base, v.Encode())
}

// totalStatSessions returns the sum of Sessions across all project rows.
// ADAPTED: replaces totalSessionCount([]ProjectStat) from legacy helpers.
func totalStatSessions(stats []cloudstore.DashboardProjectRow) int {
	total := 0
	for _, s := range stats {
		total += s.Sessions
	}
	return total
}

// totalStatObservations returns the sum of Observations across all project rows.
func totalStatObservations(stats []cloudstore.DashboardProjectRow) int {
	total := 0
	for _, s := range stats {
		total += s.Observations
	}
	return total
}

// totalStatPrompts returns the sum of Prompts across all project rows.
func totalStatPrompts(stats []cloudstore.DashboardProjectRow) int {
	total := 0
	for _, s := range stats {
		total += s.Prompts
	}
	return total
}

func browserURL(project, search, obsType string) string {
	v := url.Values{}
	if project != "" {
		v.Set("project", project)
	}
	if search != "" {
		v.Set("q", search)
	}
	if obsType != "" {
		v.Set("type", obsType)
	}
	if encoded := v.Encode(); encoded != "" {
		return fmt.Sprintf("/dashboard/browser?%s", encoded)
	}
	return "/dashboard/browser"
}

func typePillClass(activeType, candidate string) string {
	if activeType == candidate {
		return "type-pill active"
	}
	if activeType == "" && candidate == "" {
		return "type-pill active"
	}
	return "type-pill"
}

// dashboardTimestampLayout is the human-friendly layout shown on dashboard cards
// (e.g. "22 May 2026 11:31"). Storage stays UTC; only the rendered string changes.
const dashboardTimestampLayout = "02 Jan 2006 15:04"

// formatTimestamp renders a stored UTC timestamp for dashboard display. It accepts
// both RFC3339 and SQLite-style values ("YYYY-MM-DD HH:MM:SS") and honors
// ENGRAM_TIMEZONE for the display zone, falling back to system local time.
//
// Returns "-" for an empty string. If the value cannot be parsed in any supported
// layout, the original string is returned unchanged so we never lose data on
// malformed timestamps.
func formatTimestamp(ts string) string {
	ts = strings.TrimSpace(ts)
	if ts == "" {
		return "-"
	}
	return timeutil.FormatLocalWithLayout(ts, dashboardTimestampLayout)
}

// ADAPTED: legacy had formatTimestampPtr(*string); integrated uses string fields.
// Cards that show "last seen" or similar nullable values render "Never" instead of "-".
func formatTimestampStr(ts string) string {
	if ts == "" {
		return "Never"
	}
	return formatTimestamp(ts)
}

// countPausedProjects counts how many controls have SyncEnabled=false.
// ADAPTED: cloudstore.ProjectSyncControl -> cloudstore.ProjectSyncControl (same name, new file).
func countPausedProjects(controls []cloudstore.ProjectSyncControl) int {
	count := 0
	for _, control := range controls {
		if !control.SyncEnabled {
			count++
		}
	}
	return count
}

// controlsByProject returns a project->control lookup map.
func controlsByProject(controls []cloudstore.ProjectSyncControl) map[string]cloudstore.ProjectSyncControl {
	indexed := make(map[string]cloudstore.ProjectSyncControl, len(controls))
	for _, control := range controls {
		indexed[control.Project] = control
	}
	return indexed
}

// projectControlReasonValue returns the pause reason or empty string.
func projectControlReasonValue(control cloudstore.ProjectSyncControl) string {
	if control.PausedReason == nil {
		return ""
	}
	return *control.PausedReason
}

// projectControl looks up a control for a project, defaulting to enabled.
func projectControl(controls map[string]cloudstore.ProjectSyncControl, project string) cloudstore.ProjectSyncControl {
	if controls == nil {
		return cloudstore.ProjectSyncControl{Project: project, SyncEnabled: true}
	}
	control, ok := controls[project]
	if !ok {
		return cloudstore.ProjectSyncControl{Project: project, SyncEnabled: true}
	}
	return control
}

// truncateContent truncates a string to max runes, appending "..." if needed.
func truncateContent(s string, max int) string {
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	runes := []rune(s)
	return string(runes[:max]) + "..."
}

var structuredFieldRE = regexp.MustCompile(`\*\*(What|Why|Where|Learned)\*\*:\s*`)
var headingSectionRE = regexp.MustCompile(`(?m)^##\s+([^\n#]+?)\s*$`)

func renderStructuredContent(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "<p class=\"muted\">No content captured.</p>"
	}
	if !structuredFieldRE.MatchString(raw) {
		if headingSectionRE.MatchString(raw) {
			return renderHeadingSections(raw)
		}
		return renderParagraphBlocks(raw)
	}

	parts := structuredFieldRE.Split(raw, -1)
	matches := structuredFieldRE.FindAllStringSubmatch(raw, -1)
	if len(matches) == 0 {
		return renderParagraphBlocks(raw)
	}

	var b strings.Builder
	b.WriteString(`<div class="structured-content">`)
	for i, match := range matches {
		if len(match) < 2 {
			continue
		}
		value := ""
		if i+1 < len(parts) {
			value = strings.TrimSpace(parts[i+1])
		}
		if value == "" {
			continue
		}
		b.WriteString(`<section class="structured-block">`)
		b.WriteString(`<h4>` + html.EscapeString(match[1]) + `</h4>`)
		b.WriteString(renderParagraphBlocks(value))
		b.WriteString(`</section>`)
	}
	b.WriteString(`</div>`)
	return b.String()
}

func renderInlineStructuredPreview(raw string, max int) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "No content captured."
	}
	if structuredFieldRE.MatchString(raw) {
		labels := structuredFieldRE.FindAllStringSubmatch(raw, -1)
		parts := structuredFieldRE.Split(raw, -1)
		chunks := make([]string, 0, len(labels))
		for i, label := range labels {
			if len(label) < 2 {
				continue
			}
			value := ""
			if i+1 < len(parts) {
				value = strings.TrimSpace(parts[i+1])
			}
			if value == "" {
				continue
			}
			chunks = append(chunks, label[1]+": "+normalizeWhitespace(value))
		}
		raw = strings.Join(chunks, " • ")
	} else if headingSectionRE.MatchString(raw) {
		raw = flattenHeadingSections(raw)
	}
	return truncateContent(normalizeWhitespace(raw), max)
}

func renderHeadingSections(raw string) string {
	matches := headingSectionRE.FindAllStringSubmatchIndex(raw, -1)
	if len(matches) == 0 {
		return renderParagraphBlocks(raw)
	}
	var b strings.Builder
	b.WriteString(`<div class="structured-content">`)
	for i, match := range matches {
		label := strings.TrimSpace(raw[match[2]:match[3]])
		start := match[1]
		end := len(raw)
		if i+1 < len(matches) {
			end = matches[i+1][0]
		}
		value := strings.TrimSpace(raw[start:end])
		if value == "" {
			continue
		}
		b.WriteString(`<section class="structured-block">`)
		b.WriteString(`<h4>` + html.EscapeString(label) + `</h4>`)
		b.WriteString(renderParagraphBlocks(value))
		b.WriteString(`</section>`)
	}
	b.WriteString(`</div>`)
	return b.String()
}

func flattenHeadingSections(raw string) string {
	matches := headingSectionRE.FindAllStringSubmatchIndex(raw, -1)
	if len(matches) == 0 {
		return raw
	}
	chunks := make([]string, 0, len(matches))
	for i, match := range matches {
		label := strings.TrimSpace(raw[match[2]:match[3]])
		start := match[1]
		end := len(raw)
		if i+1 < len(matches) {
			end = matches[i+1][0]
		}
		value := strings.TrimSpace(raw[start:end])
		if value == "" {
			continue
		}
		chunks = append(chunks, label+": "+normalizeWhitespace(value))
	}
	return strings.Join(chunks, " • ")
}

func renderParagraphBlocks(raw string) string {
	blocks := strings.Split(strings.TrimSpace(raw), "\n\n")
	var b strings.Builder
	for _, block := range blocks {
		text := strings.TrimSpace(block)
		if text == "" {
			continue
		}
		b.WriteString(`<p>` + html.EscapeString(normalizeWhitespacePreserveParagraph(text)) + `</p>`)
	}
	if b.Len() == 0 {
		return `<p class="muted">No content captured.</p>`
	}
	return b.String()
}

func normalizeWhitespace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func normalizeWhitespacePreserveParagraph(s string) string {
	lines := strings.Split(s, "\n")
	cleaned := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		cleaned = append(cleaned, line)
	}
	return strings.Join(cleaned, " ")
}

// auditTimeValue formats a time.Time for display in the audit log filter form.
// Returns an RFC3339 string or empty string for zero values.
func auditTimeValue(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// buildAuditListURL constructs the /dashboard/admin/audit-log/list URL with
// active filter query params embedded. Used to forward deep-link filters into
// the initial HTMX hx-get attribute so that reloading the shell preserves filters.
// JW2 fix: deep-linking /dashboard/admin/audit-log?contributor=alice must
// propagate contributor=alice into the initial partial load.
func buildAuditListURL(filter cloudstore.AuditFilter) string {
	q := url.Values{}
	if c := strings.TrimSpace(filter.Contributor); c != "" {
		q.Set("contributor", c)
	}
	if p := strings.TrimSpace(filter.Project); p != "" {
		q.Set("project", p)
	}
	if o := strings.TrimSpace(filter.Outcome); o != "" {
		q.Set("outcome", o)
	}
	if !filter.OccurredAtFrom.IsZero() {
		q.Set("from", filter.OccurredAtFrom.UTC().Format(time.RFC3339))
	}
	if !filter.OccurredAtTo.IsZero() {
		q.Set("to", filter.OccurredAtTo.UTC().Format(time.RFC3339))
	}
	base := "/dashboard/admin/audit-log/list"
	if len(q) == 0 {
		return base
	}
	return base + "?" + q.Encode()
}

// formatTimeValue renders a time.Time value for dashboard display (managed
// user / token / grant timestamps come back as time.Time from cloudstore,
// unlike the string timestamps used by the sync-replicated dashboard rows).
// Returns "-" for a zero value.
func formatTimeValue(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return t.UTC().Format(dashboardTimestampLayout)
}

// formatTimePtr renders an optional time.Time pointer, returning "Never" when
// nil or zero (e.g. a token that has never been used).
func formatTimePtr(t *time.Time) string {
	if t == nil || t.IsZero() {
		return "Never"
	}
	return formatTimeValue(*t)
}

// emptyDash returns "-" for an empty/whitespace-only string, otherwise the
// trimmed original value. Used for optional managed-user/token fields.
func emptyDash(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "-"
	}
	return s
}

// managedUserToggleAction returns the enable/disable form action suffix for a
// managed user row, based on its current enabled state.
func managedUserToggleAction(enabled bool) string {
	if enabled {
		return "/disable"
	}
	return "/enable"
}

// typeBadgeVariant returns a badge color variant for an observation type.
func typeBadgeVariant(obsType string) string {
	switch obsType {
	case "decision", "architecture":
		return "success"
	case "bugfix":
		return "danger"
	case "discovery", "learning":
		return "warning"
	default:
		return "muted"
	}
}

// ─── URL helpers ─────────────────────────────────────────────────────────────

func safeQuery(urlPath string, rawQuery string) string {
	if strings.TrimSpace(rawQuery) == "" {
		return urlPath
	}
	// N1: url.ParseQuery + Encode normalises the query (re-encodes special chars
	// as percent-escapes) and avoids html.EscapeString turning '&' into '&amp;'
	// which would corrupt multi-param URLs embedded in href attributes.
	parsed, _ := url.ParseQuery(rawQuery)
	encoded := parsed.Encode()
	if encoded == "" {
		return urlPath
	}
	return urlPath + "?" + encoded
}

func preserveQuery(rawQuery string, key, value string) string {
	v, _ := url.ParseQuery(rawQuery)
	v.Set(key, value)
	return v.Encode()
}

func sanitizeDashboardNext(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if !strings.HasPrefix(raw, "/") || strings.HasPrefix(raw, "//") {
		return ""
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	if parsed.IsAbs() || parsed.Host != "" || parsed.Scheme != "" || parsed.User != nil {
		return ""
	}
	normalizedPath, ok := normalizeDashboardPath(parsed)
	if !ok {
		return ""
	}
	v := url.Values{}
	for key, values := range parsed.Query() {
		for _, value := range values {
			v.Add(key, value)
		}
	}
	rawQuery := v.Encode()
	if rawQuery == "" {
		return normalizedPath
	}
	return normalizedPath + "?" + rawQuery
}

func normalizeDashboardPath(parsed *url.URL) (string, bool) {
	escapedPath := parsed.EscapedPath()
	if strings.TrimSpace(escapedPath) == "" {
		escapedPath = parsed.Path
	}
	decodedPath, err := url.PathUnescape(escapedPath)
	if err != nil {
		return "", false
	}
	if !strings.HasPrefix(decodedPath, "/") {
		return "", false
	}
	cleaned := path.Clean(decodedPath)
	if cleaned == "." {
		cleaned = "/"
	}
	if cleaned != "/dashboard" && !strings.HasPrefix(cleaned, "/dashboard/") {
		return "", false
	}
	normalized := (&url.URL{Path: cleaned}).EscapedPath()
	if normalized == "" {
		return "", false
	}
	return normalized, true
}

func dashboardLoginPathWithNext(next string) string {
	next = sanitizeDashboardNext(next)
	if next == "" {
		return "/dashboard/login"
	}
	return "/dashboard/login?" + preserveQuery("", "next", next)
}

func dashboardPostLoginPath(next string) string {
	next = sanitizeDashboardNext(next)
	if next == "" {
		return defaultDashboardHomePath
	}
	return next
}
