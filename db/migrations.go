// Package db holds the database schema and the migrations that build it.
package db

import "embed"

// Migrations carries the SQL migration files, embedded so that a built binary
// can bring its own schema up to date with no files on disk beside it and no
// migration CLI installed on the host.
//
// The files are in goose format (-- +goose Up / -- +goose Down).
//
//go:embed migrations/*.sql
var Migrations embed.FS
