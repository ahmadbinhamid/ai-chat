// Package migrator is the migration engine: the registry plus the run/fresh
// logic. It holds no migrations itself — the files in database/migrations
// register themselves here via init(). Keeping the engine separate means a
// future "squash" command can wipe database/migrations and drop in a single
// schema file without touching this package.
package migrator

import (
	"database/sql"
	"sort"
)

// Migration is one migration: its name plus the up/down functions.
type Migration struct {
	Name string
	Up   func(*sql.DB) error
	Down func(*sql.DB) error
}

// registry holds every migration that registered itself.
var registry []Migration

// Register adds a migration to the list. Each generated file in
// database/migrations calls this from its init().
func Register(m Migration) {
	registry = append(registry, m)
}

// All returns every registered migration, sorted oldest-first by name.
func All() []Migration {
	sort.Slice(registry, func(i, j int) bool {
		return registry[i].Name < registry[j].Name
	})
	return registry
}
