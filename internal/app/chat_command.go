package app

import (
	"bufio"
	"fmt"
	"strings"

	"github.com/yggy-tech/yggy-cli/internal/api"
)

// chatHistoryLimit is how many earlier messages (questions and answers) each question carries.
const chatHistoryLimit = 20

// chat runs `yggy chat`: one question per line, each sent with the conversation so far.
// /new starts over; /exit, /quit or the end of input stops.
func (a *App) chat(args []string) int {
	fs := a.newFlags("chat")
	tier := fs.String("tier", "", "fast, balanced or accurate")
	rest, err := parseFlags(fs, args)
	if err != nil {
		return flagStatus(err)
	}
	if len(rest) > 0 {
		return a.misuse("chat")
	}
	if code := a.checkTier(*tier); code != 0 {
		return code
	}
	file, code := a.orgContext()
	if code != 0 {
		return code
	}
	if a.Interactive {
		scope := "organization " + file.Org.Label()
		if !file.Project.Empty() {
			scope = "project " + file.Project.Label()
		}
		fmt.Fprintf(a.Err, "Chat with yggy in %s. /new starts over, /exit or Ctrl-D quits.\n", scope)
	}

	lines := bufio.NewScanner(a.In)
	lines.Buffer(make([]byte, 0, 64*1024), 1<<20)
	var history []api.ChatMessage
	status := 0
	for {
		if a.Interactive {
			fmt.Fprint(a.Err, "\n› ")
		}
		if !lines.Scan() {
			if a.Interactive {
				fmt.Fprintln(a.Err)
			}
			if err := lines.Err(); err != nil {
				fmt.Fprintln(a.Err, err)
				return 1
			}
			return status
		}
		question := strings.TrimSpace(lines.Text())
		switch question {
		case "":
			continue
		case "/exit", "/quit":
			return status
		case "/new":
			history = nil
			fmt.Fprintln(a.Err, "new conversation")
			continue
		}
		result, err := a.sendChat(file, api.ChatRequest{Message: question, History: history, Tier: *tier})
		if err != nil {
			// One failed question does not end the conversation.
			fmt.Fprintln(a.Err, err)
			status = 1
			continue
		}
		a.printAnswer(result)
		history = append(history,
			api.ChatMessage{Role: "user", Content: question},
			api.ChatMessage{Role: "assistant", Content: strings.TrimSpace(result.Response)},
		)
		if len(history) > chatHistoryLimit {
			history = history[len(history)-chatHistoryLimit:]
		}
	}
}
