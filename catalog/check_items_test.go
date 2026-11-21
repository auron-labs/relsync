package catalog_test

import (
	"testing"

	"github.com/aaronflorey/release-sync/catalog"
)

func TestCheckItemsChecksOnlyTheItemsAskedForAndSkipsHiddenOnes(t *testing.T) {
	h := openHarness(t)
	for _, name := range []string{"one", "two", "three", "four"} {
		h.source.publish("acme", name, page(`"e"`, rel("v1.0.0", 1)))
	}
	one, two, three := h.add("one", catalog.AddOptions{}), h.add("two", catalog.AddOptions{}), h.add("three", catalog.AddOptions{})
	h.add("four", catalog.AddOptions{})
	if err := h.catalog.SetHidden(t.Context(), three.Item.ID, true); err != nil {
		t.Fatal(err)
	}
	_, before := h.source.counts()

	checker := catalog.NewChecker(h.catalog, newManager(t))
	sum, err := checker.CheckItems(t.Context(), []int64{one.Item.ID, two.Item.ID, three.Item.ID, 9999}, 2, func(int, int, string) {})
	if err != nil {
		t.Fatal(err)
	}
	if _, after := h.source.counts(); after-before != 2 {
		t.Errorf("%d release requests, want one for each of the two Items that were asked for and visible", after-before)
	}
	if sum.Total != 4 || sum.Checked != 2 || sum.Skipped != 2 || sum.Failed != 0 {
		t.Errorf("summary = %+v, want 4 asked for, 2 checked, 2 skipped (hidden and gone)", sum)
	}
}
