package main

import (
	"context"
	"encoding/json"
	"os"
	"sort"
	"strings"
	"text/template"

	"github.com/spf13/cobra"
	"v.io/x/lib/cmd/pflagvar"
)

var wheelCmd = &cobra.Command{
	Use:   "dependency-wheel",
	Short: "dependency wheel visualization",
	RunE:  dependencyWheel,
}

var itreeCmd = &cobra.Command{
	Use:   "itree",
	Short: "interactive tree visualization",
	RunE:  dependencyTree,
}

func init() {
	graphCmd.AddCommand(wheelCmd)
	graphCmd.AddCommand(itreeCmd)
	must(pflagvar.RegisterFlagsInStruct(wheelCmd.Flags(), "graph", &graphState, nil, nil))
	must(pflagvar.RegisterFlagsInStruct(itreeCmd.Flags(), "graph", &graphState, nil, nil))
}

type dependencyMatrix struct {
	modules      []string
	moduleIndex  map[string]int
	dependencies [][]byte
}

func newDM(modules []string) *dependencyMatrix {
	dm := &dependencyMatrix{
		modules:      make([]string, len(modules)),
		moduleIndex:  make(map[string]int, len(modules)),
		dependencies: make([][]byte, len(modules)),
	}
	copy(dm.modules, modules)
	for i, v := range dm.modules {
		dm.moduleIndex[v] = i
		dm.dependencies[i] = make([]byte, len(modules))
	}
	return dm
}

func (dm *dependencyMatrix) addDeps(deps []dependency) {
	for _, dep := range deps {
		from := dm.moduleIndex[dep.Module]
		to := dm.moduleIndex[dep.DependsOn]
		dm.dependencies[from][to] = 0x1
	}
}

func (dm *dependencyMatrix) moduleNames() string {
	var out strings.Builder
	out.WriteString("['")
	out.WriteString(strings.Join(dm.modules, "','"))
	out.WriteString("']")
	return out.String()
}

func (dm *dependencyMatrix) matrix() string {
	var out strings.Builder
	out.WriteString("[")
	for i := 0; i < len(dm.dependencies); i++ {
		out.WriteString("[")
		for j := 0; j < len(dm.dependencies)-1; j++ {
			if val := dm.dependencies[i][j]; val > 0 {
				out.WriteString("1,")
			} else {
				out.WriteString("0,")
			}
		}
		if val := dm.dependencies[i][len(dm.dependencies)-1]; val > 0 {
			out.WriteString("1]")
		} else {
			out.WriteString("0]")
		}
		if i < len(dm.dependencies)-1 {
			out.WriteString(",\n")
			continue
		}
	}
	out.WriteString("]")
	return out.String()
}

func dependencyWheel(cmd *cobra.Command, args []string) error {
	ctx := context.Background()
	dependencies, _, ordered, err := getGraph(ctx, graphState.Versioned)
	if err != nil {
		return err
	}
	dm := newDM(ordered)
	dm.addDeps(dependencies)
	data := struct {
		Name    string
		Modules string
		Matrix  string
		JS      string
	}{
		Name:    ordered[0],
		Modules: dm.moduleNames(),
		Matrix:  dm.matrix(),
		JS:      dependencyWheelJS,
	}
	return dependencyWheelTmpl.Execute(os.Stdout, &data)
}

/*
<script src="https://stackpath.bootstrapcdn.com/bootstrap/4.3.1/js/bootstrap.min.js" integrity="sha384-JjSmVgyd0p3pXB1rRibZUAYoIIy6OrQ6VrjIEaFf/nJGzIxFDsf4x0xIM+B07jRM" crossorigin="anonymous"></script>
*/
// NOTE, text/template is used instead of html/template to avoid over-escaping
//       the data value.

var dependencyWheelTmpl = template.Must(template.New("wheel").Parse(`<!DOCTYPE html>
<html>
  <head>
	<title>DependencyWheel for {{.Name}}</title>
	<script src="https://d3js.org/d3.v5.min.js"></script>
  </head>
  <body>
 
  <script>
  {{.JS}}
  </script>
   <h2>DependencyWheel for {{.Name}}</h2>
   <div id="chart_placeholder"></div>
   <script>



var data = {
	packageNames: {{.Modules}},
	matrix: {{.Matrix}}
};

var chart = d3.chart.dependencyWheel();
d3.select('#chart_placeholder')
  .datum(data)
  .call(chart);

  </script>
  </body>
</html>
`))

// treeNodeJS is for use with
type treeNodeJS struct {
	Module   string        `json:"name"`
	Cycle    string        `json:"cycle"`
	Children []*treeNodeJS `json:"children,omitempty"`
}

func forJSON(t *treeNode) *treeNodeJS {
	tjs := &treeNodeJS{
		Module: t.Module,
		Cycle:  t.Cycle,
	}
	tjs.Children = make([]*treeNodeJS, 0, len(t.Children))
	for _, v := range t.Children {
		tjs.Children = append(tjs.Children, forJSON(v))
	}
	sort.Slice(tjs.Children, func(i, j int) bool {
		return tjs.Children[i].Module < tjs.Children[j].Module
	})
	return tjs
}

func dependencyTree(cmd *cobra.Command, args []string) error {
	ctx := context.Background()
	tree, err := runQuery(ctx, graphState.Start, graphState.Contains, graphState.Versioned)
	if err != nil {
		return err
	}
	buf, err := json.MarshalIndent(forJSON(tree), "", "  ")
	if err != nil {
		return err
	}
	data := struct {
		Name     string
		TreeData string
		JS       string
	}{
		Name:     graphState.Start,
		TreeData: string(buf),
		JS:       treeJS,
	}
	return dependencyTreeTmpl.Execute(os.Stdout, &data)
}

var dependencyTreeTmpl = template.Must(template.New("wheel").Parse(`<!DOCTYPE html>
<meta charset="utf-8">
<title>DependencyTree for {{.Name}}</title>
<style type="text/css">
  
.node {
  cursor: pointer;
}

.overlay{
  background-color:#EEE;
}
   
.node circle {
  fill: #fff;
  stroke: steelblue;
  stroke-width: 1.5px;
}

.node circleCycle {
	fill: #fff;
	stroke: red;
	stroke-width: 1.5px;
}

.node text {
  font-size:10px; 
  font-family:sans-serif;
}
   
.link {
  fill: none;
  stroke: #ccc;
  stroke-width: 1.5px;
}

.templink {
  fill: none;
  stroke: red;
  stroke-width: 3px;
}

.ghostCircle.show{
  display:block;
}

.ghostCircle, .activeDrag .ghostCircle{
   display: none;
}
</style>
<script src="https://code.jquery.com/jquery-1.10.2.min.js"></script>
<script src="https://d3js.org/d3.v3.min.js"></script>
<body>
    <div id="tree-container"></div>
<script>
{{.JS}}
let treeData = {{.TreeData}};
displayTree(treeData);
</script>
</body>
</html>
`))
