package repository

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/disgoorg/snowflake/v2"
	"github.com/huandu/go-sqlbuilder"
)

func CreateLinksTable(ctx context.Context, tx *sql.Tx) error {
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

func LoadRelatedChannels(ctx context.Context, db *sql.DB, channelID snowflake.ID) ([]snowflake.ID, error) {
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
