package repository

import (
	"context"
	"database/sql"
	"fmt"
)

func InitDB(ctx context.Context, db **sql.DB, filePath string) (err error) {
	*db, err = sql.Open("sqlite3", filePath)
	if err != nil {
		return fmt.Errorf("failed to open database: %w", err)
	}

	tx, err := (*db).BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if err := CreateMessagesTable(ctx, tx); err != nil {
		return fmt.Errorf("failed to create table: %w", err)
	}

	if err := CreateAuthorsTable(ctx, tx); err != nil {
		return fmt.Errorf("failed to create table: %w", err)
	}

	if err := CreateLinksTable(ctx, tx); err != nil {
		return fmt.Errorf("failed to create table: %w", err)
	}
	return tx.Commit()
}
