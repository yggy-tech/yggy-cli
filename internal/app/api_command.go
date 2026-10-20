package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/yggy-tech/yggy-cli/internal/api"
	"github.com/yggy-tech/yggy-cli/internal/config"
	"github.com/yggy-tech/yggy-cli/internal/ops"
)

// apiCommand runs `yggy api [operation] [args]`.
func (a *App) apiCommand(args []string) int {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" {
		a.listOperations()
		return 0
	}
	op, ok := ops.Find(args[0])
	if !ok {
		fmt.Fprintf(a.Err, "unknown operation %q; run yggy api to list them\n", args[0])
		return 2
	}
	file, code := a.orgContext()
	if code != 0 {
		return code
	}
	req, outPath, err := buildRequest(op, args[1:], file)
	if err != nil {
		fmt.Fprintln(a.Err, err)
		return 2
	}
	out := a.Out
	if outPath != "" {
		f, err := os.Create(outPath)
		if err != nil {
			fmt.Fprintln(a.Err, err)
			return 1
		}
		defer f.Close()
		out = f
	}
	if err := a.client(file).Call(context.Background(), req, out); err != nil {
		fmt.Fprintln(a.Err, err)
		return 1
	}
	return 0
}

func (a *App) listOperations() {
	fmt.Fprint(a.Err, `Usage: yggy api <operation> [name=value ...] [name:=json ...] [field=@file] [--data JSON|@file] [-o file]

Path parameters and query parameters are passed as name=value. projectId
defaults to the selected project. Other name=value pairs become JSON body
fields (name:=json for numbers, booleans, arrays and objects), or form fields
for uploads, where field=@path attaches a file.

Operations:
`)
	w := tabwriter.NewWriter(a.Err, 0, 0, 2, ' ', 0)
	for _, op := range ops.Operations {
		fmt.Fprintf(w, "  %s\t%s %s\t%s\n", op.ID, op.Method, op.Path, op.Summary)
	}
	w.Flush()
}

// buildRequest turns the arguments after the operation id into a request,
// and returns the -o path when one was given.
func buildRequest(op ops.Operation, args []string, file config.File) (api.Request, string, error) {
	req := api.Request{Method: op.Method, Org: file.Org.Header(), Query: url.Values{}}
	pathValues := map[string]string{}
	body := map[string]any{}
	files := map[string]string{}
	var data, outPath string

	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch arg {
		case "--data", "-d", "-o", "--output":
			if i+1 >= len(args) {
				return req, "", fmt.Errorf("%s needs a value", arg)
			}
			i++
			if arg == "-o" || arg == "--output" {
				outPath = args[i]
			} else {
				data = args[i]
			}
			continue
		}
		if name, raw, ok := strings.Cut(arg, ":="); ok && !strings.Contains(name, "=") {
			var value any
			if err := json.Unmarshal([]byte(raw), &value); err != nil {
				return req, "", fmt.Errorf("%s is not valid JSON: %w", name, err)
			}
			body[name] = value
			continue
		}
		name, value, ok := strings.Cut(arg, "=")
		if !ok || name == "" {
			return req, "", fmt.Errorf("expected name=value, got %q", arg)
		}
		switch {
		case contains(op.PathParams, name):
			pathValues[name] = value
		case contains(op.QueryParams, name):
			req.Query.Add(name, value)
		case op.Body == ops.BodyMultipart && strings.HasPrefix(value, "@"):
			files[name] = value[1:]
		case op.Body != ops.BodyNone:
			body[name] = value
		default:
			return req, "", fmt.Errorf("%s does not take %q (path: %s; query: %s)",
				op.ID, name, listOrNone(op.PathParams), listOrNone(op.QueryParams))
		}
	}

	if _, ok := pathValues["projectId"]; !ok && contains(op.PathParams, "projectId") && file.Project.ID != "" {
		pathValues["projectId"] = file.Project.ID
	}
	path := op.Path
	for _, name := range op.PathParams {
		value, ok := pathValues[name]
		if !ok {
			if name == "projectId" {
				return req, "", errors.New("select a project first (yggy use project) or pass projectId=...")
			}
			return req, "", fmt.Errorf("%s needs %s=...", op.ID, name)
		}
		path = strings.ReplaceAll(path, "{"+name+"}", url.PathEscape(value))
	}
	req.Path = path
	for _, name := range op.Required {
		if req.Query.Get(name) == "" {
			return req, "", fmt.Errorf("%s needs %s=...", op.ID, name)
		}
	}

	switch op.Body {
	case ops.BodyJSON:
		payload, err := jsonBody(data, body)
		if err != nil {
			return req, "", err
		}
		if payload != nil {
			req.ContentType = "application/json"
			req.Body = bytes.NewReader(payload)
		}
	case ops.BodyMultipart:
		if data != "" {
			return req, "", errors.New("--data does not apply to uploads; use field=value and file=@path")
		}
		payload, contentType, err := multipartBody(body, files)
		if err != nil {
			return req, "", err
		}
		req.ContentType = contentType
		req.Body = payload
	default:
		if data != "" {
			return req, "", fmt.Errorf("%s takes no request body", op.ID)
		}
	}
	return req, outPath, nil
}

func jsonBody(data string, fields map[string]any) ([]byte, error) {
	if data != "" {
		if len(fields) > 0 {
			return nil, errors.New("use either --data or name=value body fields, not both")
		}
		raw := []byte(data)
		if strings.HasPrefix(data, "@") {
			var err error
			if raw, err = os.ReadFile(data[1:]); err != nil {
				return nil, err
			}
		}
		if !json.Valid(raw) {
			return nil, errors.New("--data is not valid JSON")
		}
		return raw, nil
	}
	if len(fields) == 0 {
		return nil, nil
	}
	return json.Marshal(fields)
}

func multipartBody(fields map[string]any, files map[string]string) (io.Reader, string, error) {
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	for _, name := range sortedKeys(files) {
		f, err := os.Open(files[name])
		if err != nil {
			return nil, "", err
		}
		part, err := writer.CreateFormFile(name, filepath.Base(files[name]))
		if err == nil {
			_, err = io.Copy(part, f)
		}
		f.Close()
		if err != nil {
			return nil, "", err
		}
	}
	for _, name := range sortedKeys(fields) {
		value := fields[name]
		text, ok := value.(string)
		if !ok {
			encoded, err := json.Marshal(value)
			if err != nil {
				return nil, "", err
			}
			text = string(encoded)
		}
		if err := writer.WriteField(name, text); err != nil {
			return nil, "", err
		}
	}
	if err := writer.Close(); err != nil {
		return nil, "", err
	}
	return &buf, writer.FormDataContentType(), nil
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func contains(list []string, value string) bool {
	for _, item := range list {
		if item == value {
			return true
		}
	}
	return false
}

func listOrNone(list []string) string {
	if len(list) == 0 {
		return "none"
	}
	return strings.Join(list, ", ")
}
