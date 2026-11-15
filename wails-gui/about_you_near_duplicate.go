package main

import (
	"os"
	"path/filepath"
	"strings"
	"unicode"
)

// beliefWordList is the lowercased words of a belief in second person, in
// order, so "Wants X" and "You want X" read as the same idea.
func beliefWordList(text string) []string {
	return strings.FieldsFunc(strings.ToLower(beliefInSecondPerson(text)), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsNumber(r)
	})
}

// beliefFunctionWords carry no idea of their own: "You run the tests before
// every push" and "Runs tests before each push" say one thing. Negations and
// comparison words are not here; beliefsContradict reads them.
var beliefFunctionWords = map[string]bool{
	"you": true, "your": true, "yours": true, "yourself": true, "the": true, "a": true, "an": true,
	"to": true, "of": true, "and": true, "or": true, "for": true, "in": true, "on": true, "at": true,
	"by": true, "with": true, "from": true, "as": true, "is": true, "are": true, "be": true, "it": true,
	"its": true, "that": true, "this": true, "these": true, "those": true, "their": true, "them": true,
	"they": true, "every": true, "each": true, "all": true, "any": true, "some": true, "so": true,
	"also": true, "always": true, "usually": true, "often": true, "typically": true, "generally": true,
}

// beliefStem folds the common English endings, so "runs", "running" and
// "run" are one word. It is deliberately rough: a missed fold only keeps
// two beliefs apart.
func beliefStem(word string) string {
	for _, suffix := range []string{"ing", "ed", "es", "s"} {
		if !strings.HasSuffix(word, suffix) || len(word)-len(suffix) < 3 || suffix == "s" && strings.HasSuffix(word, "ss") {
			continue
		}
		word = strings.TrimSuffix(word, suffix)
		if n := len(word); (suffix == "ing" || suffix == "ed") && n >= 4 && word[n-1] == word[n-2] && !strings.ContainsRune("aeiouls", rune(word[n-1])) {
			word = word[:n-1]
		}
		break
	}
	if len(word) > 3 && strings.HasSuffix(word, "e") {
		word = strings.TrimSuffix(word, "e")
	}
	return word
}

// beliefShape is a belief read once for comparison: its words in order, for
// beliefsContradict, and its folded content words, for the overlap.
type beliefShape struct {
	words   []string
	content map[string]bool
}

func newBeliefShape(text string) beliefShape {
	shape := beliefShape{words: beliefWordList(text), content: map[string]bool{}}
	for _, word := range shape.words {
		if !beliefFunctionWords[word] {
			shape.content[beliefStem(word)] = true
		}
	}
	return shape
}

// beliefNegationWords turn a belief around: "You run the tests before a push"
// and "You never run the tests before a push" share all their other words.
var beliefNegationWords = []string{"not", "never", "no", "without", "don", "doesn", "didn", "isn", "aren", "won", "nothing", "avoid", "avoids", "stop", "stops"}

// beliefComparisonWords order a choice: "bun over npm" and "npm over bun"
// have the same words and say the opposite.
var beliefComparisonWords = map[string]bool{"over": true, "than": true, "instead": true}

// beliefsContradict reports two beliefs whose shared words say opposite
// things: one is negated and the other is not, or a choice is reversed.
func beliefsContradict(a, b string) bool {
	return beliefShapesContradict(newBeliefShape(a), newBeliefShape(b))
}

func beliefShapesContradict(a, b beliefShape) bool {
	left, right := a.words, b.words
	leftSet, rightSet := map[string]bool{}, map[string]bool{}
	for _, word := range left {
		leftSet[word] = true
	}
	for _, word := range right {
		rightSet[word] = true
	}
	for _, word := range beliefNegationWords {
		if leftSet[word] != rightSet[word] {
			return true
		}
	}
	// The words around a shared comparison word must sit on the same sides.
	after := func(words []string, at int) map[string]bool {
		out := map[string]bool{}
		for _, word := range words[at+1:] {
			out[beliefStem(word)] = true
		}
		return out
	}
	for i, word := range left {
		if !beliefComparisonWords[word] {
			continue
		}
		for j, other := range right {
			if other != word {
				continue
			}
			leftAfter, rightAfter := after(left, i), after(right, j)
			for _, w := range left[:i] {
				if w = beliefStem(w); rightAfter[w] && !leftAfter[w] && !beliefComparisonWords[w] && !beliefFunctionWords[w] {
					return true
				}
			}
			for _, w := range right[:j] {
				if w = beliefStem(w); leftAfter[w] && !rightAfter[w] && !beliefComparisonWords[w] && !beliefFunctionWords[w] {
					return true
				}
			}
		}
	}
	return false
}

// nearDuplicateBeliefs reports whether two beliefs say the same thing in nearly
// the same words: the same content words, or at least four each with nearly
// all of them shared. Two beliefs that contradict each other are never the
// same belief.
func nearDuplicateBeliefs(a, b string) bool {
	return nearDuplicateBeliefShapes(newBeliefShape(a), newBeliefShape(b))
}

func nearDuplicateBeliefShapes(a, b beliefShape) bool {
	left, right := a.content, b.content
	if len(left) == 0 || len(right) == 0 {
		return false
	}
	shared := 0
	for word := range left {
		if right[word] {
			shared++
		}
	}
	union := len(left) + len(right) - shared
	switch true {
	case shared != union && (len(left) < 4 || len(right) < 4):
		return false
	case float64(shared)/float64(union) < 0.8:
		return false
	}
	return !beliefShapesContradict(a, b)
}

// nearDuplicateMemoryID finds the stored belief a new one nearly repeats, so
// the new evidence strengthens the belief already there (About you v1.0.1 Y33).
func nearDuplicateMemoryID(title string) string {
	if strings.TrimSpace(title) == "" {
		return ""
	}
	dir, err := memoryDir()
	if err != nil {
		return ""
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			continue
		}
		// A hidden belief is not reinforced: new evidence would stay hidden.
		if learnedHidden(string(data)) {
			continue
		}
		if nearDuplicateBeliefs(title, parseMemoryEntryDocument(string(data)).title()) {
			return strings.TrimSuffix(entry.Name(), ".md")
		}
	}
	return ""
}
