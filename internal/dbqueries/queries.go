package dbqueries

import (
	"github.com/disgoorg/snowflake/v2"
	"github.com/huandu/go-sqlbuilder"
)

func BuildInsertLinkQuery(virtualChannelKey string, channelID snowflake.ID, note string) (string, []any) {
	insertB := sqlbuilder.SQLite.NewInsertBuilder()

	query, args := insertB.InsertIgnoreInto("links").
		Cols("virtual_channel_key", "channel_id", "note").
		Values(virtualChannelKey, channelID, note).
		BuildWithFlavor(sqlbuilder.SQLite)

	return query, args
}

func BuildDeleteLinkQuery(virtualChannelKey string, channelID snowflake.ID) (string, []any) {
	deleteB := sqlbuilder.NewDeleteBuilder()

	query, args := deleteB.DeleteFrom("links").
		Where(deleteB.Equal("virtual_channel_key", virtualChannelKey)).
		Where(deleteB.Equal("channel_id", channelID)).
		BuildWithFlavor(sqlbuilder.SQLite)

	return query, args
}

func BuildDeleteAllLinksQuery(channelID snowflake.ID) (string, []any) {
	deleteB := sqlbuilder.NewDeleteBuilder()

	query, args := deleteB.DeleteFrom("links").
		Where(deleteB.Equal("channel_id", channelID)).
		BuildWithFlavor(sqlbuilder.SQLite)

	return query, args
}

func BuildSelectVirtualChannelKeyQuery(channelID snowflake.ID) (string, []any) {
	selectB := sqlbuilder.NewSelectBuilder()

	query, args := selectB.Select("virtual_channel_key", "note").
		From("links").
		Where(selectB.Equal("channel_id", channelID)).
		BuildWithFlavor(sqlbuilder.SQLite)

	return query, args
}
