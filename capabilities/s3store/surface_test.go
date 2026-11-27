package s3store_test

// The package exports no call but a list, a constructor and the error methods, and no field through which a caller could reach the S3 client underneath.

import (
	"go/importer"
	"go/token"
	"go/types"
	"strings"
	"testing"
)

const packagePath = "github.com/mitstack-eu/block-kit/capabilities/s3store"

var (
	errorMethods = map[string]bool{"Error": true, "Is": true, "Unwrap": true}
	errorType    = types.Universe.Lookup("error").Type().Underlying().(*types.Interface)
)

func TestPackage_ExportsNoCallButAList(t *testing.T) {
	pkg, err := importer.ForCompiler(token.NewFileSet(), "source", nil).Import(packagePath)
	if err != nil {
		t.Fatalf("type-checking %s: %v", packagePath, err)
	}

	var refused []string
	for _, name := range pkg.Scope().Names() {
		refused = append(refused, refusedExports(pkg.Scope().Lookup(name))...)
	}

	if len(refused) > 0 {
		t.Errorf("%s exports more than list calls:\n%s", packagePath, strings.Join(refused, "\n"))
	}
}

func refusedExports(obj types.Object) []string {
	if !obj.Exported() {
		return nil
	}
	switch obj := obj.(type) {
	case *types.Func:
		if obj.Name() != "New" {
			return []string{"function " + obj.Name()}
		}
	case *types.Var:
		if !types.Identical(obj.Type(), types.Universe.Lookup("error").Type()) {
			return []string{"variable " + obj.Name() + " of type " + obj.Type().String()}
		}
	case *types.TypeName:
		return refusedMembers(obj)
	}
	return nil
}

// refusedMembers names each exported method of t or *t that is no list call, outside an error's own methods, and each exported field whose type is neither plain data nor declared in this package.
func refusedMembers(obj *types.TypeName) []string {
	var refused []string
	ptr := types.NewPointer(obj.Type())
	isError := types.Implements(ptr, errorType)
	methods := types.NewMethodSet(ptr)
	for i := range methods.Len() {
		name := methods.At(i).Obj().Name()
		if !token.IsExported(name) || strings.HasPrefix(name, "List") || (isError && errorMethods[name]) {
			continue
		}
		refused = append(refused, "method "+obj.Name()+"."+name)
	}
	st, ok := obj.Type().Underlying().(*types.Struct)
	if !ok {
		return refused
	}
	for i := range st.NumFields() {
		field := st.Field(i)
		if field.Exported() && !plainData(field.Type()) {
			refused = append(refused, "field "+obj.Name()+"."+field.Name()+" of type "+field.Type().String())
		}
	}
	return refused
}

// plainData reports whether t is a basic type, a time.Time or a type of this package, or a pointer or slice of one.
func plainData(t types.Type) bool {
	switch t := t.(type) {
	case *types.Basic:
		return true
	case *types.Pointer:
		return plainData(t.Elem())
	case *types.Slice:
		return plainData(t.Elem())
	case *types.Named:
		if t.Obj().Pkg() == nil {
			return false
		}
		path := t.Obj().Pkg().Path()
		return path == packagePath || (path == "time" && t.Obj().Name() == "Time")
	}
	return false
}
