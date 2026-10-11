package index

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/pragmataW/graphmaster/internal/config"
	"github.com/pragmataW/graphmaster/internal/model"
	"github.com/pragmataW/graphmaster/internal/store"
)

// Status summarizes the index for `graphmaster status` and the MCP status
// tool. It reads the database only; it does not freshen.
type Status struct {
	Root  string
	NoGit bool
	Mode  config.Mode
	// State is "ready", "building" or "" (never indexed); Progress is the
	// build percentage while building.
	State    string
	Progress int
	// Files counts files rows by status; Langs counts indexed files
	// (ok or parse_err) by language.
	Files map[model.FileStatus]int64
	Langs map[model.Lang]int64
	store.Counts
	Pending    int64 // names, files and dependents waiting for resolution
	CacheBytes int64 // cache.db on disk
	LastScan   time.Time
}

// Status collects index statistics.
func (ix *Indexer) Status(ctx context.Context) (*Status, error) {
	st := ix.Store()
	s := &Status{
		Root:  ix.loc.Root.WorkTree,
		NoGit: ix.loc.NoGit,
		Mode:  ix.cfg.Mode,
		Files: map[model.FileStatus]int64{},
		Langs: map[model.Lang]int64{},
	}
	var err error
	if s.Counts, err = st.Counts(ctx); err != nil {
		return nil, err
	}
	rows, err := st.DB().QueryContext(ctx, `SELECT status, lang, count(*) FROM files GROUP BY status, lang`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var status, lang int
		var n int64
		if err := rows.Scan(&status, &lang, &n); err != nil {
			return nil, err
		}
		fs := model.FileStatus(uint8(status))
		s.Files[fs] += n
		if fs.Indexed() {
			s.Langs[model.Lang(uint8(lang))] += n
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := st.DB().QueryRowContext(ctx, `SELECT (SELECT count(*) FROM pending_names) +
		(SELECT count(*) FROM meta WHERE (key >= ?1 AND key < ?2) OR (key >= ?3 AND key < ?4) OR (key >= ?5 AND key < ?6))`,
		metaPendingFile, metaPendingFile+"\xff", metaPendingDependents, metaPendingDependents+"\xff",
		metaPendingRemoved, metaPendingRemoved+"\xff").Scan(&s.Pending); err != nil {
		return nil, err
	}
	s.State, _, _ = st.Meta(ctx, store.MetaState)
	if v, ok, _ := st.Meta(ctx, store.MetaProgress); ok {
		s.Progress, _ = strconv.Atoi(v)
	}
	if v, ok, _ := st.Meta(ctx, store.MetaLastScanNS); ok {
		if ns, err := strconv.ParseInt(v, 10, 64); err == nil {
			s.LastScan = time.Unix(0, ns)
		}
	}
	for _, suffix := range []string{"", "-wal"} {
		if fi, err := os.Stat(filepath.Join(ix.loc.Dir, store.CacheDBName+suffix)); err == nil {
			s.CacheBytes += fi.Size()
		}
	}
	return s, nil
}

// Format renders the status as plain text.
func (s *Status) Format() string {
	var b strings.Builder
	fmt.Fprintf(&b, "repository: %s", s.Root)
	if s.NoGit {
		b.WriteString(" (not git)")
	}
	b.WriteByte('\n')
	state := s.State
	switch state {
	case "":
		state = "not indexed"
	case store.StateBuilding:
		state = fmt.Sprintf("building %d%%", s.Progress)
	}
	fmt.Fprintf(&b, "state: %s\nmode: %s\n", state, s.Mode)
	if s.LastScan.IsZero() {
		b.WriteString("last scan: never\n")
	} else {
		fmt.Fprintf(&b, "last scan: %s (%s ago)\n", s.LastScan.Format(time.RFC3339),
			time.Since(s.LastScan).Round(time.Second))
	}
	var total int64
	for _, n := range s.Files {
		total += n
	}
	fmt.Fprintf(&b, "files: %d", total)
	statuses := make([]model.FileStatus, 0, len(s.Files))
	for fs := range s.Files {
		statuses = append(statuses, fs)
	}
	sort.Slice(statuses, func(i, j int) bool { return statuses[i] < statuses[j] })
	for i, fs := range statuses {
		sep := ", "
		if i == 0 {
			sep = " ("
		}
		fmt.Fprintf(&b, "%s%s %d", sep, fs, s.Files[fs])
	}
	if len(statuses) > 0 {
		b.WriteByte(')')
	}
	b.WriteByte('\n')
	langs := make([]model.Lang, 0, len(s.Langs))
	for l := range s.Langs {
		langs = append(langs, l)
	}
	sort.Slice(langs, func(i, j int) bool {
		return s.Langs[langs[i]] > s.Langs[langs[j]] || (s.Langs[langs[i]] == s.Langs[langs[j]] && langs[i] < langs[j])
	})
	b.WriteString("languages:")
	if len(langs) == 0 {
		b.WriteString(" none")
	}
	for _, l := range langs {
		fmt.Fprintf(&b, " %s %d", l, s.Langs[l])
	}
	b.WriteByte('\n')
	fmt.Fprintf(&b, "symbols: %d, edges: %d (%d unresolved), pending names: %d\n",
		s.Nodes, s.Edges, s.Unresolved, s.Pending)
	fmt.Fprintf(&b, "db size: %s (cache %s)\n", humanBytes(s.DBBytes), humanBytes(s.CacheBytes))
	return b.String()
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
