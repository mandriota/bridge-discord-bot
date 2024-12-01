package repository

import (
	"context"
	"database/sql"

	"github.com/disgoorg/snowflake/v2"
	"github.com/huandu/go-sqlbuilder"
)

func CreateMessagesTable(ctx context.Context, tx *sql.Tx) error {
	createMessagesTableQuery, _ := sqlbuilder.CreateTable("messages").
		IfNotExists().
		Define("original_channel_id", "INT", "NOT NULL").
		Define("original_message_id", "INT", "NOT NULL").
		Define("hook_channel_id", "INT", "NOT NULL").
		Define("hook_message_id", "INT", "NOT NULL").
		Define("PRIMARY KEY", "(original_channel_id, original_message_id, hook_channel_id, hook_message_id)").
		BuildWithFlavor(sqlbuilder.SQLite)

	_, err := tx.ExecContext(ctx, createMessagesTableQuery)
	return err
}

func LoadRelatedMessageID(ctx context.Context, db *sql.DB, targetChannelID, messageRef snowflake.ID) (related snowflake.ID, err error) {
	selectB := sqlbuilder.NewSelectBuilder()
	selectB.Select(selectB.As("hook_message_id", "related_message_id")).
		From("messages").
		Where(
			selectB.And(
				selectB.Equal("hook_channel_id", targetChannelID),
				selectB.Equal("original_message_id", messageRef),
			),
		)

	query, args := selectB.BuildWithFlavor(sqlbuilder.SQLite)
	return related, db.QueryRowContext(ctx, query, args...).Scan(&related)
}

func LoadDirelatedMessageID(ctx context.Context, db *sql.DB, targetChannelID, messageRef snowflake.ID) (related snowflake.ID, err error) {
	selectBL := sqlbuilder.NewSelectBuilder()
	selectBL.Select(selectBL.As("original_message_id", "related_message_id")).
		From("messages").
		Where(
			selectBL.And(
				selectBL.Equal("original_channel_id", targetChannelID),
				selectBL.Equal("hook_message_id", messageRef),
			),
		)

	selectBR := sqlbuilder.NewSelectBuilder()
	selectBR.Select(selectBR.As("hook_message_id", "related_message_id")).
		From("messages").
		Where(
			selectBR.And(
				selectBR.Equal("hook_channel_id", targetChannelID),
				selectBR.Equal("original_message_id", messageRef),
			),
		)

	query, args := sqlbuilder.Union(selectBL, selectBR).BuildWithFlavor(sqlbuilder.SQLite)
	return related, db.QueryRowContext(ctx, query, args...).Scan(&related)
}

func SaveMessageMapping(ctx context.Context, tx *sql.Tx, originalChannelID, originalID, hookChannelID, hookID snowflake.ID) error {
	query, args := sqlbuilder.SQLite.NewInsertBuilder().
		InsertIgnoreInto("messages").
		Cols("original_channel_id", "original_message_id", "hook_channel_id", "hook_message_id").
		Values(originalChannelID, originalID, hookChannelID, hookID).
		Build()

	_, err := tx.ExecContext(ctx, query, args...)
	return err
}
