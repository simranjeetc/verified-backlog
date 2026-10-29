package main

import (
	"bytes"
	"embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/template"
)

// Role instructions have one versioned source and travel with installed binaries.
//
//go:embed prompts/v1/*.md.tmpl
var rolePrompts embed.FS

type promptData struct {
	ID, Workspace, OutputDir, Evidence, Checks, Tests, Report, BeforeTree, FrozenTests string
}

// renderRolePrompt validates explicit dispatch facts before cmdPacket opens the verifier window.
// Output directories must already exist and stay outside .backlog (including symlink aliases).
func renderRolePrompt(root string, a args, p packet) (string, string, error) {
	workspace, err := promptDirectory(a.get("workspace"), "workspace")
	if err != nil {
		return "", "", err
	}
	out, err := promptDirectory(a.get("output-dir"), "output-dir")
	if err != nil {
		return "", "", err
	}
	base, err := filepath.Abs(filepath.Join(root, ".backlog"))
	if err != nil {
		return "", "", err
	}
	base, err = filepath.EvalSymlinks(base)
	if err != nil {
		return "", "", err
	}
	rel, err := filepath.Rel(base, out)
	if err != nil {
		return "", "", err
	}
	if rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))) {
		return "", "", refuse("--output-dir must be outside .backlog to preserve R39")
	}
	if !p.item.Opened || p.item.Brief == "" {
		return "", "", refuse("rendered prompts require an opened item with a recorded brief")
	}

	prefix := fmt.Sprintf("%s-a%d", p.item.ID, p.item.Attempt)
	d := promptData{ID: p.item.ID, Workspace: workspace, OutputDir: out, BeforeTree: p.item.BeforeTree, FrozenTests: testPath(p.item.ID, p.item.Attempt)}
	if d.BeforeTree == "" {
		d.BeforeTree = "unavailable (not recorded; reconcile baseline before asserting hygiene)"
	}
	d.Evidence = filepath.Join(out, prefix+"-evidence.md")
	d.Checks = filepath.Join(out, prefix+"-checks.tsv")
	d.Tests = filepath.Join(out, prefix+"-tests.py")
	d.Report = filepath.Join(out, fmt.Sprintf("%s-verify-r%d.md", prefix, len(p.item.attemptRounds())+1))
	src, err := rolePrompts.ReadFile("prompts/v1/" + p.role + ".md.tmpl")
	if err != nil {
		return "", "", err
	}
	t, err := template.New(p.role).Option("missingkey=error").Parse(string(src))
	if err != nil {
		return "", "", err
	}
	var b bytes.Buffer
	fmt.Fprintf(&b, "# Role prompt v1: %s (%s)\nWorkspace: %s\nChange directory with: cd -- %s\nOutput directory: %s\n", p.item.ID, p.role, workspace, supervisorQuote(workspace), out)
	if err := t.Execute(&b, d); err != nil {
		return "", "", err
	}
	if path := a.get("required-cases-file"); path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			return "", "", refuse("--required-cases-file: %v", err)
		}
		fmt.Fprintln(&b, "\n## Explicit required cases")
		writePacketText(&b, string(data))
	}
	fmt.Fprintln(&b, "\n## Current item packet")
	p.printTo(&b)
	dest := filepath.Join(out, prefix+"-"+p.role+"-prompt.md")
	return b.String(), dest, nil
}

func promptDirectory(path, flag string) (string, error) {
	if path == "" {
		return "", refuse("--%s is required with --render", flag)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", refuse("--%s: %v", flag, err)
	}
	info, err := os.Stat(real)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", refuse("--%s must name an existing directory", flag)
	}
	return real, nil
}

// writeRolePrompt replaces a prompt atomically without following an existing file symlink.
func writeRolePrompt(path, text string) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".bl-prompt-*")
	if err != nil {
		return err
	}
	temp := f.Name()
	defer os.Remove(temp)
	if _, err := f.WriteString(text); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(temp, path)
}
