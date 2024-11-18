package proto

import (
	"context"
	"testing"

	"github.com/google/go-cmp/cmp"
	"google.golang.org/protobuf/reflect/protoreflect"
)

func TestCompiler(t *testing.T) {
	tests := map[string]struct {
		imports []string
		files   []string
		service string
	}{
		"only files": {
			files: []string{
				"./testdata/foo.proto",
			},
			service: "scenarigo.testdata.foo.Foo",
		},
		"with imports": {
			imports: []string{
				"./testdata",
			},
			files: []string{
				"bar.proto",
			},
			service: "scenarigo.testdata.bar.Bar",
		},
	}
	for name, test := range tests {
		test := test
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			c := NewCompiler(test.imports)
			fds, err := c.Compile(ctx, test.files)
			if err != nil {
				t.Fatalf("failed to compile: %s", err)
			}

			names, err := fds.ListServices()
			if err != nil {
				t.Fatalf("failed to get services: %s", err)
			}
			if diff := cmp.Diff([]protoreflect.FullName{protoreflect.FullName(test.service)}, names); diff != "" {
				t.Fatalf("request differs (-want +got):\n%s", diff)
			}

			if _, err := fds.ResolveService(protoreflect.FullName(test.service)); err != nil {
				t.Fatalf("failed to get service: %s", err)
			}
		})
	}
}
