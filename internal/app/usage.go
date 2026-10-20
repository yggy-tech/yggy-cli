package app

import (
	"errors"
	"flag"
	"fmt"
	"slices"
)

// commandUsage is the usage of each command, shown by `yggy <command> --help` and when its arguments are wrong.
// `api` lists its own operations instead.
var commandUsage = map[string]string{
	"auth":      "yggy auth login [--base-url URL] [--api-key KEY | --web] [--no-browser]",
	"use":       "yggy use org [name]\n       yggy use project [name]",
	"context":   "yggy context",
	"orgs":      "yggy orgs",
	"projects":  "yggy projects",
	"documents": "yggy documents list [--status STATUS] [--limit N] [--json]\n       yggy documents upload <file>\n       yggy documents wait <id>\n       yggy documents triples <id>",
	"ask":       "yggy ask <question> [--tier fast|balanced|accurate] [--json]",
	"chat":      "yggy chat [--tier fast|balanced|accurate]",
	"query":     "yggy query <question> [--top-k N] [--mode hybrid|lexical|graph] [--include entities,triples,identity] [--json]",
	"update":    "yggy update [--check] [--version X.Y.Z]",
	"version":   "yggy version",
}

func (a *App) usage() {
	fmt.Fprint(a.Err, `yggy — CLI for the yggy API

Usage:
  yggy auth login [--base-url URL] [--api-key KEY | --web] [--no-browser]
  yggy use org [name]
  yggy use project [name]
  yggy context

  yggy orgs                      list the organizations this key can use
  yggy projects                  list the projects of the selected organization
  yggy documents list            list the documents of the selected project
  yggy documents upload <file>
  yggy documents wait <id>
  yggy documents triples <id>

  yggy ask <question>            get a written answer with its sources
  yggy chat                      talk with yggy; each question keeps the conversation so far
  yggy query <question>          find the documents that match a question

  yggy api                       list the operations of the public API
  yggy api <operation> [args]    call one, e.g. yggy api getDocumentStatus id=<id>

  yggy update [--check] [--version X.Y.Z]   update yggy to the latest release
  yggy version

Run yggy <command> --help for the options of one command.

Login stores the API URL and key. In a terminal it offers the browser or an API key; --web
always uses the browser and prints the address when --no-browser is set. use selects an organization or project
that this key can already see. Document commands use the selected project.
ask, chat and query use the selected project, or the whole organization when no project is selected.
`)
}

// commandHelp prints the usage of one command to stdout.
func (a *App) commandHelp(command string) int {
	fmt.Fprintf(a.Out, "usage: %s\n", commandUsage[command])
	return 0
}

// misuse prints the usage of one command to stderr and returns the usage exit status.
func (a *App) misuse(command string) int {
	fmt.Fprintf(a.Err, "usage: %s\n", commandUsage[command])
	return 2
}

// asksForHelp reports whether -h or --help appears among the arguments, before any "--".
func asksForHelp(args []string) bool {
	end := slices.Index(args, "--")
	if end < 0 {
		end = len(args)
	}
	return slices.ContainsFunc(args[:end], func(arg string) bool { return arg == "-h" || arg == "--help" })
}

// newFlags returns a flag set that reports parse errors with the usage of command.
func (a *App) newFlags(command string) *flag.FlagSet {
	fs := flag.NewFlagSet(command, flag.ContinueOnError)
	fs.SetOutput(a.Err)
	fs.Usage = func() { fmt.Fprintf(a.Err, "usage: %s\n", commandUsage[command]) }
	return fs
}

// parseFlags parses flags placed anywhere among the arguments and returns the rest in order,
// so `yggy query what changed --top-k 3` works. Everything after "--" is kept as it is.
func parseFlags(fs *flag.FlagSet, args []string) ([]string, error) {
	var tail []string
	if end := slices.Index(args, "--"); end >= 0 {
		args, tail = args[:end], args[end+1:]
	}
	var rest []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		args = fs.Args()
		if len(args) == 0 {
			return append(rest, tail...), nil
		}
		rest = append(rest, args[0])
		args = args[1:]
	}
}

// flagStatus is the exit status for a parse error. The flag set has already printed it.
func flagStatus(err error) int {
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}
	return 2
}
