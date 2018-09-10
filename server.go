package zenrpc

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"path"
	"reflect"
	"strings"
	"sync"
	"unicode"

	"github.com/gorilla/websocket"
	"github.com/marusama/kin-openapi/openapi2"
	"github.com/marusama/kin-openapi/openapi2conv"
	"github.com/marusama/kin-openapi/openapi3"
	"github.com/marusama/kin-openapi/openapi3gen"
	"github.com/marusama/zenrpc/smd"
)

type contextKey string

const (
	// defaultBatchMaxLen is default value of BatchMaxLen option in rpc Server options.
	defaultBatchMaxLen = 10

	// defaultTargetURL is default value for SMD target url.
	defaultTargetURL = "/"

	// context key for http.Request object.
	requestKey contextKey = "request"

	// context key for namespace.
	namespaceKey contextKey = "namespace"

	// context key for ID.
	IDKey contextKey = "id"

	// contentTypeJSON is default content type for HTTP transport.
	contentTypeJSON = "application/json"
)

// MiddlewareFunc is a function for executing as middleware.
type MiddlewareFunc func(InvokeFunc) InvokeFunc

// InvokeFunc is a function for processing single JSON-RPC 2.0 Request after validation and parsing.
type InvokeFunc func(context.Context, string, json.RawMessage) Response

// Invoker implements service handler.
type Invoker interface {
	Invoke(ctx context.Context, method string, params json.RawMessage) Response
	SMD() smd.ServiceInfo
}

// Service is as struct for discovering JSON-RPC 2.0 services for zenrpc generator cmd.
type Service struct{}

// Options is options for JSON-RPC 2.0 Server.
type Options struct {
	// BatchMaxLen sets maximum quantity of requests in single batch.
	BatchMaxLen int

	// TargetHost target host.
	TargetHost string

	// TargetURL is RPC endpoint.
	TargetURL string

	// ExposeSMD exposes SMD schema with ?smd GET parameter.
	ExposeSMD bool

	// ExposeSwagger exposes Swagger json (OpenAPI 2) with ?swagger.json GET parameter.
	ExposeSwagger bool

	// ExposeOpenAPI3 exposes OpenAPI 3 with ?openapi3.json GET parameter.
	ExposeOpenAPI3 bool

	// DisableTransportChecks disables Content-Type and methods checks. Use only for development mode.
	DisableTransportChecks bool

	// AllowCORS adds header Access-Control-Allow-Origin with *.
	AllowCORS bool

	// Upgrader sets options for gorilla websocket. If nil, default options will be used
	Upgrader *websocket.Upgrader

	// HideErrorDataField removes data field from response error
	HideErrorDataField bool

	// BuildMethodEndpointPathFunc build method endpoint path
	BuildMethodEndpointPathFunc func(namespace string, method string) string

	// PostProcessSwaggerFunc post process swagger object
	PostProcessSwaggerFunc func(swagger *openapi2.Swagger)

	// PostProcessOpenAPI3Func post process OpenAPI 3 object
	PostProcessOpenAPI3Func func(swagger *openapi3.Swagger)
}

// Server is JSON-RPC 2.0 Server.
type Server struct {
	services   map[string]Invoker
	options    Options
	middleware []MiddlewareFunc
	logger     Printer
}

// NewServer returns new JSON-RPC 2.0 Server.
func NewServer(opts Options) Server {
	// For safety reasons we do not allowing to much requests in batch
	if opts.BatchMaxLen == 0 {
		opts.BatchMaxLen = defaultBatchMaxLen
	}

	if opts.TargetURL == "" {
		opts.TargetURL = defaultTargetURL
	}

	if opts.Upgrader == nil {
		opts.Upgrader = &websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool { return opts.AllowCORS },
		}
	}

	if opts.BuildMethodEndpointPathFunc == nil {
		opts.BuildMethodEndpointPathFunc = func(namespace string, method string) string {
			if namespace == "" {
				return path.Join(opts.TargetURL, method)
			}
			return path.Join(opts.TargetURL, namespace, method)
		}
	}

	return Server{
		services: make(map[string]Invoker),
		options:  opts,
	}
}

// Use registers middleware.
func (s *Server) Use(m ...MiddlewareFunc) {
	s.middleware = append(s.middleware, m...)
}

// Register registers new service for given namespace. For public namespace use empty string.
func (s *Server) Register(namespace string, service Invoker) {
	s.services[strings.ToLower(namespace)] = service
}

// RegisterAll registers all services listed in map.
func (s *Server) RegisterAll(services map[string]Invoker) {
	for ns, srv := range services {
		s.Register(ns, srv)
	}
}

// SetLogger sets logger for debug
func (s *Server) SetLogger(printer Printer) {
	s.logger = printer
}

// process process JSON-RPC 2.0 message, invokes correct method for namespace and returns JSON-RPC 2.0 Response.
func (s *Server) process(ctx context.Context, message json.RawMessage) interface{} {
	requests := []Request{}
	// parsing batch requests
	batch := IsArray(message)

	// making not batch request looks like batch to simplify further code
	if !batch {
		message = append(append([]byte{'['}, message...), ']')
	}

	// unmarshal request(s)
	if err := json.Unmarshal(message, &requests); err != nil {
		return NewResponseError(nil, ParseError, "", nil)
	}

	// if there no requests to process
	if len(requests) == 0 {
		return NewResponseError(nil, InvalidRequest, "", nil)
	} else if len(requests) > s.options.BatchMaxLen {
		return NewResponseError(nil, InvalidRequest, "", "max requests length in batch exceeded")
	}

	// process single request: if request single and not notification  - just run it and return result
	if !batch && requests[0].ID != nil {
		return s.processRequest(ctx, requests[0])
	}

	// process batch requests
	if res := s.processBatch(ctx, requests); len(res) > 0 {
		return res
	}

	return nil
}

// processBatch process batch requests with context.
func (s Server) processBatch(ctx context.Context, requests []Request) []Response {
	reqLen := len(requests)

	// running requests in batch asynchronously
	respChan := make(chan Response, reqLen)

	var wg sync.WaitGroup
	wg.Add(reqLen)

	for _, req := range requests {
		// running request in goroutine
		go func(req Request) {
			if req.ID == nil {
				// ignoring response if request is notification
				wg.Done()
				s.processRequest(ctx, req)
			} else {
				respChan <- s.processRequest(ctx, req)
				wg.Done()
			}
		}(req)
	}

	// waiting to complete
	wg.Wait()
	close(respChan)

	// collecting responses
	responses := make([]Response, 0, reqLen)
	for r := range respChan {
		responses = append(responses, r)
	}

	// no responses -> all requests are notifications
	if len(responses) == 0 {
		return nil
	}
	return responses
}

// processRequest processes a single request in service invoker.
func (s Server) processRequest(ctx context.Context, req Request) Response {
	// checks for json-rpc version and method
	if req.Version != Version || req.Method == "" {
		return NewResponseError(req.ID, InvalidRequest, "", nil)
	}

	// convert method to lower and find namespace
	lowerM := strings.ToLower(req.Method)
	sp := strings.SplitN(lowerM, ".", 2)
	namespace, method := "", lowerM
	if len(sp) == 2 {
		namespace, method = sp[0], sp[1]
	}

	if _, ok := s.services[namespace]; !ok {
		return NewResponseError(req.ID, MethodNotFound, "", nil)
	}

	// set namespace to context
	ctx = newNamespaceContext(ctx, namespace)

	// set id to context
	ctx = newIDContext(ctx, req.ID)

	// set middleware to func
	f := InvokeFunc(s.services[namespace].Invoke)
	for i := len(s.middleware) - 1; i >= 0; i-- {
		f = s.middleware[i](f)
	}

	// invoke func with middleware
	resp := f(ctx, method, req.Params)
	resp.ID = req.ID

	if s.options.HideErrorDataField && resp.Error != nil {
		resp.Error.Data = nil
	}

	return resp
}

func (s Server) printf(format string, v ...interface{}) {
	if s.logger != nil {
		s.logger.Printf(format, v...)
	}
}

// SMD returns Service Mapping Description object with all registered methods.
func (s Server) SMD() smd.Schema {
	sch := smd.Schema{
		Transport:   "POST",
		Envelope:    "JSON-RPC-2.0",
		SMDVersion:  "2.0",
		ContentType: contentTypeJSON,
		Target:      s.options.TargetURL,
		Services:    make(map[string]smd.Service),
	}

	for n, v := range s.services {
		info, namespace := v.SMD(), ""
		if n != "" {
			namespace = n + "."
		}

		for m, d := range info.Methods {
			method := namespace + m
			sch.Services[method] = d
			sch.Description += info.Description // TODO formatting
		}
	}

	return sch
}

// MethodEndpoints returns method endpoints for all registered methods.
func (s *Server) MethodEndpoints() map[string]http.Handler {
	endpoints := make(map[string]http.Handler)

	for namespace, service := range s.services {
		info := service.SMD()
		for method := range info.Methods {
			p := s.options.BuildMethodEndpointPathFunc(namespace, method)
			endpoints[p] = s
		}
	}

	return endpoints
}

// Swagger returns Swagger object (OpenAPI 2) with all registered methods.
func (s Server) Swagger() *openapi2.Swagger {

	swagger, err := openapi2conv.FromV3Swagger(s.OpenAPI3())
	if err != nil {
		return &openapi2.Swagger{
			Swagger: "2.0",
			Info: openapi3.Info{
				Title:       "error",
				Description: err.Error(),
			},
		}
	}
	swagger.Swagger = "2.0"
	swagger.Host = s.options.TargetHost
	swagger.BasePath = s.options.TargetURL
	swagger.Consumes = []string{contentTypeJSON}
	swagger.Produces = []string{contentTypeJSON}

	// workaround: cut basePath from path
	if swagger.BasePath != "" && swagger.BasePath != "/" {
		correctPaths := make(map[string]*openapi2.PathItem, len(swagger.Paths))
		for oldPath, item := range swagger.Paths {
			newPath := oldPath[len(swagger.BasePath):]
			correctPaths[newPath] = item
		}
		swagger.Paths = correctPaths
	}

	// fill description and summary
	for itemPath, item := range swagger.Paths {
		for code, resp := range item.Post.Responses {
			if resp.Description == "" {
				resp.Description = itemPath + "/" + code
			}
		}

		// summary
		item.Post.Summary = strings.Replace(itemPath[1:], "/", ".", -1)
	}

	if s.options.PostProcessSwaggerFunc != nil {
		s.options.PostProcessSwaggerFunc(swagger)
	}
	return swagger
}

// OpenAPI3 returns OpenAPI3 object with all registered methods.
func (s Server) OpenAPI3() *openapi3.Swagger {

	swagger := &openapi3.Swagger{
		OpenAPI: "3.0",
		Paths:   openapi3.Paths{},
		Servers: openapi3.Servers{
			&openapi3.Server{
				URL: s.options.TargetHost,
			},
		},
	}

	for namespace, v := range s.services {
		info := v.SMD()
		srvType := reflect.ValueOf(v).Type()
		ptrSrvType := reflect.PtrTo(srvType)
		for methodName, method := range info.Methods {
			p := s.options.BuildMethodEndpointPathFunc(namespace, methodName)
			rpcMethodName := methodName
			if namespace != "" {
				rpcMethodName = namespace + "." + rpcMethodName
			}

			methodType, ok := srvType.MethodByName(methodName)
			if !ok {
				methodType, _ = ptrSrvType.MethodByName(methodName)
			}
			refMethodType := methodType.Type

			// request
			requestBody := openapi3.NewObjectSchema()
			{
				requestBody.Description = method.Description
				requestBody.Required = []string{"id", "jsonrpc", "method", "params"}
				requestBody.AdditionalPropertiesAllowed = false

				requestBody.WithProperty("id", openapi3.NewStringSchema())
				requestBody.WithProperty("jsonrpc", openapi3.NewStringSchema().WithEnum("2.0"))
				requestBody.WithProperty("method", openapi3.NewStringSchema().WithEnum(rpcMethodName))

				params := openapi3.NewObjectSchema()
				requestBody.WithProperty("params", params)

				requiredParams := make([]string, 0, len(method.Parameters))
				paramOffset := refMethodType.NumIn() - len(method.Parameters)
				for i, p := range method.Parameters {
					paramType := refMethodType.In(paramOffset + i)

					g := openapi3gen.NewGenerator()
					schemaRef, err := g.GenerateSchemaRef(paramType)
					if err != nil {
						panic(err)
					}

					for ref := range g.SchemaRefs {
						ref.Ref = ""
					}

					for name, item := range p.Properties {
						if prop, ok := schemaRef.Value.Properties[name]; ok {
							if len(item.Description) > 0 {
								schema := openapi3.Schema{}
								schema = *prop.Value
								schema.Description = item.Description

								prop = &openapi3.SchemaRef{Ref: prop.Ref, Value: &schema}
								schemaRef.Value.Properties[name] = prop
							}
							// nested item
							setNestedDescription(getDefinitionRef(item.Ref), p.Definitions, prop.Value.Properties)
						}
					}

					if !p.Optional {
						requiredParams = append(requiredParams, p.Name)
					}

					params.WithProperty(p.Name, schemaRef.Value)
				}

				params.Required = requiredParams
			}

			// response
			responseBody := openapi3.NewObjectSchema()
			{
				responseBody.Required = []string{"id", "jsonrpc"}

				responseBody.WithProperty("id", openapi3.NewStringSchema())
				responseBody.WithProperty("jsonrpc", openapi3.NewStringSchema().WithEnum("2.0"))

				responseBody.WithProperty("error", openapi3.NewObjectSchema().
					WithProperty("code", openapi3.NewInt64Schema()).
					WithProperty("message", openapi3.NewStringSchema()))

				var resultSchema *openapi3.Schema
				if refMethodType.NumOut() > 0 {
					resultType := refMethodType.Out(0)
					g := openapi3gen.NewGenerator()
					schemaRef, err := g.GenerateSchemaRef(resultType)
					if err != nil {
						panic(err)
					}
					for ref := range g.SchemaRefs {
						ref.Ref = ""
					}
					resultSchema = schemaRef.Value
				} else {
					resultSchema = openapi3.NewObjectSchema()
				}

				for name, item := range method.Returns.Properties {
					if prop, ok := resultSchema.Properties[name]; ok {
						if len(item.Description) > 0 {
							schema := openapi3.Schema{}
							schema = *prop.Value
							schema.Description = item.Description

							prop = &openapi3.SchemaRef{Ref: prop.Ref, Value: &schema}
							resultSchema.Properties[name] = prop
						}
						// nested item
						setNestedDescription(getDefinitionRef(item.Ref), method.Returns.Definitions, prop.Value.Properties)
					}
				}

				responseBody.WithProperty("result", resultSchema)
			}

			swagger.Paths[p] = &openapi3.PathItem{
				Post: &openapi3.Operation{
					Summary:     method.Description,
					Description: method.Description,
					RequestBody: &openapi3.RequestBodyRef{
						Value: &openapi3.RequestBody{
							Required: true,
							Content:  openapi3.NewContentWithJSONSchema(requestBody),
						},
					},
					Responses: openapi3.Responses{
						"200": &openapi3.ResponseRef{
							Value: openapi3.NewResponse().WithJSONSchema(responseBody),
						},
					},
				},
			}
		}
	}

	if s.options.PostProcessOpenAPI3Func != nil {
		s.options.PostProcessOpenAPI3Func(swagger)
	}
	return swagger
}

// IsArray checks json message if it array or object.
func IsArray(message json.RawMessage) bool {
	for _, b := range message {
		if unicode.IsSpace(rune(b)) {
			continue
		}

		if b == '[' {
			return true
		}
		break
	}

	return false
}

// ConvertToObject converts json array into object using key by index from keys array.
func ConvertToObject(keys []string, params json.RawMessage) (json.RawMessage, error) {
	paramCount := len(keys)

	rawParams := []json.RawMessage{}
	if err := json.Unmarshal(params, &rawParams); err != nil {
		return nil, err
	}

	rawParamCount := len(rawParams)
	if paramCount < rawParamCount {
		return nil, fmt.Errorf("invalid params number, expected %d, got %d", paramCount, len(rawParams))
	}

	buf := bytes.Buffer{}
	if _, err := buf.WriteString(`{`); err != nil {
		return nil, err
	}

	for i, p := range rawParams {
		// Writing key
		if _, err := buf.WriteString(`"` + keys[i] + `":`); err != nil {
			return nil, err
		}

		// Writing value
		if _, err := buf.Write(p); err != nil {
			return nil, err
		}

		// Writing trailing comma if not last argument
		if i != rawParamCount-1 {
			if _, err := buf.WriteString(`,`); err != nil {
				return nil, err
			}
		}

	}
	if _, err := buf.WriteString(`}`); err != nil {
		return nil, err
	}

	return buf.Bytes(), nil
}

// getDefinitionRef get definition reference
func getDefinitionRef(ref string) string {
	lst := strings.Split(ref, "/")
	return lst[len(lst)-1]
}

// setNestedDescription set nested description
func setNestedDescription(ref string, src map[string]smd.Definition, dst map[string]*openapi3.SchemaRef) {
	if len(ref) > 0 {
		for name, item := range src[ref].Properties {
			if prop, ok := dst[name]; ok {
				if len(item.Description) > 0 {
					schema := openapi3.Schema{}
					schema = *prop.Value
					schema.Description = item.Description

					prop = &openapi3.SchemaRef{Ref: prop.Ref, Value: &schema}
					dst[name] = prop
				}
				// nested item
				setNestedDescription(getDefinitionRef(item.Ref), src, prop.Value.Properties)
			}
		}
	}
}

// newRequestContext creates new context with http.Request.
func newRequestContext(ctx context.Context, req *http.Request) context.Context {
	return context.WithValue(ctx, requestKey, req)
}

// RequestFromContext returns http.Request from context.
func RequestFromContext(ctx context.Context) (*http.Request, bool) {
	r, ok := ctx.Value(requestKey).(*http.Request)
	return r, ok
}

// newNamespaceContext creates new context with current method namespace.
func newNamespaceContext(ctx context.Context, namespace string) context.Context {
	return context.WithValue(ctx, namespaceKey, namespace)
}

// NamespaceFromContext returns method's namespace from context.
func NamespaceFromContext(ctx context.Context) string {
	if r, ok := ctx.Value(namespaceKey).(string); ok {
		return r
	}

	return ""
}

// newIDContext creates new context with current request ID.
func newIDContext(ctx context.Context, ID *json.RawMessage) context.Context {
	return context.WithValue(ctx, IDKey, ID)
}

// IDFromContext returns request ID from context.
func IDFromContext(ctx context.Context) *json.RawMessage {
	if r, ok := ctx.Value(IDKey).(*json.RawMessage); ok {
		return r
	}

	return nil
}
