package postgres

import (
	"context"

	"github.com/google/uuid"

	"github.com/KGMA74/relaix-server/store"
)

type accountStore struct{ q querier }

const accountColumns = `id, name, created_at`

func (a accountStore) Create(ctx context.Context, name string) (*store.Account, error) {
	return scanAccount(a.q.QueryRow(ctx,
		`INSERT INTO accounts (name) VALUES ($1) RETURNING `+accountColumns, name))
}

func (a accountStore) Get(ctx context.Context, id uuid.UUID) (*store.Account, error) {
	return scanAccount(a.q.QueryRow(ctx,
		`SELECT `+accountColumns+` FROM accounts WHERE id = $1`, id))
}

func (a accountStore) List(ctx context.Context) ([]*store.Account, error) {
	rows, err := a.q.Query(ctx, `SELECT `+accountColumns+` FROM accounts ORDER BY created_at, id`)
	if err != nil {
		return nil, translate(err)
	}
	defer rows.Close()

	var out []*store.Account
	for rows.Next() {
		acc, err := scanAccount(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, acc)
	}
	return out, translate(rows.Err())
}

func scanAccount(row scanner) (*store.Account, error) {
	var acc store.Account
	if err := row.Scan(&acc.ID, &acc.Name, &acc.CreatedAt); err != nil {
		return nil, translate(err)
	}
	return &acc, nil
}
