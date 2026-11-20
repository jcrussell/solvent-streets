package cmdutil

import "strings"

// HelpWidth is the column --help prose is wrapped to.
const HelpWidth = 76

// Reflow re-wraps each paragraph of help prose to width columns. A paragraph
// is a run of lines between blank lines; one whose first line is indented
// (a list or a code block) is kept as written, and a 'quoted command' is
// never split across lines. Help text assembled from a
// template, where a substituted phrase changes a line's length, uses it so
// the generated reference stays evenly wrapped.
func Reflow(text string, width int) string {
	paras := strings.Split(text, "\n\n")
	for i, p := range paras {
		if p == "" || strings.HasPrefix(p, " ") || strings.HasPrefix(p, "\t") {
			continue
		}
		paras[i] = wrapWords(quotedWords(strings.Fields(p)), width)
	}
	return strings.Join(paras, "\n\n")
}

// quotedWords joins the words of each single-quoted span into one word.
func quotedWords(words []string) []string {
	out := make([]string, 0, len(words))
	for i := 0; i < len(words); i++ {
		start := i
		if w := words[i]; strings.HasPrefix(w, "'") && strings.Count(w, "'") == 1 {
			for i+1 < len(words) {
				i++
				if strings.Contains(words[i], "'") {
					break
				}
			}
		}
		out = append(out, strings.Join(words[start:i+1], " "))
	}
	return out
}

func wrapWords(words []string, width int) string {
	var b strings.Builder
	n := 0
	for _, w := range words {
		switch {
		case n == 0:
		case n+1+len(w) > width:
			b.WriteByte('\n')
			n = 0
		default:
			b.WriteByte(' ')
			n++
		}
		b.WriteString(w)
		n += len(w)
	}
	return b.String()
}
