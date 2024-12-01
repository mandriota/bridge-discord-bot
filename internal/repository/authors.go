package repository

import (
	"context"
	"database/sql"

	"github.com/disgoorg/snowflake/v2"
	"github.com/huandu/go-sqlbuilder"
)

func CreateAuthorsTable(ctx context.Context, tx *sql.Tx) error {
		createAuthorsTableQuery, _ := sqlbuilder.CreateTable("authors").
		IfNotExists().
		Define("username", "TEXT", "NOT NULL").
		Define("id", "INT", "NOT NULL").
		Define("PRIMARY KEY", "(username, id)").
		BuildWithFlavor(sqlbuilder.SQLite)

	_, err := tx.ExecContext(ctx, createAuthorsTableQuery)
	return err
}

func LoadAuthorID(ctx context.Context, db *sql.DB, username string) (id snowflake.ID, err error) {
	selectB := sqlbuilder.SQLite.NewSelectBuilder()
	query, args := selectB.Select("id").
		From("authors").
		Where(selectB.Equal("username", username)).
		Build()

	return id, db.QueryRowContext(ctx, query, args...).Scan(&id)
}

func SaveAuthorMapping(ctx context.Context, tx *sql.Tx, username string, id snowflake.ID) error {
	query, args := sqlbuilder.SQLite.NewInsertBuilder().
		InsertIgnoreInto("authors").
		Cols("username", "id").
		Values(username, id).
		Build()

	_, err := tx.ExecContext(ctx, query, args...)
	return err
}
