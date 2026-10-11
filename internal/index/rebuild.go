package index

import (
	"os"
	"sort"
	"strconv"

	"github.com/pragmataW/graphmaster/internal/model"
	"github.com/pragmataW/graphmaster/internal/store"
)

// EnvCrashAfterBatch makes the indexer exit abruptly right after the given
// number of writer commits. Tests only: it simulates a kill mid-build.
const EnvCrashAfterBatch = "GRAPHMASTER_TEST_CRASH_AFTER_BATCH"

// crashExitCode is the exit status of a simulated crash.
const crashExitCode = 86

func crashAfterFromEnv() int {
	n, err := strconv.Atoi(os.Getenv(EnvCrashAfterBatch))
	if err != nil || n <= 0 {
		return 0
	}
	return n
}

// resetGraphSQL empties the derived graph while keeping the database file,
// so readers in other processes stay valid. names (append-only), dirs
// (directory cache) and steering tables survive.
var resetGraphSQL = []string{
	`DELETE FROM edges`,
	`DELETE FROM nodes`,
	`DELETE FROM imports`,
	`DELETE FROM files`,
	`DELETE FROM packages`,
	`DELETE FROM pending_names`,
	`INSERT INTO sym_fts(sym_fts) VALUES('delete-all')`,
	`INSERT INTO sym_tri(sym_tri) VALUES('delete-all')`,
	`DELETE FROM meta WHERE key >= 'dep:' AND key < 'dep;'`,
	`DELETE FROM dep_owners`,
	`DELETE FROM meta WHERE key >= '` + metaPendingFile + `' AND key < 'pending_file;'`,
	`DELETE FROM meta WHERE key >= '` + metaPendingDependents + `' AND key < 'pending_dependents;'`,
	`DELETE FROM meta WHERE key >= '` + metaPendingRemoved + `' AND key < 'pending_removed;'`,
	`DELETE FROM meta WHERE key >= '` + store.MetaExtractorVerPf + `' AND key < 'extractor_ver;'`,
}

// resetGraph wipes the graph inside w's batch and records the
// configuration the rebuild runs with, so a crash mid-rebuild resumes
// incrementally instead of starting over.
func resetGraph(w *store.Writer, p *plan) error {
	for _, q := range resetGraphSQL {
		if _, err := w.Exec(q); err != nil {
			return err
		}
	}
	return writeVersionMeta(w, p)
}

// writeVersionMeta stores the extractor versions and configuration
// signature the graph now reflects.
func writeVersionMeta(w *store.Writer, p *plan) error {
	for _, l := range model.AllLangs() {
		key := store.MetaExtractorVerPf + l.String()
		if v := p.xvers[l]; v > 0 {
			if err := w.SetMeta(key, strconv.Itoa(v)); err != nil {
				return err
			}
		} else if _, err := w.Exec(`DELETE FROM meta WHERE key = ?`, key); err != nil {
			return err
		}
	}
	return w.SetMeta(metaConfigSig, p.cfgSig)
}

// persistDeps stores the dependencies and owner attributions rc recorded
// since the previous call (see dep_owners in the store schema).
func persistDeps(w *store.Writer, rc *RepoCtx) error {
	if deps := rc.TakeNewDeps(); len(deps) > 0 {
		keys := make([]string, 0, len(deps))
		for k := range deps {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if err := w.SetMeta(metaDepPrefix+k, deps[k]); err != nil {
				return err
			}
		}
	}
	for _, o := range rc.TakeNewOwners() {
		var err error
		if o.Owner == "" {
			_, err = w.Exec(`INSERT OR IGNORE INTO dep_owners(key, file_id) VALUES(?, 0)`, o.Key)
		} else {
			_, err = w.Exec(`INSERT OR IGNORE INTO dep_owners(key, file_id)
				SELECT ?, id FROM files WHERE path = ?`, o.Key, o.Owner)
		}
		if err != nil {
			return err
		}
	}
	return nil
}
