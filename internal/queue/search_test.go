package queue

import (
	"context"
	"reflect"
	"testing"
)

func TestListJobsSearchCombinesFiltersBeforePagination(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	ctx := context.Background()
	for _, item := range []struct{ watch, path string }{
		{"incoming", "/drop/Report-old.csv"},
		{"archive", "/drop/report-archive.csv"},
		{"incoming", "/drop/report-new.csv"},
		{"incoming", "/drop/100%_complete.csv"},
		{"incoming", "/drop/unrelated.csv"},
	} {
		if _, _, err := store.Enqueue(ctx, EnqueueParams{WatchName: item.watch, Path: item.path}); err != nil {
			t.Fatal(err)
		}
	}
	claimed, err := store.Claim(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Fail(ctx, claimed.ID, claimed.RunID, "Network timeout", 0); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		filter JobFilter
		ids    []int64
	}{
		{"case insensitive", JobFilter{Search: "REPORT"}, []int64{3, 2, 1}},
		{"first matching page", JobFilter{Search: "report", Limit: 1}, []int64{3}},
		{"next matching page", JobFilter{Search: "report", Limit: 1, Offset: 1}, []int64{2}},
		{"watch and search", JobFilter{Search: "report", WatchName: "incoming"}, []int64{3, 1}},
		{"status watch and search", JobFilter{Search: "report", WatchName: "incoming", Status: StatusQueued}, []int64{3}},
		{"watch name", JobFilter{Search: "ARCHIVE"}, []int64{2}},
		{"status text", JobFilter{Search: "failed"}, []int64{1}},
		{"error text", JobFilter{Search: "timeout"}, []int64{1}},
		{"job ID", JobFilter{Search: "#2"}, []int64{2}},
		{"trim whitespace", JobFilter{Search: "  report-old  "}, []int64{1}},
		{"literal wildcards", JobFilter{Search: "%_"}, []int64{4}},
		{"literal SQL", JobFilter{Search: "' OR 1=1 --"}, []int64{}},
		{"hash is not empty ID", JobFilter{Search: "#"}, []int64{}},
		{"no matches", JobFilter{Search: "missing-file"}, []int64{}},
	} {
		t.Run(test.name, func(t *testing.T) {
			jobs, err := store.ListJobs(ctx, test.filter)
			if err != nil {
				t.Fatal(err)
			}
			ids := make([]int64, 0, len(jobs))
			for _, job := range jobs {
				ids = append(ids, job.ID)
			}
			if !reflect.DeepEqual(ids, test.ids) {
				t.Fatalf("matching jobs = %v, want %v", ids, test.ids)
			}
		})
	}
}
