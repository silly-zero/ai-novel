package database

import (
	"context"
	"errors"
	"sort"

	"github.com/ai-novel/studio/ent"
	"github.com/ai-novel/studio/ent/chapter"
	"github.com/ai-novel/studio/ent/novel"
)

type DuplicateChapterOrder struct {
	NovelID    int
	Order      int
	ChapterIDs []int
}

func FindDuplicateChapterOrders(ctx context.Context, client *ent.Client) ([]DuplicateChapterOrder, error) {
	if client == nil {
		return nil, errors.New("database client is nil")
	}
	var groups []struct {
		NovelID int `json:"novel_chapters"`
		Order   int `json:"order"`
		Count   int `json:"count"`
	}
	if err := client.Chapter.Query().
		GroupBy(chapter.FieldOrder, chapter.NovelColumn).
		Aggregate(ent.Count()).
		Scan(ctx, &groups); err != nil {
		return nil, err
	}
	result := make([]DuplicateChapterOrder, 0)
	for _, group := range groups {
		if group.Count < 2 {
			continue
		}
		ids, err := client.Chapter.Query().Where(
			chapter.HasNovelWith(novel.ID(group.NovelID)),
			chapter.OrderEQ(group.Order),
		).Order(ent.Asc(chapter.FieldID)).Select(chapter.FieldID).Ints(ctx)
		if err != nil {
			return nil, err
		}
		sort.Ints(ids)
		result = append(result, DuplicateChapterOrder{NovelID: group.NovelID, Order: group.Order, ChapterIDs: ids})
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].NovelID != result[j].NovelID {
			return result[i].NovelID < result[j].NovelID
		}
		return result[i].Order < result[j].Order
	})
	return result, nil
}
