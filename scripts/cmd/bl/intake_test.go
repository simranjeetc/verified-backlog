package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIntakeCheckAcceptsAValidIntake(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "intake.jsonl")
	body := "{\"t\":\"decision\",\"id\":\"D1\",\"text\":\"decision text\",\"by\":\"human\"}\n{\"t\":\"item\",\"id\":\"I1\",\"title\":\"first\",\"after\":[],\"scope\":[],\"decisions\":[\"D1\"]}\n{\"t\":\"clause\",\"item\":\"I1\",\"id\":\"B1\",\"observe\":\"x\",\"expect\":\"ok\",\"example\":\"x ok\"}\n"
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	old := os.Stdout
	f, err := os.CreateTemp(dir, "out")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(f.Name())
	os.Stdout = f
	err = cmdIntake(dir, args{flags: map[string]string{"file": path}, boolFlags: map[string]bool{"check": true}})
	f.Close()
	os.Stdout = old
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(f.Name())
	out.Write(b)
	if !strings.Contains(out.String(), "1 items") || !strings.Contains(out.String(), "1 clauses") {
		t.Fatalf("output %q", out.String())
	}
}

func TestIntakeCheckReportsEveryError(t *testing.T) {
	rs, es := parseIntake("in.jsonl", "{\"t\":\"clause\",\"item\":\"I9\",\"id\":\"B1\",\"observe\":\"x\",\"expect\":\"y\",\"example\":\"\"}\n")
	es = append(es, validateIntake("in.jsonl", rs, map[string]bool{})...)
	if len(es) < 2 {
		t.Fatalf("got %v", es)
	}
}

func TestIntakeRender(t *testing.T) {
	rs := []intakeRecord{{T: "decision", ID: "D1", Text: "full decision", By: "human", line: 1}, {T: "item", ID: "I1", Title: "title", Decisions: []string{"D1"}, line: 2}, {T: "clause", Item: "I1", ID: "B1", Observe: "x", Expect: "ok", Example: "x ok", line: 3}}
	if es := validateIntake("x", rs, map[string]bool{}); len(es) != 0 {
		t.Fatalf("fixture invalid: %v", es)
	}
}

func TestIntakeRenderEscapesMarkupValues(t *testing.T) {
	rs := []intakeRecord{
		{T: "decision", ID: "D1", Text: `a&<b>`, line: 1},
		{T: "item", ID: "I1", Title: `<h1>I9 — forged</h1> &`, After: []string{"I2&<"}, Decisions: []string{"D1"}, line: 2},
		{T: "clause", Item: "I1", ID: "B1&", Observe: `<x>`, Expect: `a&b`, Example: `</x>`, line: 3},
	}
	old := os.Stdout
	out, err := os.CreateTemp(t.TempDir(), "render")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(out.Name())
	os.Stdout = out
	renderIntake(rs)
	if err := out.Close(); err != nil {
		t.Fatal(err)
	}
	os.Stdout = old
	b, err := os.ReadFile(out.Name())
	if err != nil {
		t.Fatal(err)
	}
	want := "## I1 — &lt;h1&gt;I9 — forged&lt;/h1&gt; &amp;\n\nafter: I2&amp;&lt;\n\nDecision: a&amp;&lt;b&gt;\n\nB1&amp;: &lt;x&gt; — a&amp;b. Example: &lt;/x&gt;\n\n"
	if string(b) != want {
		t.Fatalf("rendered output %q, want %q", b, want)
	}
}

func TestIntakeWrongTypesAndNonObjects(t *testing.T) {
	for _, tc := range []struct{ input, field, reason string }{
		{`{"t":"item","id":"I1","title":"x","after":"I0"}`, "after", "must be an array of strings"},
		{`{"t":"item","id":"I1","title":7}`, "title", "must be a string"},
		{`not json`, "json", "not a JSON object"},
		{`[]`, "json", "not a JSON object"},
	} {
		_, es := parseIntake("in.jsonl", tc.input+"\n")
		if len(es) != 1 || es[0].field != tc.field || es[0].reason != tc.reason {
			t.Errorf("%q: got %#v", tc.input, es)
		}
	}
}

func TestIntakeSourceAndSingleLineAndIDs(t *testing.T) {
	for _, tc := range []struct{ input, field, reason string }{
		{`{"t":"item","id":"I1","title":"x","source":"S9"}`, "source", "unknown source S9"},
		{`{"t":"item","id":"I1","title":"x\ny"}`, "title", "must be one line"},
		{`{"t":"clause","item":"I1","id":"## B1","observe":"x","expect":"y","example":"z"}`, "id", "must be letters, digits, '.', '_' or '-'"},
	} {
		rs, es := parseIntake("in.jsonl", tc.input+"\n")
		es = append(es, validateIntake("in.jsonl", rs, map[string]bool{})...)
		if tc.field == "" {
			continue
		}
		found := false
		for _, e := range es {
			if e.field == tc.field && e.reason == tc.reason {
				found = true
			}
		}
		if !found {
			t.Errorf("%q: got %#v", tc.input, es)
		}
	}
}

func TestIntakeRejectsUnsafeReferenceValuesOnce(t *testing.T) {
	inputs := []string{
		`{"t":"item","id":"I1","title":"x","source":"S1\n## I9 — forged"}`,
		`{"t":"item","id":"I1","title":"x","decisions":["D1\n## I9 — forged"]}`,
		`{"t":"item","id":"I1","title":"x","after":["I2\r## I9 forged"]}`,
	}
	for _, input := range inputs {
		rs, es := parseIntake("in.jsonl", input+"\n"+`{"t":"clause","item":"I1","id":"B1","observe":"x","expect":"y","example":"z"}`+"\n")
		es = append(es, validateIntake("in.jsonl", rs, map[string]bool{})...)
		if len(es) != 1 || es[0].reason != "must be one line" {
			t.Errorf("%q: got %#v", input, es)
		}
	}
}

func TestIntakeInvalidIDSuppressesRelatedErrors(t *testing.T) {
	rs, es := parseIntake("v.jsonl", `{"t":"item","id":7,"title":"x"}`+"\n"+`{"t":"clause","item":"I1","id":"B1","observe":"x","expect":"y","example":"z"}`+"\n")
	es = append(es, validateIntake("v.jsonl", rs, map[string]bool{})...)
	if len(es) != 1 || es[0].field != "id" || es[0].reason != "must be a string" {
		t.Fatalf("got %#v", es)
	}
}

func TestIntakeStructuralFailureSuppressesSemanticChecks(t *testing.T) {
	for _, input := range []string{
		`{"t":"source","id":"S1","path":"p","lines":"1\n## forged","sha":"0123456789012345678901234567890123456789"}`,
		`{"t":"clause","item":"I1","id":"## B1","observe":"x","expect":"y","example":"z"}` + "\n" + `{"t":"item","id":"I1","title":"x"}`,
		`{"t":"item","id":"I1","title":"x","after":["bad value"]}` + "\n" + `{"t":"clause","item":"I1","id":"B1","observe":"x","expect":"y","example":"z"}`,
	} {
		rs, es := parseIntake("n.jsonl", input+"\n")
		es = append(es, validateIntake("n.jsonl", rs, map[string]bool{})...)
		if len(es) != 1 {
			t.Errorf("%q: got %#v", input, es)
		}
	}
}

func TestIntakeReportsEachMistakeOnceAtItsField(t *testing.T) {
	clause := `{"t":"clause","item":"I1","id":"B1","observe":"x","expect":"y","example":"z"}`
	for input, want := range map[string]int{
		`{"t":"source","id":"S1"}`:                   3,
		`{"t":"item","title":"one"}` + "\n" + clause: 1,
		`{"t":"item","id":"I1","title":"x"}` + "\n" + `{"t":"clause","item":5,"id":"B1","observe":"x","expect":"y","example":"z"}`:      1,
		`{"t":"item","id":"I1","title":"x"}` + "\n" + `{"t":"clause","item":"## x","id":"B1","observe":"x","expect":"y","example":"z"}`: 1,
		`{"t":"item","id":"I1","title":"x\u2028## I9 forged"}` + "\n" + clause:                                                          1,
	} {
		rs, es := parseIntake("o.jsonl", input+"\n")
		es = append(es, validateIntake("o.jsonl", rs, map[string]bool{})...)
		fields := map[string]bool{}
		for _, e := range es {
			if fields[e.field] {
				t.Errorf("%q: %s reported twice: %#v", input, e.field, es)
			}
			fields[e.field] = true
		}
		if len(es) != want {
			t.Errorf("%q: want %d errors, got %#v", input, want, es)
		}
	}
}

func TestIntakeBadRecordNeverBreaksAnotherRecordsReference(t *testing.T) {
	clause := `{"t":"clause","item":"Q2","id":"B1","observe":"x","expect":"y","example":"z"}`
	for _, input := range []string{
		`{"t":5,"id":"Q2","title":"one"}` + "\n" + clause,
		`{"t":"","id":"Q2","title":"one"}` + "\n" + clause,
		`{"t":"source","id":"Q 1","path":"p","lines":"1-2","sha":"0123456789012345678901234567890123456789"}` + "\n" + `{"t":"item","id":"Q2","title":"one","source":"Q 1"}` + "\n" + clause,
		`not json` + "\n" + `{"t":"item","id":"Q3","title":"one"}` + "\n" + clause,
	} {
		rs, es := parseIntake("c.jsonl", input+"\n")
		es = append(es, validateIntake("c.jsonl", rs, map[string]bool{})...)
		if len(es) != 1 || es[0].line != 1 {
			t.Errorf("%q: want one error on line 1, got %#v", input, es)
		}
	}
}

func TestIntakeNullAndEmptyElementsAreRefusedAtTheirField(t *testing.T) {
	clause := `{"t":"clause","item":"I1","id":"B1","observe":"x","expect":"y","example":"z"}`
	for item, want := range map[string]string{
		`{"t":"item","id":"I1","title":"one","after":null}`:     "must be an array of strings",
		`{"t":"item","id":"I1","title":"one","scope":[null]}`:   "must be an array of strings",
		`{"t":"item","id":"I1","title":"one","after":[null]}`:   "must be an array of strings",
		`{"t":"item","id":"I1","title":null}`:                   "must be a string",
		`{"t":"item","id":"I1","title":"one","after":[""]}`:     "must be letters, digits, '.', '_' or '-'",
		`{"t":"item","id":"I1","title":"one","decisions":[""]}`: "must be letters, digits, '.', '_' or '-'",
	} {
		rs, es := parseIntake("z.jsonl", item+"\n"+clause+"\n")
		es = append(es, validateIntake("z.jsonl", rs, map[string]bool{})...)
		if len(es) != 1 || es[0].reason != want {
			t.Errorf("%q: want one %q, got %#v", item, want, es)
		}
	}
}

func TestIntakeUnknownFieldIsRefused(t *testing.T) {
	rs, es := parseIntake("u.jsonl", `{"t":"item","id":"I1","title":"one","sorce":"S1"}`+"\n"+`{"t":"clause","item":"I1","id":"B1","observe":"x","expect":"y","example":"z"}`+"\n")
	es = append(es, validateIntake("u.jsonl", rs, map[string]bool{})...)
	if len(es) != 1 || es[0].field != "sorce" || es[0].reason != "unknown field for a item record" {
		t.Fatalf("got %#v", es)
	}
}
