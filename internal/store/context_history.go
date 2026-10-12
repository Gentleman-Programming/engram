package store

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"time"
)

type historyPeriod struct {
	label      string
	start, end time.Time
	samples    int
	monthly    bool
}
type historyGroup struct {
	label   string
	count   int
	types   []string
	samples []string
}

func historyPeriods(now time.Time) []historyPeriod {
	now = now.UTC()
	month := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	next := month.AddDate(0, 1, 0)
	var periods []historyPeriod
	for start := month; start.Before(next); {
		days := (8 - int(start.Weekday())) % 7
		if days == 0 {
			days = 7
		}
		end := start.AddDate(0, 0, days)
		if end.After(next) {
			end = next
		}
		n := 1
		if !now.Before(start) && now.Before(end) {
			n = 3
		}
		periods = append(periods, historyPeriod{label: start.Format("2006-01-02") + "–" + end.AddDate(0, 0, -1).Format("2006-01-02"), start: start, end: end, samples: n})
		start = end
	}
	quarterMonth := time.Month((int(now.Month())-1)/3*3 + 1)
	quarter := time.Date(now.Year(), quarterMonth, 1, 0, 0, 0, 0, time.UTC)
	for start := quarter; start.Before(month); start = start.AddDate(0, 1, 0) {
		periods = append(periods, historyPeriod{label: start.Format("2006-01"), start: start, end: start.AddDate(0, 1, 0), monthly: true})
	}
	for start := time.Date(now.Year(), 1, 1, 0, 0, 0, 0, time.UTC); start.Before(quarter); start = start.AddDate(0, 3, 0) {
		periods = append(periods, historyPeriod{label: fmt.Sprintf("%d Q%d", now.Year(), (int(start.Month())-1)/3+1), start: start, end: start.AddDate(0, 3, 0)})
	}
	sort.Slice(periods, func(i, j int) bool { return periods[i].start.After(periods[j].start) })
	return periods
}

// The transaction pins recent membership, aggregates and bounded title reads to
// one SQLite snapshot. Old content bodies are never selected.
func (s *Store) contextHistory(project, scope string, limit int, now time.Time) ([]Observation, []historyGroup, error) {
	tx, err := s.db.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, nil, err
	}
	// Cleanup is best effort; query and commit results remain authoritative.
	defer func() { _ = tx.Rollback() }()
	project, _ = NormalizeProject(project)
	filter := "deleted_at IS NULL AND pinned = 0"
	args := []any{}
	if project != "" {
		filter += " AND LOWER(project) = ?"
		args = append(args, project)
	}
	if scope != "" {
		filter += " AND scope = ?"
		args = append(args, normalizeScope(scope))
	}
	if limit <= 0 {
		limit = s.cfg.MaxContextResults
	}
	recentSQL := "SELECT " + observationSelectColumns + " FROM observations WHERE " + filter + " ORDER BY datetime(created_at) DESC, id DESC LIMIT ?"
	recentArgs := append(append([]any{}, args...), limit)
	rows, err := tx.Query(recentSQL, recentArgs...)
	if err != nil {
		return nil, nil, err
	}
	var recent []Observation
	for rows.Next() {
		var o Observation
		if err = scanObservationRow(rows, &o); err != nil {
			_ = rows.Close()
			return nil, nil, err
		}
		recent = append(recent, o)
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return nil, nil, err
	}
	if s.contextHistoryAfterRead != nil {
		s.contextHistoryAfterRead("recent")
	}
	base := historyBaseSQL(filter)
	baseArgs := append(append([]any{}, recentArgs...), args...)
	periods := historyPeriods(now)
	aggregateSQL, queryArgs := historyAggregateSQL(base, baseArgs, periods, now)
	rows, err = tx.Query(aggregateSQL, queryArgs...)
	if err != nil {
		return nil, nil, err
	}
	groups := map[string]*historyGroup{}
	for rows.Next() {
		var label, kind string
		var count int
		if err = rows.Scan(&label, &kind, &count); err != nil {
			_ = rows.Close()
			return nil, nil, err
		}
		g := groups[label]
		if g == nil {
			g = &historyGroup{label: label}
			groups[label] = g
		}
		g.count += count
		g.types = append(g.types, fmt.Sprintf("%s: %d", historyLine(kind), count))
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return nil, nil, err
	}
	if s.contextHistoryAfterRead != nil {
		s.contextHistoryAfterRead("counts")
	}
	var result []historyGroup
	for _, p := range periods {
		g := groups[p.label]
		if g == nil {
			continue
		}
		if !p.monthly {
			g.types = nil
		}
		if p.samples > 0 {
			sampleArgs := append(append([]any{}, baseArgs...), p.start.Format(time.RFC3339), p.end.Format(time.RFC3339), p.samples)
			rows, err = tx.Query(historySampleSQL(base), sampleArgs...)
			if err != nil {
				return nil, nil, err
			}
			for rows.Next() {
				var id int64
				var title string
				if err = rows.Scan(&id, &title); err != nil {
					_ = rows.Close()
					return nil, nil, err
				}
				g.samples = append(g.samples, fmt.Sprintf("  - Sample #%d: %s\n", id, historyLine(title)))
			}
			err = rows.Err()
			_ = rows.Close()
			if err != nil {
				return nil, nil, err
			}
		}
		result = append(result, *g)
		delete(groups, p.label)
	}
	var years []string
	for label := range groups {
		years = append(years, label)
	}
	sort.Sort(sort.Reverse(sort.StringSlice(years)))
	for _, year := range years {
		g := groups[year]
		g.types = nil
		result = append(result, *g)
	}
	if err = tx.Commit(); err != nil {
		return nil, nil, err
	}
	return recent, result, nil
}

// Share the exact production SQL with EXPLAIN tests. Recent membership stays
// tied to the same transaction as counts and bounded sample queries.
func historyBaseSQL(filter string) string {
	return "WITH recent AS (SELECT id FROM observations WHERE " + filter + " ORDER BY datetime(created_at) DESC,id DESC LIMIT ?), old AS (SELECT id,type,title,created_at FROM observations WHERE " + filter + " AND id NOT IN (SELECT id FROM recent)) "
}

func historyAggregateSQL(base string, baseArgs []any, periods []historyPeriod, now time.Time) (string, []any) {
	// Most long-lived history predates this year. Resolve those rows before
	// testing every finer-grained calendar interval; the intervals are disjoint.
	expr := "CASE WHEN datetime(created_at) < datetime(?) THEN strftime('%Y',created_at)"
	args := append(append([]any{}, baseArgs...), time.Date(now.UTC().Year(), 1, 1, 0, 0, 0, 0, time.UTC).Format(time.RFC3339))
	for _, p := range periods {
		expr += " WHEN datetime(created_at) >= datetime(?) AND datetime(created_at) < datetime(?) THEN ?"
		args = append(args, p.start.Format(time.RFC3339), p.end.Format(time.RFC3339), p.label)
	}
	expr += " END"
	return base + "SELECT " + expr + " AS period,type,count(*) FROM old GROUP BY period,type HAVING period IS NOT NULL ORDER BY period DESC,type", args
}

func historySampleSQL(base string) string {
	return base + "SELECT id,title FROM old WHERE datetime(created_at)>=datetime(?) AND datetime(created_at)<datetime(?) ORDER BY datetime(created_at) DESC,id DESC LIMIT ?"
}

func historyLine(s string) string { return strings.Join(strings.Fields(s), " ") }

func renderContextHistory(groups []historyGroup, budget int) string {
	const header = "### Older History\nSamples are titles, not summaries; use mem_get_observation with an ID to drill in.\n"
	const omitted = "- [older groups omitted]\n"
	if len(groups) == 0 || budget <= len(header) {
		return ""
	}
	var lines []string
	for _, g := range groups {
		line := fmt.Sprintf("- %s: %d observations", g.label, g.count)
		if len(g.types) > 0 {
			line += " (" + strings.Join(g.types, ", ") + ")"
		}
		lines = append(lines, line+"\n")
		lines = append(lines, g.samples...)
	}
	complete := header + strings.Join(lines, "") + "\n"
	if len(complete) <= budget {
		return complete
	}
	out := header
	useful := false
	for i, line := range lines {
		reserve := 1
		if i < len(lines)-1 {
			reserve += len(omitted)
		}
		if len(out)+len(line)+reserve > budget {
			if useful && len(out)+len(omitted)+1 <= budget {
				out += omitted
			}
			break
		}
		out += line
		useful = true
	}
	if !useful {
		return ""
	}
	return out + "\n"
}

func positiveContextRemainder(maxBytes, reserved int) int {
	if maxBytes <= 0 {
		return 0
	}
	return maxBytes - reserved
}
