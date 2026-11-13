package classificationml

import (
	"errors"
	"slices"
	"testing"
)

func trainingFixture() []Example {
	return []Example{
		{Text: "invoice order amount product catalog", Category: "business", Split: "train"},
		{Text: "product stock inventory quantity", Category: "business", Split: "train"},
		{Text: "invoice billing order revenue", Category: "business", Split: "train"},
		{Text: "customer email contact personal", Category: "personal", Split: "train"},
		{Text: "email phone person name", Category: "personal", Split: "train"},
		{Text: "customer phone address personal", Category: "personal", Split: "train"},
		{Text: "customer email personal address", Category: "personal", Split: "evaluation"},
		{Text: "product invoice stock order", Category: "business", Split: "evaluation"},
	}
}
func TestLocalTrainingUsesSeparateEvaluationAndAbstains(t *testing.T) {
	examples := trainingFixture()
	m, ev, e := Train(examples, 0.65)
	if e != nil {
		t.Fatal(e)
	}
	if m.TrainCount != 6 || ev.Count != 2 || ev.Accuracy != 1 || len(ev.PerClass) != 2 || ev.MacroF1 != 1 {
		t.Fatalf("wrong heldout benchmark: %+v", ev)
	}
	if p := m.Predict("customer personal email"); p.Category != "personal" || len(p.MatchedTokens) == 0 || len(p.Scores) != 2 {
		t.Fatalf("missing supervised prediction: %+v", p)
	}
	if p := m.Predict("unseen xyzqv opaque"); p.Category != "unknown" || len(p.Scores) != 0 {
		t.Fatalf("unseen metadata should abstain: %+v", p)
	}
	second, _, e := Train(examples, 0.65)
	if e != nil || Digest(m) != Digest(second) {
		t.Fatal("model not reproducible")
	}
	if !slices.Contains(Tokens("员工手机号"), "手机") {
		t.Fatal("Chinese features missing")
	}
	examples[6].Text = examples[3].Text
	if _, _, e := Train(examples, 0.65); !errors.Is(e, ErrDataset) {
		t.Fatal("training/evaluation leakage accepted")
	}
}
func TestLocalTrainingBoundsAndLabels(t *testing.T) {
	for _, change := range []func([]Example){
		func(x []Example) { x[0].Category = "core" },
		func(x []Example) { x[0].Split = "validation" },
		func(x []Example) { x[6].Split = "train" },
		func(x []Example) { x[0].Text = "" },
	} {
		samples := trainingFixture()
		change(samples)
		if _, _, e := Train(samples, 0.65); !errors.Is(e, ErrDataset) {
			t.Fatal("invalid dataset accepted")
		}
	}
	if _, _, e := Train(trainingFixture(), 0.1); !errors.Is(e, ErrDataset) {
		t.Fatal("invalid threshold accepted")
	}
}
