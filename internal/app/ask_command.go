package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/yggy-tech/yggy-cli/internal/api"
	"github.com/yggy-tech/yggy-cli/internal/config"
)

const snippetWidth = 160

var (
	chatTiers    = []string{"fast", "balanced", "accurate"}
	queryModes   = []string{"hybrid", "lexical", "graph"}
	queryInclude = []string{"entities", "triples", "identity"}
)

// ask runs `yggy ask <question>`: the graph agent writes an answer and names the documents it used.
func (a *App) ask(args []string) int {
	fs := a.newFlags("ask")
	tier := fs.String("tier", "", "fast, balanced or accurate")
	asJSON := fs.Bool("json", false, "print the API response")
	words, err := parseFlags(fs, args)
	if err != nil {
		return flagStatus(err)
	}
	question := strings.TrimSpace(strings.Join(words, " "))
	if question == "" {
		return a.misuse("ask")
	}
	if code := a.checkTier(*tier); code != 0 {
		return code
	}
	file, code := a.orgContext()
	if code != 0 {
		return code
	}
	result, err := a.sendChat(file, api.ChatRequest{Message: question, Tier: *tier})
	if err != nil {
		fmt.Fprintln(a.Err, err)
		return 1
	}
	if *asJSON {
		return a.printJSON(result.Raw)
	}
	a.printAnswer(result)
	return 0
}

func (a *App) checkTier(tier string) int {
	if tier != "" && !slices.Contains(chatTiers, tier) {
		fmt.Fprintf(a.Err, "--tier is one of %s\n", strings.Join(chatTiers, ", "))
		return 2
	}
	return 0
}

// sendChat asks the agent, with a waiting note on the terminal while it works.
func (a *App) sendChat(file config.File, req api.ChatRequest) (api.ChatResult, error) {
	if a.Interactive {
		fmt.Fprint(a.Err, "thinking…")
		defer fmt.Fprint(a.Err, "\r\033[K")
	}
	return a.client(file).Chat(context.Background(), file.Org.Header(), file.Project.ID, req)
}

// printAnswer prints the agent's answer and, once per document, the sources it cited.
func (a *App) printAnswer(result api.ChatResult) {
	fmt.Fprintln(a.Out, a.render(strings.TrimSpace(result.Response)))
	sources := citedDocuments(result.Citations)
	if len(sources) == 0 {
		return
	}
	fmt.Fprintln(a.Out, "\nSources")
	for i, citation := range sources {
		fmt.Fprintf(a.Out, "  %d. %s\n", i+1, firstNonEmpty(citation.Source, citation.DocumentID))
		if snippet := oneLine(citation.Snippet); snippet != "" {
			fmt.Fprintf(a.Out, "     %s\n", snippet)
		}
	}
}

// query runs `yggy query <question>`: ranked documents with the passage that best matches.
func (a *App) query(args []string) int {
	fs := a.newFlags("query")
	topK := fs.Int("top-k", 0, "how many documents to return, 1 to 20")
	mode := fs.String("mode", "", "hybrid, lexical or graph")
	include := fs.String("include", "", "comma-separated: entities, triples, identity")
	asJSON := fs.Bool("json", false, "print the API response")
	words, err := parseFlags(fs, args)
	if err != nil {
		return flagStatus(err)
	}
	request := api.QueryRequest{Question: strings.TrimSpace(strings.Join(words, " ")), TopK: *topK, Mode: *mode}
	if request.Question == "" {
		return a.misuse("query")
	}
	if *topK < 0 || *topK > 20 {
		fmt.Fprintln(a.Err, "--top-k is between 1 and 20")
		return 2
	}
	if *mode != "" && !slices.Contains(queryModes, *mode) {
		fmt.Fprintf(a.Err, "--mode is one of %s\n", strings.Join(queryModes, ", "))
		return 2
	}
	for _, field := range strings.Split(*include, ",") {
		if field = strings.TrimSpace(field); field == "" {
			continue
		}
		if !slices.Contains(queryInclude, field) {
			fmt.Fprintf(a.Err, "--include takes %s\n", strings.Join(queryInclude, ", "))
			return 2
		}
		request.Include = append(request.Include, field)
	}
	file, code := a.orgContext()
	if code != 0 {
		return code
	}
	result, err := a.client(file).Query(context.Background(), file.Org.Header(), file.Project.ID, request)
	if err != nil {
		fmt.Fprintln(a.Err, err)
		return 1
	}
	if *asJSON {
		return a.printJSON(result.Raw)
	}
	a.printQuery(result)
	return 0
}

func (a *App) printQuery(result api.QueryResult) {
	if len(result.Contexts) == 0 {
		fmt.Fprintln(a.Err, "no documents match. For a written answer, try yggy ask")
		return
	}
	// The passage is only worth a line when it says more than the top title.
	if answer := strings.TrimSpace(result.Answer); answer != "" && answer != strings.TrimSpace(result.Contexts[0].Title) {
		fmt.Fprintf(a.Out, "%s\n\n", answer)
	}
	fmt.Fprintln(a.Out, "Documents")
	for i, match := range result.Contexts {
		fmt.Fprintf(a.Out, "  %d. %s  %.2f  %s\n", i+1, firstNonEmpty(match.Title, "(untitled)"), match.Score, match.DocumentID)
		if snippet := oneLine(match.Snippet); snippet != "" {
			fmt.Fprintf(a.Out, "     %s\n", snippet)
		}
	}
	if len(result.Entities) > 0 {
		fmt.Fprintln(a.Out, "\nEntities")
		for _, entity := range result.Entities {
			fmt.Fprintf(a.Out, "  %s", entity.Label)
			if kind := lastSegment(entity.Type); kind != "" {
				fmt.Fprintf(a.Out, "  (%s)", kind)
			}
			fmt.Fprintf(a.Out, "  %.2f\n", entity.Score)
		}
	}
	if len(result.Triples) > 0 {
		fmt.Fprintln(a.Out, "\nTriples")
		for _, triple := range result.Triples {
			fmt.Fprintf(a.Out, "  %s  %s  %s\n", triple.Subject.Label, triple.Predicate.Label, triple.Object.Label)
		}
	}
}

// orgContext loads the config and requires a selected organization. The project is optional.
func (a *App) orgContext() (config.File, int) {
	file, code := a.load()
	if code != 0 {
		return file, code
	}
	if file.Org.Header() == "" {
		fmt.Fprintln(a.Err, "select an organization first: yggy use org")
		return file, 2
	}
	return file, 0
}

func (a *App) printJSON(body []byte) int {
	var indented bytes.Buffer
	if json.Indent(&indented, body, "", "  ") != nil {
		indented.Reset()
		indented.Write(body)
	}
	indented.WriteByte('\n')
	if _, err := indented.WriteTo(a.Out); err != nil {
		fmt.Fprintln(a.Err, err)
		return 1
	}
	return 0
}

// citedDocuments keeps the first citation of each document, in order.
func citedDocuments(citations []api.ChatCitation) []api.ChatCitation {
	seen := map[string]bool{}
	return slices.DeleteFunc(slices.Clone(citations), func(citation api.ChatCitation) bool {
		if seen[citation.DocumentID] {
			return true
		}
		seen[citation.DocumentID] = true
		return false
	})
}

// oneLine collapses whitespace and shortens text to one terminal line.
func oneLine(text string) string {
	text = strings.Join(strings.Fields(text), " ")
	if runes := []rune(text); len(runes) > snippetWidth {
		return strings.TrimSpace(string(runes[:snippetWidth-1])) + "…"
	}
	return text
}

// lastSegment shortens a class URI such as https://schema.org/Person to Person.
func lastSegment(uri string) string {
	if i := strings.LastIndexAny(uri, "/#"); i >= 0 {
		return uri[i+1:]
	}
	return uri
}
