// Package migrator is the migration engine (registry plus run/fresh logic); it holds no migrations itself.
// Migrations self-register via init() in database/migrations, kept separate so a future "squash" command can replace that package alone.
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
