// generated by zenrpc; DO NOT EDIT

package testdata

import (
	"context"
	"encoding/json"

	"github.com/sergeyfast/zenrpc"
	"github.com/sergeyfast/zenrpc/smd"
)

func (ArithService) SMD() smd.ServiceInfo {
	return smd.ServiceInfo{
		Description: "",
		Methods: map[string]smd.Service{
			"Divide": {
				Description: "Divide divides two numbers.",
				Parameters: []smd.JSONSchema{
					{Name: "a", Optional: false, Description: "the a", Type: smd.Integer},
					{Name: "b", Optional: false, Description: "the b", Type: smd.Integer},
				},
				Returns: smd.JSONSchema{
					Type:        smd.Object,
					Description: "result is Quotient, should be named var", // or Quotient docs if return desc
					Optional:    true,
					Properties: map[string]smd.Property{
						"Quo": {Type: smd.Integer, Description: "Quo docs"},
						"rem": {Type: smd.Integer, Description: "rem docs"},
					},
				},
			},
			"Sum": {
				Description: "Sum sums two digits and returns error with error code as result and IP from context.",
				Parameters: []smd.JSONSchema{
					{Name: "a", Optional: false, Type: smd.Integer},
					{Name: "b", Optional: false, Type: smd.Integer},
				},
				Returns: smd.JSONSchema{
					Type: smd.Boolean,
				},
			},
			"Multiply": {
				Description: "Multiply multiples two digits and returns result.",
				Parameters: []smd.JSONSchema{
					{Name: "a", Optional: false, Type: smd.Integer},
					{Name: "b", Optional: false, Type: smd.Integer},
				},
				Returns: smd.JSONSchema{
					Type: smd.Integer,
				},
			},
			"Pow": {
				Description: "Pow returns x**y, the base-x exponential of y. If Exp is not set then default value is 2.",
				Parameters: []smd.JSONSchema{
					{Name: "base", Optional: false, Type: smd.Float},
					{Name: "exp", Optional: true, Type: smd.Float, Default: smd.RawMessageString("2"), Description: "exponent could be empty"},
				},
				Returns: smd.JSONSchema{
					Type: smd.Float,
				},
			},
		},
	}
}

// Invoke is as generated code from zenrpc cmd
func (s ArithService) Invoke(ctx context.Context, method string, params json.RawMessage) zenrpc.Response {
	resp := zenrpc.Response{}

	switch method {

	case "divide":
		// A int `json:"a"`
		var args = struct {
			A int `json:"a"`
			B int `json:"b"`
		}{}

		if err := json.Unmarshal(params, &args); err != nil {
			return zenrpc.NewResponseError(nil, zenrpc.InvalidParams, err.Error(), nil)
		}

		resp.Set(s.Divide(args.A, args.B))

	case "multiply":
		// A int `json:"a"`
		var args = struct {
			A int `json:"a"`
			B int `json:"b"`
		}{}

		if err := json.Unmarshal(params, &args); err != nil {
			return zenrpc.NewResponseError(nil, zenrpc.InvalidParams, err.Error(), nil)
		}

		resp.Set(s.Multiply(args.A, args.B))

	case "pow":
		// A int `json:"a"`
		var args = struct {
			Base float64 `json:"base"`
			Exp  float64 `json:"exp"`
		}{}

		if err := json.Unmarshal(params, &args); err != nil {
			return zenrpc.NewResponseError(nil, zenrpc.InvalidParams, err.Error(), nil)
		}

		resp.Set(s.Pow(args.Base, args.Exp))

	case "sum":
		// A int `json:"a"`
		var args = struct {
			A int `json:"a"`
			B int `json:"b"`
		}{}

		if err := json.Unmarshal(params, &args); err != nil {
			return zenrpc.NewResponseError(nil, zenrpc.InvalidParams, err.Error(), nil)
		}

		resp.Set(s.Sum(ctx, args.A, args.B))

	case "sumtest":
		// A int `json:"a"`
		var args = struct {
		}{}

		if err := json.Unmarshal(params, &args); err != nil {
			return zenrpc.NewResponseError(nil, zenrpc.InvalidParams, err.Error(), nil)
		}

		resp.Set(s.SumTest())

	default:
		resp = zenrpc.NewResponseError(nil, zenrpc.MethodNotFound, "", nil)
	}

	return resp
}
