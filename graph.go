// Copyright 2019 Cosmos Nicolaou. All rights reserved.
// Use of this source code is governed by the Apache-2.0
// license that can be found in the LICENSE file.

package main

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"text/template"

	"github.com/spf13/cobra"
	"v.io/x/lib/cmd/pflagvar"
)

var graphCmd = &cobra.Command{
	Use:   "graph",
	Short: "module dependency graph related commands",
}

var graphDotCmd = &cobra.Command{
	Use:   "dot",
	Short: "output dependency graph in dot format",
	RunE:  graphDot,
}

var graphQueryCmd = &cobra.Command{
	Use:   "query",
	Short: "query the dependency graph",
	RunE:  graphQuery,
}

type graphStateDef struct {
	Versioned    bool   `graph:"versioned,false,'if set, module versions are tracked'"`
	Start        string `query:"start,,module to start dependency analysis"`
	Dependencies bool   `query:"dependencies,true,set to false to trace dependents rather than dependencies"`
	Contains     string `query:"contains,,specify a module to be found in the dependencie or dependent module paths"`
}

var graphState graphStateDef

func init() {
	rootCmd.AddCommand(graphCmd)
	graphCmd.AddCommand(graphDotCmd)
	graphCmd.AddCommand(graphQueryCmd)

	must(pflagvar.RegisterFlagsInStruct(graphDotCmd.Flags(), "graph", &graphState, nil, nil))
	must(pflagvar.RegisterFlagsInStruct(graphQueryCmd.Flags(), "graph", &graphState, nil, nil))
	must(pflagvar.RegisterFlagsInStruct(graphQueryCmd.Flags(), "query", &graphState, nil, nil))
}

type dependency struct {
	Module, DependsOn string
}

func getRoot(ctx context.Context) (string, error) {
	buf := bytes.NewBuffer(nil)
	cmd := exec.CommandContext(ctx, "go", "list", "-m")
	cmd.Stderr = buf
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("failed to run `go list -m`: %v: %v", buf.String(), err)
	}
	return strings.TrimSuffix(string(output), "\n"), nil
}

func getGraph(ctx context.Context, versioned bool) ([]dependency, map[string]bool, error) {
	// Use go mod graph to get the raw dependencies.
	buf := bytes.NewBuffer(nil)
	cmd := exec.CommandContext(ctx, "go", "mod", "graph")
	cmd.Stderr = buf
	output, err := cmd.Output()
	if err != nil {
		return nil, nil, fmt.Errorf("failed to run `go mod graph`: %v: %v", buf.String(), err)
	}

	scanOutput := func(fn func(a, b string)) error {
		sc := bufio.NewScanner(bytes.NewBuffer(output))
		for sc.Scan() {
			line := sc.Text()
			parts := strings.Split(line, " ")
			if len(parts) != 2 {
				fmt.Fprintf(os.Stderr, "invalid input line: %v\n", line)
				continue
			}
			fn(parts[0], parts[1])
		}
		return sc.Err()
	}

	dependencies := []dependency{}
	unique := map[string]bool{}
	if !versioned {
		// strip all versions and dedup.
		deduped := map[string]bool{}
		err = scanOutput(func(mod, dep string) {
			deduped[stripVersion(mod)+" "+stripVersion(dep)] = true
		})
		for k := range deduped {
			parts := strings.Split(k, " ")
			mod, dep := parts[0], parts[1]
			unique[mod] = true
			unique[dep] = true
			dependencies = append(dependencies, dependency{Module: mod, DependsOn: dep})
		}
	} else {
		err = scanOutput(func(mod, dep string) {
			unique[mod] = true
			unique[dep] = true
			dependencies = append(dependencies, dependency{Module: mod, DependsOn: dep})
		})
	}
	if err != nil {
		return nil, nil, err
	}
	return dependencies, unique, nil
}

func stripVersion(m string) string {
	if idx := strings.Index(m, "@"); idx > 0 {
		return m[:idx]
	}
	return m
}

var graphDotTpl = template.Must(template.New("dot").Parse(`
digraph {
	graph [overlap=false, size=14];
	root="{{.Root}}";
	node [  shape = plaintext, fontname = "Helvetica", fontsize=24];
	"{{.Root}}" [style = filled, fillcolor = "#E94762"];
{{range .Dependencies}}"{{.Module}}" -> "{{.DependsOn}}"
{{end}}
}
`))

func graphDot(cmd *cobra.Command, args []string) error {
	ctx := context.Background()
	root, err := getRoot(ctx)
	if err != nil {
		return err
	}
	dependencies, _, err := getGraph(ctx, graphState.Versioned)
	if err != nil {
		return err
	}
	graph := struct {
		Root         string
		Dependencies []dependency
	}{
		Root:         root,
		Dependencies: dependencies,
	}
	return graphDotTpl.Execute(os.Stdout, &graph)
}

type graphNode struct {
	module       string
	dependencies []*graphNode
	dependents   []*graphNode
}

type graph struct {
	nodes map[string]*graphNode
}

// buildGraph builds the dependency graph, including cycles.
func buildGraph(dependencies []dependency, unique map[string]bool) (*graph, error) {
	nodes := make(map[string]*graphNode, len(unique))
	for k := range unique {
		nodes[k] = &graphNode{
			module: k,
		}
	}
	for _, dep := range dependencies {
		mod := nodes[dep.Module]
		if mod == nil {
			return nil, fmt.Errorf("uncrecognised module: %v", dep.Module)
		}
		dependency := nodes[dep.DependsOn]
		if dependency == nil {
			return nil, fmt.Errorf("uncrecognised module dependency: %v", dep.DependsOn)
		}
		mod.dependencies = append(mod.dependencies, dependency)
		dependency.dependents = append(dependency.dependents, mod)
	}
	return &graph{nodes: nodes}, nil
}

type treeNode struct {
	module   string
	children map[string]*treeNode
}

// given a starting point, create a tree of dependencies from it, taking
// care to detect cycles.
func (gr *graph) dependencyTree(c *treeNode) map[string]*treeNode {
	return gr.flatten(c, func(gn *graphNode) []*graphNode {
		sort.Slice(gn.dependencies, func(i, j int) bool {
			return gn.dependencies[i].module < gn.dependencies[j].module
		})
		return gn.dependencies
	}, map[string]bool{})
}

// given a starting point, create a tree of dependents from it, taking
// care to detect cycles.
func (gr *graph) dependentTree(c *treeNode) map[string]*treeNode {
	return gr.flatten(c, func(gn *graphNode) []*graphNode {
		sort.Slice(gn.dependents, func(i, j int) bool {
			return gn.dependents[i].module < gn.dependents[j].module
		})
		return gn.dependents
	}, map[string]bool{})
}

func (gr *graph) flatten(c *treeNode, follow func(gn *graphNode) []*graphNode, visited map[string]bool) map[string]*treeNode {
	gn := gr.nodes[c.module]
	if gn == nil || visited[c.module] {
		return nil
	}
	visited[c.module] = true
	c.children = map[string]*treeNode{}
	for _, dep := range follow(gn) {
		dt := &treeNode{module: dep.module}
		sdeps := gr.flatten(dt, follow, visited)
		for k, v := range sdeps {
			dt.children[k] = v
		}
		c.children[dep.module] = dt
	}
	return c.children
}

func filter(dt *treeNode, match func(tn *treeNode) bool, matched bool) *treeNode {
	mod := &treeNode{module: dt.module}
	matched = matched || match(dt)
	if matched {
		// should probably copy the subtree for easier maintenance in the
		// future.
		mod.children = dt.children
		return mod
	}
	if len(dt.children) == 0 {
		// we're done, drop this path altogether.
		return nil
	}
	mod.children = map[string]*treeNode{}
	for k, v := range dt.children {
		if m := filter(v, match, matched); m != nil {
			mod.children[k] = m
		}
	}
	if len(mod.children) == 0 {
		return nil
	}
	return mod
}

func (dt *treeNode) print(depth int) {
	if dt == nil {
		return
	}
	fmt.Printf("%v%v\n", strings.Repeat(" ", depth*2), dt.module)
	children := make([]string, 0, len(dt.children))
	for c := range dt.children {
		children = append(children, c)
	}
	sort.Strings(children)
	for _, c := range children {
		dt.children[c].print(depth + 1)
	}
}

func graphQuery(cmd *cobra.Command, args []string) error {
	ctx := context.Background()
	start := graphState.Start
	if len(start) == 0 {
		root, err := getRoot(ctx)
		if err != nil {
			return err
		}
		start = root
	}

	dependencies, unique, err := getGraph(ctx, graphState.Versioned)
	if err != nil {
		return err
	}
	graph, err := buildGraph(dependencies, unique)
	if err != nil {
		return err
	}
	dt := &treeNode{module: start}
	if graphState.Dependencies {
		dt.children = graph.dependencyTree(dt)
	} else {
		dt.children = graph.dependentTree(dt)
	}
	if contains := graphState.Contains; len(contains) > 0 {
		filtered := filter(dt, func(tn *treeNode) bool {
			return tn.module == contains
		}, false)
		filtered.print(0)
		return nil
	}
	dt.print(0)
	return nil
}
