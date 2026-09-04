package main

import (
	"fmt"
	"os"

	"github.com/scarypheonix/meta/internal/ast"
	"github.com/scarypheonix/meta/internal/check"
	"github.com/scarypheonix/meta/internal/diag"
	"github.com/scarypheonix/meta/internal/parse"
	"github.com/scarypheonix/meta/internal/prelude"
	"github.com/scarypheonix/meta/internal/resolve"
	"github.com/scarypheonix/meta/internal/source"
)

func main() {
	ids := ast.NewIDGen()
	pp := os.Args[1]
	pd, err := os.ReadFile(pp)
	if err != nil {
		panic(err)
	}
	ptree := parse.FileWith(source.NewFile(prelude.Name, string(pd)), diag.New(), ids)

	for _, path := range os.Args[2:] {
		data, err := os.ReadFile(path)
		if err != nil {
			panic(err)
		}
		fmt.Println("== " + path)
		tree := parse.FileWith(source.NewFile(path, string(data)), diag.New(), ids)
		bag := diag.New()
		res := resolve.Program(bag, resolve.Input{File: ptree, Prelude: true}, resolve.Input{File: tree})
		if bag.HasErrors() {
			for _, d := range bag.All() {
				s := d.Primary.Span
				fmt.Printf("error %s %s:%d:%d: %s\n", d.Code, s.File.Name, s.Start, s.End, d.Msg)
			}
			continue
		}
		cbag := diag.New()
		_, lines := check.Trace(cbag, res, ptree, tree)
		for _, d := range cbag.All() {
			s := d.Primary.Span
			fmt.Printf("error %s %s:%d:%d: %s\n", d.Code, s.File.Name, s.Start, s.End, d.Msg)
		}
		for _, l := range lines {
			fmt.Println(l)
		}
	}
}
