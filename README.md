# godep

godep provides support for understanding go dependencies. The
current sub commands are:

## graph
graph uses ```go mod graph``` to obtain the module dependency
graph which can be displayed or queried in simple ways.
In particular:

1. the graph can be output as a dot file
2. the graph can be flattened in both 'directions', that
is given a starting point all dependencies or all dependents
can be enumerated as a tree (with cycles detected). The
flattened trees can be queried for the presence of a given
module.

## examples


Dot graph of all dependencies for the current module:
```sh
go run github.com/cosnicolaou/godep graph --dot . > mymodule.dot
```


Simple display of dependency hierarchy:
```sh
go run github.com/cosnicolaou/godep graph query
```

Find all dependencies introduced by golang.org/x/tools:
```sh
go run github.com/cosnicolaou/godep graph query --start=golang.org/x/tools
```

Find all dependents that use golang.org/x/tools:
```sh
go run github.com/cosnicolaou/godep graph query --dependencies=false --start=golang.org/x/tools
```

Find all dependents that use golang.org/x/tools:
```sh
go run github.com/cosnicolaou/godep graph query --dependencies=false --start=golang.org/x/tools
```

Find all dependents that use golang.org/x/tools which occur on a path that includes google.golang.org/grpc:
```sh
go run github.com/cosnicolaou/godep graph query --dependencies=false --start=golang.org/x/tools --contains=google.golang.org/grpc
```



## TODO
1. add a command to display detected cycles rather than just
breaking them
2. add dot output generation for the flattened trees as well
as the graph
3. add an interactive visualizer for dependencies (likely using
d3)
