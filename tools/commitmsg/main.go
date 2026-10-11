// SPDX-FileCopyrightText: 2026 lusoris <lusoris@pm.me>
//
// SPDX-License-Identifier: EUPL-1.2

// Command commitmsg refuses a commit message or a pull request text that names a model or
// a coding tool as an author of the change. The CI job runs it on the commits a pull
// request adds and on the commits a push brings to the default branch:
//
//	git log -z --format='%H%n%B' <base>..<head> > messages
//	go run ./tools/commitmsg -messages=messages [-text=<pull request text>]
//
// Two shapes are refused, on any line of a message. A trailer whose key ends in "-by" or
// "-with", such as a co-author trailer, that names one of the tools below in its key or
// its value;
// and a line that says something was generated, created or written with or by one of
// them, the tool named after those words. A message may still write about such a tool in
// its prose; a sentence of that second shape has to be worded another way.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
)

const (
	maxInputBytes = 8 << 20
	maxMessages   = 10000
	maxLines      = 200000
	textLabel     = "the pull request text"
)

var (
	// toolName matches a model, its vendor or a coding tool as a whole word.
	toolName = regexp.MustCompile(`(?i)\b(aider|anthropic|antigravity|chatgpt|claude|codeium|codex|copilot|cursor|deepseek|devin|gemini|gpt|grok|llama|mistral|ollama|openai|opencode|qwen|tabnine|windsurf)\b`)
	// trailer matches "Co-authored-by: value", "Assisted-by: value", "Generated-with: value".
	trailer = regexp.MustCompile(`(?i)^\s*([a-z][a-z-]*-(?:by|with))\s*:`)
	// madeWith matches "Generated with", "created by", "written using" anywhere in a line.
	madeWith = regexp.MustCompile(`(?i)\b(generated|created|written|authored|produced)\s+(with|by|using|via)\b`)
)

type finding struct {
	label  string
	line   int
	reason string
	text   string
}

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "commitmsg:", err)
		os.Exit(1)
	}
}

func run(args []string, out io.Writer) error {
	flags := flag.NewFlagSet("commitmsg", flag.ContinueOnError)
	messages := flags.String("messages", "", "file of commit messages as git log -z --format='%H%n%B' writes them")
	text := flags.String("text", "", "file that holds a pull request text to check as well")
	if err := flags.Parse(args); err != nil {
		return fmt.Errorf("parse flags: %w", err)
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected argument %q", flags.Arg(0))
	}
	if *messages == "" {
		return errors.New("-messages is required")
	}
	body, err := readBounded(*messages)
	if err != nil {
		return err
	}
	records, err := split(body)
	if err != nil {
		return fmt.Errorf("%s: %w", *messages, err)
	}
	if *text != "" {
		prText, err := readBounded(*text)
		if err != nil {
			return err
		}
		records = append(records, textLabel+"\n"+prText)
	}
	found, err := checkAll(records)
	if err != nil {
		return err
	}
	return report(out, len(records), found)
}

func report(out io.Writer, checked int, found []finding) error {
	for i := 0; i < len(found); i++ {
		f := found[i]
		if _, err := fmt.Fprintf(out, "%s: line %d: %s: %s\n", f.label, f.line, f.reason, f.text); err != nil {
			return fmt.Errorf("write finding: %w", err)
		}
	}
	if len(found) > 0 {
		return fmt.Errorf("%d of %d texts name a model or a coding tool as an author", countLabels(found), checked)
	}
	if _, err := fmt.Fprintf(out, "%d texts checked, none names a model or a coding tool as an author\n", checked); err != nil {
		return fmt.Errorf("write result: %w", err)
	}
	return nil
}

func countLabels(found []finding) int {
	labels := make(map[string]struct{}, len(found))
	for i := 0; i < len(found); i++ {
		labels[found[i].label] = struct{}{}
	}
	return len(labels)
}

func readBounded(path string) (string, error) {
	// #nosec G304 -- the path is a flag the caller supplies directly.
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open: %w", err)
	}
	body, err := io.ReadAll(io.LimitReader(file, maxInputBytes+1))
	if closeErr := file.Close(); err == nil && closeErr != nil {
		err = closeErr
	}
	if err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	if len(body) > maxInputBytes {
		return "", fmt.Errorf("read %s: larger than %d bytes", path, maxInputBytes)
	}
	return string(body), nil
}

// split returns the records of a NUL-separated git log. A log without a record is an
// error: a range that names no commit would otherwise pass as "nothing found".
func split(log string) ([]string, error) {
	parts := strings.Split(log, "\x00")
	if len(parts) > maxMessages+1 {
		return nil, fmt.Errorf("more than %d commit messages", maxMessages)
	}
	records := make([]string, 0, len(parts))
	for i := 0; i < len(parts); i++ {
		record := strings.TrimLeft(parts[i], "\n")
		if strings.TrimSpace(record) == "" {
			continue
		}
		records = append(records, record)
	}
	if len(records) == 0 {
		return nil, errors.New("no commit message read; the range names no commit")
	}
	return records, nil
}

func checkAll(records []string) ([]finding, error) {
	var found []finding
	for i := 0; i < len(records); i++ {
		label, message, _ := strings.Cut(records[i], "\n")
		more, err := check(strings.TrimSpace(label), message)
		if err != nil {
			return nil, err
		}
		found = append(found, more...)
	}
	return found, nil
}

// check returns one finding per line of a message that names a tool as an author. Line
// numbers count from the first line of the message.
func check(label string, message string) ([]finding, error) {
	lines := strings.Split(message, "\n")
	if len(lines) > maxLines {
		return nil, fmt.Errorf("%s: more than %d lines", label, maxLines)
	}
	var found []finding
	for i := 0; i < len(lines); i++ {
		line := strings.TrimRight(lines[i], " \t\r")
		if reason := refusal(line); reason != "" {
			found = append(found, finding{label: label, line: i + 1, reason: reason, text: line})
		}
	}
	return found, nil
}

// refusal says why a line is refused, or returns "" for a line that may stay.
func refusal(line string) string {
	if !toolName.MatchString(line) {
		return ""
	}
	if parts := trailer.FindStringSubmatch(line); parts != nil {
		return "trailer " + parts[1] + " names a model or a coding tool"
	}
	if at := madeWith.FindStringIndex(line); at != nil && toolName.MatchString(line[at[1]:]) {
		return "line credits a model or a coding tool with the change"
	}
	return ""
}
