package main

import (
	"reflect"
	"testing"
)

func TestRefusalRuleCountsIncludesLegacyAndUnnamed(t *testing.T) {
	r10 := "R10"
	empty := ""
	got := refusalRuleCounts([]runRecord{
		{Exit: 2, Rule: &r10}, {Exit: 2, Rule: &r10}, {Exit: 2, Rule: &empty},
		{Exit: 2}, {Exit: 0, Rule: &r10},
	})
	want := map[string]int{"R10": 2, "unnamed": 1, "unrecorded": 1}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("refusalRuleCounts = %#v, want %#v", got, want)
	}
	if got := sortedRefusalRules(got); !reflect.DeepEqual(got, []string{"R10", "unnamed", "unrecorded"}) {
		t.Fatalf("sortedRefusalRules = %v", got)
	}
}

func TestSortedRefusalRulesOrdersByCountThenName(t *testing.T) {
	got := sortedRefusalRules(map[string]int{"Z9": 2, "R10": 3, "R39": 2})
	want := []string{"R10", "R39", "Z9"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("sortedRefusalRules = %v, want %v", got, want)
	}
}
