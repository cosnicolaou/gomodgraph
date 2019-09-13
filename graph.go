// Copyright 2019 Cosmos Nicolaou. All rights reserved.
// Use of this source code is governed by the Apache-2.0
// license that can be found in the LICENSE file.

package main

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io/ioutil"
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
	DotFormat    string `dot:"format,,set to a dot output format to run dot internally to generate that format"`
	DotCommand   string `dot:"command,sfdp,command to run to process dot script"`
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
	must(pflagvar.RegisterFlagsInStruct(graphDotCmd.Flags(), "dot", &graphState, nil, nil))
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

func getGraph(ctx context.Context, versioned bool) ([]dependency, map[string]bool, []string, error) {
	// Use go mod graph to get the raw dependencies.
	ordered := []string{}
	buf := bytes.NewBuffer(nil)
	cmd := exec.CommandContext(ctx, "go", "mod", "graph")
	cmd.Stderr = buf
	output, err := cmd.Output()
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to run `go mod graph`: %v: %v", buf.String(), err)
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
	dupOrdered := []string{}
	if !versioned {
		// strip all versions and dedup.
		deduped := map[string]bool{}
		err = scanOutput(func(mod, dep string) {
			mod = stripVersion(mod)
			dep = stripVersion(dep)
			deduped[mod+" "+dep] = true
			dupOrdered = append(dupOrdered, mod)
			dupOrdered = append(dupOrdered, dep)
		})
		for k := range deduped {
			parts := strings.Split(k, " ")
			mod, dep := parts[0], parts[1]
			unique[mod] = true
			unique[dep] = true
			dependencies = append(dependencies, dependency{Module: mod, DependsOn: dep})
		}
		dedup := map[string]bool{}
		for _, m := range dupOrdered {
			if !dedup[m] {
				ordered = append(ordered, m)
			}
			dedup[m] = true
		}
	} else {
		err = scanOutput(func(mod, dep string) {
			if _, ok := unique[mod]; !ok {
				ordered = append(ordered, mod)
			}
			if _, ok := unique[dep]; !ok {
				ordered = append(ordered, dep)
			}
			unique[mod] = true
			unique[dep] = true
			dependencies = append(dependencies, dependency{Module: mod, DependsOn: dep})
		})
	}
	if err != nil {
		return nil, nil, nil, err
	}
	return dependencies, unique, ordered, nil
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
	dependencies, _, _, err := getGraph(ctx, graphState.Versioned)
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
	format := graphState.DotFormat
	if len(format) == 0 {
		// output raw dot format
		return graphDotTpl.Execute(os.Stdout, &graph)
	}

	writeDotFile := func() (string, error) {
		tmpfile, err := ioutil.TempFile("", "dot-*.dot")
		if err != nil {
			return "", err
		}
		defer tmpfile.Close()
		if err := graphDotTpl.Execute(tmpfile, &graph); err != nil {
			os.Remove(tmpfile.Name())
			return tmpfile.Name(), err
		}
		return tmpfile.Name(), nil
	}
	name, err := writeDotFile()
	if err != nil {
		return err
	}
	defer os.Remove(name)
	dotcmd := exec.CommandContext(ctx, graphState.DotCommand, "-T"+format, name)
	dotcmd.Stdout = os.Stdout
	return dotcmd.Run()
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
	Module   string
	Cycle    string
	Children map[string]*treeNode
}

// given a starting point, create a tree of dependencies from it, taking
// care to detect cycles.
func (gr *graph) dependencyTree(c *treeNode) map[string]*treeNode {
	r, _ := gr.flatten(c, func(gn *graphNode) []*graphNode {
		sort.Slice(gn.dependencies, func(i, j int) bool {
			return gn.dependencies[i].module < gn.dependencies[j].module
		})
		return gn.dependencies
	}, map[string]bool{})
	return r
}

// given a starting point, create a tree of dependents from it, taking
// care to detect cycles.
func (gr *graph) dependentTree(c *treeNode) map[string]*treeNode {
	r, _ := gr.flatten(c, func(gn *graphNode) []*graphNode {
		sort.Slice(gn.dependents, func(i, j int) bool {
			return gn.dependents[i].module < gn.dependents[j].module
		})
		return gn.dependents
	}, map[string]bool{})
	return r
}

func (gr *graph) flatten(c *treeNode, follow func(gn *graphNode) []*graphNode, visited map[string]bool) (map[string]*treeNode, bool) {
	gn := gr.nodes[c.Module]
	if cycle := visited[c.Module]; gn == nil || cycle {
		// detected a cycle
		return nil, true
	}
	visited[c.Module] = true
	c.Children = map[string]*treeNode{}
	for _, dep := range follow(gn) {
		dt := &treeNode{Module: dep.module}
		sdeps, cycle := gr.flatten(dt, follow, visited)
		if cycle {
			c.Cycle = dt.Module
		}
		for k, v := range sdeps {
			dt.Children[k] = v
		}
		c.Children[dep.module] = dt
	}
	return c.Children, false
}

func filter(dt *treeNode, match func(tn *treeNode) bool, matched bool) *treeNode {
	mod := &treeNode{Module: dt.Module}
	matched = matched || match(dt)
	if matched {
		// should probably copy the subtree for easier maintenance in the
		// future.
		mod.Children = dt.Children
		return mod
	}
	if len(dt.Children) == 0 {
		// we're done, drop this path altogether.
		return nil
	}
	mod.Children = map[string]*treeNode{}
	for k, v := range dt.Children {
		if m := filter(v, match, matched); m != nil {
			mod.Children[k] = m
		}
	}
	if len(mod.Children) == 0 {
		return nil
	}
	return mod
}

func (dt *treeNode) print(depth int) {
	if dt == nil {
		return
	}
	if cycle := dt.Cycle; len(cycle) > 0 {
		fmt.Printf("%v%v (cycle -> %v)\n", strings.Repeat(" ", depth*2), dt.Module, cycle)
	} else {
		fmt.Printf("%v%v\n", strings.Repeat(" ", depth*2), dt.Module)
	}
	children := make([]string, 0, len(dt.Children))
	for c := range dt.Children {
		children = append(children, c)
	}
	sort.Strings(children)
	for _, c := range children {
		dt.Children[c].print(depth + 1)
	}
}

func runQuery(ctx context.Context, start, contains string, versioned bool) (*treeNode, error) {
	if len(start) == 0 {
		root, err := getRoot(ctx)
		if err != nil {
			return nil, err
		}
		start = root
	}
	dependencies, unique, _, err := getGraph(ctx, versioned)
	if err != nil {
		return nil, err
	}
	graph, err := buildGraph(dependencies, unique)
	if err != nil {
		return nil, err
	}
	dt := &treeNode{Module: start}
	if graphState.Dependencies {
		dt.Children = graph.dependencyTree(dt)
	} else {
		dt.Children = graph.dependentTree(dt)
	}
	if contains := graphState.Contains; len(contains) > 0 {
		filtered := filter(dt, func(tn *treeNode) bool {
			return tn.Module == contains
		}, false)
		return filtered, nil
	}
	return dt, nil
}

func graphQuery(cmd *cobra.Command, args []string) error {
	ctx := context.Background()
	tree, err := runQuery(ctx, graphState.Start, graphState.Contains, graphState.Versioned)
	if err != nil {
		return err
	}
	tree.print(0)
	return nil
}
