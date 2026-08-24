package database

import (
	"context"
	"testing"
)

func TestDuplicateChapterOrderHasStableShape(t *testing.T) {
	result := DuplicateChapterOrder{NovelID: 7, Order: 3, ChapterIDs: []int{11, 12}}
	if result.NovelID != 7 || result.Order != 3 || len(result.ChapterIDs) != 2 {
		t.Fatalf("result = %#v", result)
	}
}

func TestFindDuplicateChapterOrdersRejectsNilClient(t *testing.T) {
	_, err := FindDuplicateChapterOrders(context.Background(), nil)
	if err == nil {
		t.Fatal("nil client did not return an error")
	}
}
