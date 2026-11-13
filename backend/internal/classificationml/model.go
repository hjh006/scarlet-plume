// Package classificationml supplies a bounded, reproducible local classifier.
// Scores are relative model scores, not calibrated probabilities or legal grades.
package classificationml

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

var ErrDataset = errors.New("invalid or overlapping classification dataset")
var Categories = []string{"business", "personal", "sensitive_personal"}

type Example struct {
	Text     string `json:"text"`
	Category string `json:"category"`
	Split    string `json:"split"`
}
type Class struct {
	Category  string         `json:"category"`
	Documents int            `json:"documents"`
	Total     int            `json:"total"`
	Counts    map[string]int `json:"counts"`
}
type Model struct {
	Algorithm  string          `json:"algorithm"`
	MinScore   float64         `json:"minScore"`
	TrainCount int             `json:"trainCount"`
	Vocabulary map[string]bool `json:"vocabulary"`
	Classes    []Class         `json:"classes"`
}
type Score struct {
	Category      string  `json:"category"`
	RelativeScore float64 `json:"relativeScore"`
}
type Prediction struct {
	Category      string   `json:"category"`
	Scores        []Score  `json:"scores"`
	MatchedTokens []string `json:"matchedTokens"`
	Reason        string   `json:"reason"`
}
type ClassMetrics struct {
	Category  string  `json:"category"`
	Support   int     `json:"support"`
	Precision float64 `json:"precision"`
	Recall    float64 `json:"recall"`
	F1        float64 `json:"f1"`
}
type Evaluation struct {
	Count     int                       `json:"count"`
	Accuracy  float64                   `json:"accuracy"`
	MacroF1   float64                   `json:"macroF1"`
	PerClass  []ClassMetrics            `json:"perClass"`
	Confusion map[string]map[string]int `json:"confusion"`
	Boundary  string                    `json:"boundary"`
}

// Tokens uses words and Han character pairs so both English field names and
// Chinese business descriptions work without a language-model dependency.
func Tokens(text string) []string {
	seen := map[string]bool{}
	var word []rune
	var han rune
	flush := func() {
		if len(word) > 0 {
			seen[string(word)] = true
			word = nil
		}
	}
	for _, r := range strings.ToLower(text) {
		if unicode.Is(unicode.Han, r) {
			flush()
			seen[string(r)] = true
			if han != 0 {
				seen[string([]rune{han, r})] = true
			}
			han = r
		} else if unicode.IsLetter(r) || unicode.IsDigit(r) {
			han = 0
			word = append(word, r)
		} else {
			flush()
			han = 0
		}
	}
	flush()
	out := make([]string, 0, len(seen))
	for t := range seen {
		out = append(out, t)
	}
	sort.Strings(out)
	if len(out) > 256 {
		out = out[:256]
	}
	return out
}

func Digest(v any) string {
	b, _ := json.Marshal(v)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func known(c string) bool {
	for _, v := range Categories {
		if c == v {
			return true
		}
	}
	return false
}

func Train(examples []Example, minScore float64) (Model, Evaluation, error) {
	m := Model{Algorithm: "multinomial-naive-bayes-v1", MinScore: minScore, Vocabulary: map[string]bool{}, Classes: []Class{}}
	ev := Evaluation{Confusion: map[string]map[string]int{}, PerClass: []ClassMetrics{}, Boundary: "仅指定独立评估集的实测结果；不是生产准确率、概率校准或供应商同条件基准。"}
	if len(examples) < 8 || len(examples) > 500 || minScore < 0.5 || minScore > 0.99 || math.IsNaN(minScore) {
		return m, ev, ErrDataset
	}
	classes := map[string]*Class{}
	seen := map[string]bool{}
	evaluation := []Example{}
	bytes := 0
	for _, ex := range examples {
		bytes += len(ex.Text)
		normalized := strings.Join(strings.Fields(strings.ToLower(ex.Text)), " ")
		if !utf8.ValidString(ex.Text) || utf8.RuneCountInString(ex.Text) > 1000 || len(normalized) == 0 || bytes > 512<<10 || !known(ex.Category) || (ex.Split != "train" && ex.Split != "evaluation") || seen[normalized] {
			return m, ev, ErrDataset
		}
		seen[normalized] = true
		tokens := Tokens(ex.Text)
		if len(tokens) == 0 {
			return m, ev, ErrDataset
		}
		if ex.Split == "evaluation" {
			evaluation = append(evaluation, ex)
			continue
		}
		c := classes[ex.Category]
		if c == nil {
			c = &Class{Category: ex.Category, Counts: map[string]int{}}
			classes[ex.Category] = c
		}
		c.Documents++
		m.TrainCount++
		for _, t := range tokens {
			m.Vocabulary[t] = true
			c.Counts[t]++
			c.Total++
		}
	}
	if len(classes) < 2 || m.TrainCount < 6 || len(evaluation) < 2 || len(m.Vocabulary) > 8192 {
		return m, ev, ErrDataset
	}
	for _, cat := range Categories {
		if c := classes[cat]; c != nil {
			if c.Documents < 2 {
				return m, ev, ErrDataset
			}
			m.Classes = append(m.Classes, *c)
		}
	}
	supports := map[string]int{}
	predicted := map[string]int{}
	correct := map[string]int{}
	for _, ex := range evaluation {
		if classes[ex.Category] == nil {
			return m, ev, ErrDataset
		}
		p := m.Predict(ex.Text)
		ev.Count++
		supports[ex.Category]++
		predicted[p.Category]++
		if ev.Confusion[ex.Category] == nil {
			ev.Confusion[ex.Category] = map[string]int{}
		}
		ev.Confusion[ex.Category][p.Category]++
		if p.Category == ex.Category {
			ev.Accuracy++
			correct[ex.Category]++
		}
	}
	ev.Accuracy /= float64(ev.Count)
	for _, cat := range Categories {
		if supports[cat] == 0 {
			continue
		}
		v := ClassMetrics{Category: cat, Support: supports[cat]}
		if predicted[cat] > 0 {
			v.Precision = float64(correct[cat]) / float64(predicted[cat])
		}
		v.Recall = float64(correct[cat]) / float64(supports[cat])
		if v.Precision+v.Recall > 0 {
			v.F1 = 2 * v.Precision * v.Recall / (v.Precision + v.Recall)
		}
		ev.MacroF1 += v.F1
		ev.PerClass = append(ev.PerClass, v)
	}
	ev.MacroF1 /= float64(len(ev.PerClass))
	return m, ev, nil
}

func (m Model) Predict(text string) Prediction {
	out := Prediction{Category: "unknown", Scores: []Score{}, MatchedTokens: []string{}, Reason: "没有与本模型词表相交的元数据特征，保留待人工识别。"}
	for _, t := range Tokens(text) {
		if m.Vocabulary[t] {
			out.MatchedTokens = append(out.MatchedTokens, t)
		}
	}
	if len(out.MatchedTokens) == 0 || len(m.Classes) < 2 || m.TrainCount < 1 {
		return out
	}
	logs := make([]float64, len(m.Classes))
	maxLog := math.Inf(-1)
	for i, c := range m.Classes {
		logs[i] = math.Log(float64(c.Documents) / float64(m.TrainCount))
		for _, t := range out.MatchedTokens {
			logs[i] += math.Log(float64(c.Counts[t]+1) / float64(c.Total+len(m.Vocabulary)))
		}
		maxLog = math.Max(maxLog, logs[i])
	}
	sum := 0.0
	for i := range logs {
		logs[i] = math.Exp(logs[i] - maxLog)
		sum += logs[i]
	}
	for i, c := range m.Classes {
		out.Scores = append(out.Scores, Score{Category: c.Category, RelativeScore: logs[i] / sum})
	}
	sort.Slice(out.Scores, func(i, j int) bool {
		if out.Scores[i].RelativeScore == out.Scores[j].RelativeScore {
			return out.Scores[i].Category < out.Scores[j].Category
		}
		return out.Scores[i].RelativeScore > out.Scores[j].RelativeScore
	})
	out.Reason = "相对模型得分未达到本模型采用阈值，保留待人工识别。"
	if out.Scores[0].RelativeScore >= m.MinScore {
		out.Category = out.Scores[0].Category
		out.Reason = "本地监督模型按已冻结词表给出元数据分类参考；相对得分不是正确概率，须结合来源证据人工确认。"
	}
	if len(out.MatchedTokens) > 30 {
		out.MatchedTokens = out.MatchedTokens[:30]
	}
	return out
}
