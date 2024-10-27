package grpc

import (
	"bytes"
	gocontext "context"
	"reflect"
	"strings"
	"testing"

	"github.com/goccy/go-yaml"
	"github.com/golang/mock/gomock"
	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/sergi/go-diff/diffmatchpatch"
	"github.com/zoncoen/query-go"
	"github.com/zoncoen/scenarigo/context"
	"github.com/zoncoen/scenarigo/internal/mockutil"
	"github.com/zoncoen/scenarigo/internal/queryutil"
	"github.com/zoncoen/scenarigo/internal/testutil"
	"github.com/zoncoen/scenarigo/internal/yamlutil"
	"github.com/zoncoen/scenarigo/reporter"
	testpb "github.com/zoncoen/scenarigo/testdata/gen/pb/test"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	spb "google.golang.org/genproto/googleapis/rpc/status"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/testing/protocmp"
	"google.golang.org/protobuf/types/known/anypb"
)

func TestRequestExtractor(t *testing.T) {
	req := &RequestExtractor{
		Method: "Echo",
		Metadata: metadata.MD{
			"foo": []string{"FOO"},
		},
		Message: map[string]string{"messageBody": "hey"},
	}
	tests := map[string]struct {
		query       string
		expect      any
		expectError string
	}{
		"method": {
			query:  ".method",
			expect: req.Method,
		},
		"metadata": {
			query:  ".metadata.foo[0]",
			expect: "FOO",
		},
		"message": {
			query:  ".message.messageBody",
			expect: "hey",
		},
		"message (backward compatibility)": {
			query:  ".messageBody",
			expect: "hey",
		},
		"not found": {
			query:       ".message.aaa",
			expectError: `".message.aaa" not found`,
		},
	}
	for name, test := range tests {
		test := test
		t.Run(name, func(t *testing.T) {
			q, err := query.ParseString(
				test.query,
				queryutil.Options()...,
			)
			if err != nil {
				t.Fatal(err)
			}
			v, err := q.Extract(req)
			if test.expectError == "" && err != nil {
				t.Fatalf("unexpected error: %s", err)
			}
			if test.expectError != "" {
				if err == nil {
					t.Fatal("no error")
				} else if !strings.Contains(err.Error(), test.expectError) {
					t.Fatalf("expect %q but got %q", test.expectError, err)
				}
			}
			if got, expect := v, test.expect; got != expect {
				t.Fatalf("expect %v but got %v", expect, got)
			}
		})
	}
}

func TestResponseExtractor(t *testing.T) {
	resp := &ResponseExtractor{
		Status: responseStatus{
			Code: "OK",
		},
		Header: &yamlutil.MDMarshaler{
			"foo": []string{"FOO"},
		},
		Trailer: &yamlutil.MDMarshaler{
			"bar": []string{"BAR"},
		},
		Message: map[string]string{"messageBody": "hey"},
	}
	tests := map[string]struct {
		query       string
		expect      any
		expectError string
	}{
		"status": {
			query:  ".status.code",
			expect: resp.Status.Code,
		},
		"header": {
			query:  ".header.foo[0]",
			expect: "FOO",
		},
		"trailer": {
			query:  ".trailer.bar[0]",
			expect: "BAR",
		},
		"message": {
			query:  ".message.messageBody",
			expect: "hey",
		},
		"message (backward compatibility)": {
			query:  ".messageBody",
			expect: "hey",
		},
		"not found": {
			query:       ".message.aaa",
			expectError: `".message.aaa" not found`,
		},
	}
	for name, test := range tests {
		test := test
		t.Run(name, func(t *testing.T) {
			q, err := query.ParseString(
				test.query,
				queryutil.Options()...,
			)
			if err != nil {
				t.Fatal(err)
			}
			v, err := q.Extract(resp)
			if test.expectError == "" && err != nil {
				t.Fatalf("unexpected error: %s", err)
			}
			if test.expectError != "" {
				if err == nil {
					t.Fatal("no error")
				} else if !strings.Contains(err.Error(), test.expectError) {
					t.Fatalf("expect %q but got %q", test.expectError, err)
				}
			}
			if got, expect := v, test.expect; got != expect {
				t.Fatalf("expect %v but got %v", expect, got)
			}
		})
	}
}

func TestRequest_Invoke(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		t.Run("Echo returns no error", func(t *testing.T) {
			req := &testpb.EchoRequest{MessageId: "1", MessageBody: "hello"}
			resp := &testpb.EchoResponse{MessageId: "1", MessageBody: "hello"}

			ctrl := gomock.NewController(t)
			defer ctrl.Finish()
			client := testpb.NewMockTestClient(ctrl)
			client.EXPECT().Echo(gomock.Any(), mockutil.ProtoMessage(req), gomock.Any()).Return(resp, nil)

			r := &Request{
				Client: "{{vars.client}}",
				Method: "Echo",
				Message: yaml.MapSlice{
					yaml.MapItem{Key: "messageId", Value: "1"},
					yaml.MapItem{Key: "messageBody", Value: "hello"},
				},
			}
			ctx := context.FromT(t).WithVars(map[string]interface{}{
				"client": client,
			})
			ctx, result, err := r.Invoke(ctx)
			if err != nil {
				t.Fatalf("unexpected error: %s", err)
			}
			typedResult, ok := result.(*response)
			if !ok {
				t.Fatalf("failed to type conversion from %s to *response", reflect.TypeOf(result))
			}
			message, serr, err := extract(typedResult)
			if err != nil {
				t.Fatalf("unexpected error: %s", err)
			}
			if diff := cmp.Diff(resp, message, protocmp.Transform()); diff != "" {
				t.Errorf("differs: (-want +got)\n%s", diff)
			}
			if serr != nil {
				t.Fatalf("unexpected error: %v", serr)
			}

			// ensure that ctx.WithRequest and ctx.WithResponse are called
			r.Client = ""
			r.Message = req
			if diff := cmp.Diff((*RequestExtractor)(r), ctx.Request(), protocmp.Transform()); diff != "" {
				t.Errorf("differs: (-want +got)\n%s", diff)
			}
			if diff := cmp.Diff((*ResponseExtractor)(typedResult), ctx.Response(), protocmp.Transform(), cmpopts.IgnoreFields(ResponseExtractor{}, "rvalues")); diff != "" {
				t.Errorf("differs: (-want +got)\n%s", diff)
			}
		})
		t.Run("Echo returns error", func(t *testing.T) {
			req := &testpb.EchoRequest{MessageId: "1", MessageBody: "hello"}

			ctrl := gomock.NewController(t)
			defer ctrl.Finish()
			client := testpb.NewMockTestClient(ctrl)
			client.EXPECT().Echo(gomock.Any(), mockutil.ProtoMessage(req), gomock.Any()).Return(nil, status.New(codes.Unauthenticated, "unauthenticated").Err())

			r := &Request{
				Client: "{{vars.client}}",
				Method: "Echo",
				Message: yaml.MapSlice{
					yaml.MapItem{Key: "messageId", Value: "1"},
					yaml.MapItem{Key: "messageBody", Value: "hello"},
				},
			}
			ctx := context.FromT(t).WithVars(map[string]interface{}{
				"client": client,
			})
			ctx, result, err := r.Invoke(ctx)
			if err != nil {
				t.Fatalf("unexpected error: %s", err)
			}
			typedResult, ok := result.(*response)
			if !ok {
				t.Fatalf("failed to type conversion from %s to *response", reflect.TypeOf(result))
			}
			_, serr, err := extract(typedResult)
			if err != nil {
				t.Fatalf("unexpected error: %s", err)
			}

			if serr.Code() != codes.Unauthenticated {
				t.Fatalf("expected code is %s but got %s", codes.Unauthenticated.String(), serr.Code().String())
			}

			// ensure that ctx.WithRequest and ctx.WithResponse are called
			r.Client = ""
			r.Message = req
			if diff := cmp.Diff((*RequestExtractor)(r), ctx.Request(), protocmp.Transform()); diff != "" {
				t.Errorf("differs: (-want +got)\n%s", diff)
			}
		})
	})
	t.Run("failure", func(t *testing.T) {
		tests := map[string]struct {
			vars        map[string]interface{}
			client      string
			method      string
			metadata    map[string]interface{}
			expectError string
		}{
			"no client": {
				expectError: "gRPC client must be specified",
			},
			"client not found": {
				client:      "{{vars.client}}",
				expectError: "failed to get client",
			},
			"nil client": {
				vars: map[string]interface{}{
					"client": nil,
				},
				client:      "{{vars.client}}",
				method:      "Echo",
				expectError: ".client: client {{vars.client}} is invalid",
			},
			"method not found": {
				vars: map[string]interface{}{
					"client": testpb.NewTestClient(nil),
				},
				client:      "{{vars.client}}",
				method:      "NotFound",
				expectError: "method {{vars.client}}.NotFound not found",
			},
			"invalid metadata": {
				vars: map[string]interface{}{
					"client": testpb.NewTestClient(nil),
				},
				method:   "Echo",
				client:   "{{vars.client}}",
				metadata: map[string]interface{}{"a": "{{b}}"},
			},
		}
		for name, tc := range tests {
			tc := tc
			t.Run(name, func(t *testing.T) {
				ctx := context.FromT(t)
				if tc.vars != nil {
					ctx = ctx.WithVars(tc.vars)
				}
				req := &Request{
					Client: tc.client,
					Method: tc.method,
				}
				if tc.metadata != nil {
					req.Metadata = tc.metadata
				}
				_, _, err := req.Invoke(ctx)
				if err == nil {
					t.Fatal("no error")
				}
				if e := err.Error(); !strings.Contains(e, tc.expectError) {
					t.Errorf(`"%s" does not contain "%s"`, e, tc.expectError)
				}
			})
		}
	})
}

func TestRequest_Invoke_Log(t *testing.T) {
	req := &testpb.EchoRequest{MessageId: "1", MessageBody: "hello"}
	resp := &testpb.EchoResponse{MessageId: "1", MessageBody: "hello"}

	tests := map[string]struct {
		err    error
		expect string
	}{
		"success": {
			expect: `
=== RUN   test.yaml
--- PASS: test.yaml (0.00s)
        request:
          method: Echo
          metadata:
            version:
            - 1.0.0
          message:
            messageId: "1"
            messageBody: hello
        response:
          status:
            code: OK
          message:
            messageId: "1"
            messageBody: hello
PASS
ok  	test.yaml	0.000s
`,
		},
		"failure": {
			err: status.FromProto(&spb.Status{
				Code:    int32(codes.InvalidArgument),
				Message: "invalid argument",
				Details: []*anypb.Any{
					mustAny(t,
						&errdetails.LocalizedMessage{
							Locale:  "ja-JP",
							Message: "エラー",
						},
					),
					mustAny(t,
						&errdetails.DebugInfo{
							Detail: "debug",
						},
					),
				},
			}).Err(),
			expect: `
=== RUN   test.yaml
--- PASS: test.yaml (0.00s)
        request:
          method: Echo
          metadata:
            version:
            - 1.0.0
          message:
            messageId: "1"
            messageBody: hello
        response:
          status:
            code: InvalidArgument
            message: invalid argument
            details:
              google.rpc.LocalizedMessage:
                locale: ja-JP
                message: エラー
              google.rpc.DebugInfo:
                detail: debug
          message:
            messageId: "1"
            messageBody: hello
PASS
ok  	test.yaml	0.000s
`,
		},
	}
	for name, test := range tests {
		test := test
		t.Run(name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()
			client := testpb.NewMockTestClient(ctrl)
			client.EXPECT().Echo(gomock.Any(), mockutil.ProtoMessage(req), gomock.Any()).Return(resp, test.err)

			r := &Request{
				Client: "{{vars.client}}",
				Method: "Echo",
				Metadata: map[string]string{
					"version": "1.0.0",
				},
				Message: yaml.MapSlice{
					yaml.MapItem{Key: "messageId", Value: "1"},
					yaml.MapItem{Key: "messageBody", Value: "hello"},
				},
			}

			var b bytes.Buffer
			reporter.Run(func(rptr reporter.Reporter) {
				rptr.Run("test.yaml", func(rptr reporter.Reporter) {
					ctx := context.New(rptr).WithVars(map[string]interface{}{
						"client": client,
					})
					if _, _, err := r.Invoke(ctx); err != nil {
						t.Fatalf("unexpected error: %s", err)
					}
				})
			}, reporter.WithWriter(&b), reporter.WithVerboseLog())

			expect := strings.TrimPrefix(test.expect, "\n")
			if diff := cmp.Diff(expect, testutil.ResetDuration(b.String())); diff != "" {
				t.Errorf("differs (-want +got):\n%s", diff)
			}
		})
	}
}

func TestValidateMethod(t *testing.T) {
	t.Run("valid", func(t *testing.T) {
		method := reflect.ValueOf(testpb.NewTestClient(nil)).MethodByName("Echo")
		if err := validateMethod(method); err != nil {
			t.Fatalf("unexpected error: %s", err)
		}
	})
	t.Run("invalid", func(t *testing.T) {
		tests := map[string]struct {
			method reflect.Value
		}{
			"invalid": {
				method: reflect.Value{},
			},
			"must be func": {
				method: reflect.ValueOf(struct{}{}),
			},
			"nil": {
				method: reflect.ValueOf((func())(nil)),
			},
			"number of arguments must be 3": {
				method: reflect.ValueOf(func() (proto.Message, error) {
					return nil, nil
				}),
			},
			"first argument must be context.Context": {
				method: reflect.ValueOf(func(ctx struct{}, in proto.Message, opts ...grpc.CallOption) (proto.Message, error) {
					return nil, nil
				}),
			},
			"second argument must be proto.Message": {
				method: reflect.ValueOf(func(ctx gocontext.Context, in struct{}, opts ...grpc.CallOption) (proto.Message, error) {
					return nil, nil
				}),
			},
			"third argument must be []grpc.CallOption": {
				method: reflect.ValueOf(func(ctx gocontext.Context, in proto.Message, opts ...struct{}) (proto.Message, error) {
					return nil, nil
				}),
			},
			"number of return values must be 2": {
				method: reflect.ValueOf(func(ctx gocontext.Context, in proto.Message, opts ...grpc.CallOption) {
				}),
			},
			"first return value must be proto.Message": {
				method: reflect.ValueOf(func(ctx gocontext.Context, in proto.Message, opts ...grpc.CallOption) (*struct{}, error) {
					return nil, nil //nolint:nilnil
				}),
			},
			"second return value must be error": {
				method: reflect.ValueOf(func(ctx gocontext.Context, in proto.Message, opts ...grpc.CallOption) (proto.Message, *struct{}) {
					return nil, nil
				}),
			},
		}
		for name, tc := range tests {
			tc := tc
			t.Run(name, func(t *testing.T) {
				if err := validateMethod(tc.method); err == nil {
					t.Fatal("no error")
				}
			})
		}
	})
}

func TestBuildRequestBody(t *testing.T) {
	tests := map[string]struct {
		vars   interface{}
		src    interface{}
		expect *testpb.EchoRequest
		error  bool
	}{
		"empty": {
			expect: &testpb.EchoRequest{},
		},
		"set fields": {
			src: yaml.MapSlice{
				yaml.MapItem{
					Key:   "messageId",
					Value: "1",
				},
				yaml.MapItem{
					Key:   "messageBody",
					Value: "hello",
				},
			},
			expect: &testpb.EchoRequest{
				MessageId:   "1",
				MessageBody: "hello",
			},
		},
		"with vars": {
			vars: map[string]string{
				"body": "hello",
			},
			src: yaml.MapSlice{
				yaml.MapItem{
					Key:   "messageBody",
					Value: "{{vars.body}}",
				},
			},
			expect: &testpb.EchoRequest{
				MessageBody: "hello",
			},
		},
		"unknown field": {
			src: yaml.MapSlice{
				yaml.MapItem{
					Key: "invalid",
				},
			},
			error: true,
		},
	}
	for name, tc := range tests {
		tc := tc
		t.Run(name, func(t *testing.T) {
			ctx := context.FromT(t)
			if tc.vars != nil {
				ctx = ctx.WithVars(tc.vars)
			}
			var req testpb.EchoRequest
			err := buildRequestMsg(ctx, &req, tc.src)
			if err != nil {
				if !tc.error {
					t.Fatalf("unexpected error: %s", err)
				}
				return
			}
			if tc.error {
				t.Fatal("no error")
			}
			if diff := cmp.Diff(tc.expect, &req, protocmp.Transform()); diff != "" {
				t.Errorf("differs: (-want +got)\n%s", diff)
			}
		})
	}
}

func TestMDMarshaler_MarshalYAML(t *testing.T) {
	tests := map[string]struct {
		md       metadata.MD
		expected string
	}{
		"nil": {
			expected: `method: Foo
metadata: {}
`,
		},
		"empty": {
			md: metadata.MD{},
			expected: `method: Foo
metadata: {}
`,
		},
		"no -bin": {
			md: metadata.MD{
				"grpc-status": {codes.Internal.String()},
			},
			expected: `method: Foo
metadata:
  grpc-status:
  - Internal
`,
		},
		"has -bin": {
			md: metadata.MD{
				"grpc-status-details-bin": {"test", string("\xF4\x90\x80\x80")}, // U+10FFFF+1; out of range

			},
			expected: `method: Foo
metadata:
  grpc-status-details-bin:
  - test
  - f4908080
`,
		},
	}
	for name, test := range tests {
		test := test
		t.Run(name, func(t *testing.T) {
			b, err := yaml.Marshal(Request{
				Method:   "Foo",
				Metadata: yamlutil.NewMDMarshaler(test.md),
			})
			if err != nil {
				t.Fatal(err)
			}
			if got, expected := string(b), test.expected; got != expected {
				dmp := diffmatchpatch.New()
				diffs := dmp.DiffMain(expected, got, false)
				t.Errorf("differs:\n%s", dmp.DiffPrettyText(diffs))
			}
		})
	}
}
