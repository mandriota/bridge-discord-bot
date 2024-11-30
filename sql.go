package main

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/disgoorg/snowflake/v2"
	"github.com/huandu/go-sqlbuilder"
)

func createMessagesTable(ctx context.Context, tx *sql.Tx) error {
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

func createAuthorsTable(ctx context.Context, tx *sql.Tx) error {
		createAuthorsTableQuery, _ := sqlbuilder.CreateTable("authors").
		IfNotExists().
		Define("username", "TEXT", "NOT NULL").
		Define("id", "INT", "NOT NULL").
		Define("PRIMARY KEY", "(username, id)").
		BuildWithFlavor(sqlbuilder.SQLite)

	_, err := tx.ExecContext(ctx, createAuthorsTableQuery)
	return err
}

func createLinksTable(ctx context.Context, tx *sql.Tx) error {
		createLinksTableQuery, _ := sqlbuilder.CreateTable("links").
		IfNotExists().
		Define("virtual_channel_key", "TEXT", "NOT NULL").
		Define("channel_id", "INT", "NOT NULL").
		Define("note", "TEXT", "NOT NULL").
		Define("PRIMARY KEY", "(virtual_channel_key, channel_id)").
		BuildWithFlavor(sqlbuilder.SQLite)

	_, err := tx.ExecContext(ctx, createLinksTableQuery)
	return err
}

func loadRelatedMessageID(ctx context.Context, db *sql.DB, targetChannelID, messageRef snowflake.ID) (related snowflake.ID, err error) {
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

func loadDirelatedMessageID(ctx context.Context, db *sql.DB, targetChannelID, messageRef snowflake.ID) (related snowflake.ID, err error) {
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

func saveMessageMapping(ctx context.Context, tx *sql.Tx, originalChannelID, originalID, hookChannelID, hookID snowflake.ID) error {
	query, args := sqlbuilder.SQLite.NewInsertBuilder().
		InsertIgnoreInto("messages").
		Cols("original_channel_id", "original_message_id", "hook_channel_id", "hook_message_id").
		Values(originalChannelID, originalID, hookChannelID, hookID).
		Build()

	_, err := tx.ExecContext(ctx, query, args...)
	return err
}

func loadAuthorID(ctx context.Context, db *sql.DB, username string) (id snowflake.ID, err error) {
	selectB := sqlbuilder.SQLite.NewSelectBuilder()
	query, args := selectB.Select("id").
		From("authors").
		Where(selectB.Equal("username", username)).
		Build()

	return id, db.QueryRowContext(ctx, query, args...).Scan(&id)
}

func saveAuthorMapping(ctx context.Context, tx *sql.Tx, username string, id snowflake.ID) error {
	query, args := sqlbuilder.SQLite.NewInsertBuilder().
		InsertIgnoreInto("authors").
		Cols("username", "id").
		Values(username, id).
		Build()

	_, err := tx.ExecContext(ctx, query, args...)
	return err
}

func loadRelatedChannels(ctx context.Context, db *sql.DB, channelID snowflake.ID) ([]snowflake.ID, error) {
	queryB := sqlbuilder.NewSelectBuilder()
	subqueryB := sqlbuilder.NewSelectBuilder()

	queryB.Select("channel_id").
		From("links").
		Where(
			queryB.In("virtual_channel_key", subqueryB),
			queryB.NotEqual("channel_id", channelID),
		)

	subqueryB.Select("virtual_channel_key").
		From("links").
		Where(subqueryB.Equal("channel_id", channelID))

	query, args := queryB.BuildWithFlavor(sqlbuilder.SQLite)

	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch related channels: %w", err)
	}
	defer rows.Close()

	relatedChannelIDText := ""
	relatedChannelsID := []snowflake.ID{}

	for rows.Next() {
		if err := rows.Scan(&relatedChannelIDText); err != nil {
			return nil, fmt.Errorf("failed to scan related channel: %w", err)
		}
		relatedChannelsID = append(relatedChannelsID, snowflake.MustParse(relatedChannelIDText))
	}

	return relatedChannelsID, nil
}

func buildInsertLinkQuery(virtualChannelKey string, channelID snowflake.ID, note string) (string, []any) {
	insertB := sqlbuilder.SQLite.NewInsertBuilder()

	query, args := insertB.InsertIgnoreInto("links").
		Cols("virtual_channel_key", "channel_id", "note").
		Values(virtualChannelKey, channelID, note).
		BuildWithFlavor(sqlbuilder.SQLite)

	return query, args
}

func buildDeleteLinkQuery(virtualChannelKey string, channelID snowflake.ID) (string, []any) {
	deleteB := sqlbuilder.NewDeleteBuilder()

	query, args := deleteB.DeleteFrom("links").
		Where(deleteB.Equal("virtual_channel_key", virtualChannelKey)).
		Where(deleteB.Equal("channel_id", channelID)).
		BuildWithFlavor(sqlbuilder.SQLite)

	return query, args
}

func buildDeleteAllLinksQuery(channelID snowflake.ID) (string, []any) {
	deleteB := sqlbuilder.NewDeleteBuilder()

	query, args := deleteB.DeleteFrom("links").
		Where(deleteB.Equal("channel_id", channelID)).
		BuildWithFlavor(sqlbuilder.SQLite)

	return query, args
}

func buildSelectVirtualChannelKeyQuery(channelID snowflake.ID) (string, []any) {
	selectB := sqlbuilder.NewSelectBuilder()

	query, args := selectB.Select("virtual_channel_key", "note").
		From("links").
		Where(selectB.Equal("channel_id", channelID)).
		BuildWithFlavor(sqlbuilder.SQLite)

	return query, args
}
