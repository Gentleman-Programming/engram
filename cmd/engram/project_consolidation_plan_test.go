package main

import (
	"reflect"
	"testing"

	"github.com/Gentleman-Programming/engram/v3/internal/project"
)

func TestPlanDetectedProjectConsolidation(t *testing.T) {
	candidates := []project.ProjectMatch{{Name: "ENGRAM"}, {Name: " EnGrAm "}}
	before := append([]project.ProjectMatch(nil), candidates...)
	for _, tc := range []struct {
		answer  string
		sources []string
		invalid []string
	}{
		{"all", []string{"ENGRAM", " EnGrAm "}, nil},
		{"a", []string{"ENGRAM", " EnGrAm "}, nil},
		{"2, 1,2", []string{" EnGrAm ", "ENGRAM", " EnGrAm "}, nil},
		{"1tail", []string{"ENGRAM"}, nil}, // Preserve the existing fmt.Sscanf contract.
		{"1,x,2,y", nil, []string{"x"}},
		{"1,,2", nil, []string{""}},
		{"0", nil, []string{"0"}},
		{"3", nil, []string{"3"}},
		{"y", nil, []string{"y"}}, // Group-only alias must not expand this mode.
	} {
		t.Run(tc.answer, func(t *testing.T) {
			got := planDetectedProjectConsolidation("engram", candidates, tc.answer)
			want := projectConsolidationPlan{canonical: "engram", sources: tc.sources, invalidSelections: tc.invalid}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("plan=%+v want=%+v", got, want)
			}
			if !reflect.DeepEqual(candidates, before) {
				t.Fatal("planner modified candidate list")
			}
		})
	}
}

func TestPlanGroupedProjectConsolidation(t *testing.T) {
	group := projectGroup{Canonical: " EnGrAm ", Names: []string{"ENGRAM", "engram", " EnGrAm "}}
	before := append([]string(nil), group.Names...)
	for _, tc := range []struct {
		answer  string
		sources []string
		invalid []string
	}{
		{"all", []string{"ENGRAM", " EnGrAm "}, nil},
		{"a", []string{"ENGRAM", " EnGrAm "}, nil},
		{"y", []string{"ENGRAM", " EnGrAm "}, nil},
		{"yes", []string{"ENGRAM", " EnGrAm "}, nil},
		{"3,2,1,3", []string{" EnGrAm ", "ENGRAM", " EnGrAm "}, nil},
		{"2", nil, nil},
		{"1,x,3,,0", []string{"ENGRAM", " EnGrAm "}, []string{"x", "", "0"}},
		{"4", nil, []string{"4"}},
	} {
		t.Run(tc.answer, func(t *testing.T) {
			got := planGroupedProjectConsolidation(group, tc.answer, "explicit-target")
			want := projectConsolidationPlan{canonical: "engram", renameTarget: "explicit-target", sources: tc.sources, invalidSelections: tc.invalid}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("plan=%+v want=%+v", got, want)
			}
			if group.Canonical != " EnGrAm " || !reflect.DeepEqual(group.Names, before) {
				t.Fatal("planner modified group")
			}
		})
	}

	unicode := planGroupedProjectConsolidation(projectGroup{Canonical: "cafe\u0301", Names: []string{"cafe\u0301", "café"}}, "all", "")
	if unicode.canonical != "café" || !reflect.DeepEqual(unicode.sources, []string{"cafe\u0301"}) {
		t.Fatalf("Unicode selection changed: %+v", unicode)
	}
}
