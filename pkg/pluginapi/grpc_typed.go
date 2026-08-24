package pluginapi

import (
	"context"
	proto "github.com/Tencent/WeKnora/pkg/pluginapi/proto"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	dataSourceService   = "weknora.plugin.v1.DataSourcePlugin"
	parserService       = "weknora.plugin.v1.ParserPlugin"
	webSearchService    = "weknora.plugin.v1.WebSearchPlugin"
	pluginControlService = "weknora.plugin.v1.PluginControl"
)

// PluginControlClient is the shared control-plane client. Runtimes depend only
// on this interface, never on type-specific clients such as
// DataSourcePluginClient. Handshake and Health are uniform across datasource,
// parser, search, model and retriever plugins.
type PluginControlClient interface {
	Handshake(context.Context, *proto.HandshakeRequest, ...grpc.CallOption) (*proto.HandshakeResponse, error)
	Health(context.Context, *proto.HealthRequest, ...grpc.CallOption) (*proto.HealthResponse, error)
}

type PluginControlServer interface {
	Handshake(context.Context, *proto.HandshakeRequest) (*proto.HandshakeResponse, error)
	Health(context.Context, *proto.HealthRequest) (*proto.HealthResponse, error)
}

type pluginControlClient struct{ cc grpc.ClientConnInterface }

func NewPluginControlClient(cc grpc.ClientConnInterface) PluginControlClient {
	return &pluginControlClient{cc: cc}
}

func (c *pluginControlClient) Handshake(ctx context.Context, in *proto.HandshakeRequest, opts ...grpc.CallOption) (*proto.HandshakeResponse, error) {
	out := new(proto.HandshakeResponse)
	err := c.cc.Invoke(ctx, "/"+pluginControlService+"/Handshake", in, out, opts...)
	return out, err
}

func (c *pluginControlClient) Health(ctx context.Context, in *proto.HealthRequest, opts ...grpc.CallOption) (*proto.HealthResponse, error) {
	out := new(proto.HealthResponse)
	err := c.cc.Invoke(ctx, "/"+pluginControlService+"/Health", in, out, opts...)
	return out, err
}

func RegisterPluginControlServer(r grpc.ServiceRegistrar, s PluginControlServer) {
	r.RegisterService(&pluginControlServiceDesc, s)
}

var pluginControlServiceDesc = grpc.ServiceDesc{
	ServiceName: pluginControlService,
	HandlerType: (*PluginControlServer)(nil),
	Methods: []grpc.MethodDesc{
		{MethodName: "Handshake", Handler: controlHandler(func(s PluginControlServer, c context.Context, in *proto.HandshakeRequest) (any, error) {
			return s.Handshake(c, in)
		}, func() *proto.HandshakeRequest { return &proto.HandshakeRequest{} })},
		{MethodName: "Health", Handler: controlHandler(func(s PluginControlServer, c context.Context, in *proto.HealthRequest) (any, error) {
			return s.Health(c, in)
		}, func() *proto.HealthRequest { return &proto.HealthRequest{} })},
	},
}

func controlHandler[T any](call func(PluginControlServer, context.Context, T) (any, error), newReq func() T) grpc.MethodHandler {
	return func(srv any, ctx context.Context, dec func(any) error, interceptor grpc.UnaryServerInterceptor) (any, error) {
		in := newReq()
		if err := dec(in); err != nil {
			return nil, err
		}
		s := srv.(PluginControlServer)
		if interceptor == nil {
			return call(s, ctx, in)
		}
		info := &grpc.UnaryServerInfo{Server: srv}
		h := func(ctx context.Context, req any) (any, error) { return call(s, ctx, req.(T)) }
		return interceptor(ctx, in, info, h)
	}
}

type DataSourcePluginServer interface {
	Validate(context.Context, *proto.DataSourceRequest) (*proto.DataSourceResponse, error)
	ListResources(context.Context, *proto.DataSourceRequest) (*proto.DataSourceResponse, error)
	ResolveResourceAncestors(context.Context, *proto.DataSourceRequest) (*proto.DataSourceResponse, error)
	FetchAll(context.Context, *proto.DataSourceRequest) (*proto.DataSourceResponse, error)
	FetchIncremental(context.Context, *proto.DataSourceRequest) (*proto.DataSourceResponse, error)
}

type DataSourcePluginClient interface {
	Validate(context.Context, *proto.DataSourceRequest, ...grpc.CallOption) (*proto.DataSourceResponse, error)
	ListResources(context.Context, *proto.DataSourceRequest, ...grpc.CallOption) (*proto.DataSourceResponse, error)
	ResolveResourceAncestors(context.Context, *proto.DataSourceRequest, ...grpc.CallOption) (*proto.DataSourceResponse, error)
	FetchAll(context.Context, *proto.DataSourceRequest, ...grpc.CallOption) (*proto.DataSourceResponse, error)
	FetchIncremental(context.Context, *proto.DataSourceRequest, ...grpc.CallOption) (*proto.DataSourceResponse, error)
}

// DataSourceStreamingPluginServer is optional. Keeping streaming separate
// lets existing unary-only plugins continue to compile and run unchanged.
type DataSourceStreamingPluginServer interface {
	FetchAllStream(context.Context, *proto.DataSourceRequest, DataSourceResponseServer) error
	FetchIncrementalStream(context.Context, *proto.DataSourceRequest, DataSourceResponseServer) error
}

type DataSourceResponseServer interface {
	Send(*proto.DataSourceResponse) error
}

type DataSourceStreamingPluginClient interface {
	FetchAllStream(context.Context, *proto.DataSourceRequest, ...grpc.CallOption) (DataSourceResponseClient, error)
	FetchIncrementalStream(context.Context, *proto.DataSourceRequest, ...grpc.CallOption) (DataSourceResponseClient, error)
}

type DataSourceResponseClient interface {
	Recv() (*proto.DataSourceResponse, error)
}

type dataSourceResponseClient struct{ grpc.ClientStream }

func (s dataSourceResponseClient) Recv() (*proto.DataSourceResponse, error) {
	response := new(proto.DataSourceResponse)
	if err := s.RecvMsg(response); err != nil {
		return nil, err
	}
	return response, nil
}

type dataSourcePluginClient struct{ cc grpc.ClientConnInterface }

func NewDataSourcePluginClient(cc grpc.ClientConnInterface) DataSourcePluginClient {
	return &dataSourcePluginClient{cc: cc}
}
func (c *dataSourcePluginClient) Validate(ctx context.Context, in *proto.DataSourceRequest, opts ...grpc.CallOption) (*proto.DataSourceResponse, error) {
	out := new(proto.DataSourceResponse)
	err := c.cc.Invoke(ctx, "/"+dataSourceService+"/Validate", in, out, opts...)
	return out, err
}
func (c *dataSourcePluginClient) ListResources(ctx context.Context, in *proto.DataSourceRequest, opts ...grpc.CallOption) (*proto.DataSourceResponse, error) {
	out := new(proto.DataSourceResponse)
	err := c.cc.Invoke(ctx, "/"+dataSourceService+"/ListResources", in, out, opts...)
	return out, err
}
func (c *dataSourcePluginClient) ResolveResourceAncestors(ctx context.Context, in *proto.DataSourceRequest, opts ...grpc.CallOption) (*proto.DataSourceResponse, error) {
	out := new(proto.DataSourceResponse)
	err := c.cc.Invoke(ctx, "/"+dataSourceService+"/ResolveResourceAncestors", in, out, opts...)
	return out, err
}
func (c *dataSourcePluginClient) FetchAll(ctx context.Context, in *proto.DataSourceRequest, opts ...grpc.CallOption) (*proto.DataSourceResponse, error) {
	out := new(proto.DataSourceResponse)
	err := c.cc.Invoke(ctx, "/"+dataSourceService+"/FetchAll", in, out, opts...)
	return out, err
}
func (c *dataSourcePluginClient) FetchIncremental(ctx context.Context, in *proto.DataSourceRequest, opts ...grpc.CallOption) (*proto.DataSourceResponse, error) {
	out := new(proto.DataSourceResponse)
	err := c.cc.Invoke(ctx, "/"+dataSourceService+"/FetchIncremental", in, out, opts...)
	return out, err
}

func (c *dataSourcePluginClient) FetchAllStream(ctx context.Context, in *proto.DataSourceRequest, opts ...grpc.CallOption) (DataSourceResponseClient, error) {
	stream, err := c.cc.NewStream(ctx, &dataSourceServiceDesc.Streams[0], "/"+dataSourceService+"/FetchAllStream", opts...)
	if err != nil {
		return nil, err
	}
	if err := stream.SendMsg(in); err != nil {
		return nil, err
	}
	if err := stream.CloseSend(); err != nil {
		return nil, err
	}
	return dataSourceResponseClient{ClientStream: stream}, nil
}

func (c *dataSourcePluginClient) FetchIncrementalStream(ctx context.Context, in *proto.DataSourceRequest, opts ...grpc.CallOption) (DataSourceResponseClient, error) {
	stream, err := c.cc.NewStream(ctx, &dataSourceServiceDesc.Streams[1], "/"+dataSourceService+"/FetchIncrementalStream", opts...)
	if err != nil {
		return nil, err
	}
	if err := stream.SendMsg(in); err != nil {
		return nil, err
	}
	if err := stream.CloseSend(); err != nil {
		return nil, err
	}
	return dataSourceResponseClient{ClientStream: stream}, nil
}
func RegisterDataSourcePluginServer(r grpc.ServiceRegistrar, s DataSourcePluginServer) {
	r.RegisterService(&dataSourceServiceDesc, s)
}

func dataSourceHandler[T any](decode func(any) error, call func(DataSourcePluginServer, context.Context, T) (any, error), newReq func() T) grpc.MethodHandler {
	return func(srv any, ctx context.Context, dec func(any) error, interceptor grpc.UnaryServerInterceptor) (any, error) {
		in := newReq()
		if err := dec(in); err != nil {
			return nil, err
		}
		s := srv.(DataSourcePluginServer)
		if interceptor == nil {
			return call(s, ctx, in)
		}
		info := &grpc.UnaryServerInfo{Server: srv}
		h := func(ctx context.Context, req any) (any, error) { return call(s, ctx, req.(T)) }
		return interceptor(ctx, in, info, h)
	}
}

var dataSourceServiceDesc = grpc.ServiceDesc{ServiceName: dataSourceService, HandlerType: (*DataSourcePluginServer)(nil), Methods: []grpc.MethodDesc{
	{MethodName: "Validate", Handler: dataSourceHandler(func(any) error { return nil }, func(s DataSourcePluginServer, c context.Context, in *proto.DataSourceRequest) (any, error) {
		return s.Validate(c, in)
	}, func() *proto.DataSourceRequest { return &proto.DataSourceRequest{} })},
	{MethodName: "ListResources", Handler: dataSourceHandler(func(any) error { return nil }, func(s DataSourcePluginServer, c context.Context, in *proto.DataSourceRequest) (any, error) {
		return s.ListResources(c, in)
	}, func() *proto.DataSourceRequest { return &proto.DataSourceRequest{} })},
	{MethodName: "ResolveResourceAncestors", Handler: dataSourceHandler(func(any) error { return nil }, func(s DataSourcePluginServer, c context.Context, in *proto.DataSourceRequest) (any, error) {
		return s.ResolveResourceAncestors(c, in)
	}, func() *proto.DataSourceRequest { return &proto.DataSourceRequest{} })},
	{MethodName: "FetchAll", Handler: dataSourceHandler(func(any) error { return nil }, func(s DataSourcePluginServer, c context.Context, in *proto.DataSourceRequest) (any, error) {
		return s.FetchAll(c, in)
	}, func() *proto.DataSourceRequest { return &proto.DataSourceRequest{} })},
	{MethodName: "FetchIncremental", Handler: dataSourceHandler(func(any) error { return nil }, func(s DataSourcePluginServer, c context.Context, in *proto.DataSourceRequest) (any, error) {
		return s.FetchIncremental(c, in)
	}, func() *proto.DataSourceRequest { return &proto.DataSourceRequest{} })},
}, Streams: []grpc.StreamDesc{
	{StreamName: "FetchAllStream", ServerStreams: true, Handler: dataSourceStreamHandler(func(s DataSourceStreamingPluginServer, c context.Context, in *proto.DataSourceRequest, stream DataSourceResponseServer) error {
		return s.FetchAllStream(c, in, stream)
	})},
	{StreamName: "FetchIncrementalStream", ServerStreams: true, Handler: dataSourceStreamHandler(func(s DataSourceStreamingPluginServer, c context.Context, in *proto.DataSourceRequest, stream DataSourceResponseServer) error {
		return s.FetchIncrementalStream(c, in, stream)
	})},
}}

func dataSourceStreamHandler(call func(DataSourceStreamingPluginServer, context.Context, *proto.DataSourceRequest, DataSourceResponseServer) error) grpc.StreamHandler {
	return func(srv any, stream grpc.ServerStream) error {
		in := new(proto.DataSourceRequest)
		if err := stream.RecvMsg(in); err != nil {
			return err
		}
		server, ok := srv.(DataSourceStreamingPluginServer)
		if !ok {
			return status.Error(codes.Unimplemented, "streaming datasource RPC is not implemented")
		}
		return call(server, stream.Context(), in, dataSourceResponseServer{stream})
	}
}

type dataSourceResponseServer struct{ grpc.ServerStream }

func (s dataSourceResponseServer) Send(response *proto.DataSourceResponse) error {
	return s.SendMsg(response)
}

type ParserPluginServer interface {
	Parse(context.Context, *proto.ParserRequest) (*proto.ParserResponse, error)
}
type ParserPluginClient interface {
	Parse(context.Context, *proto.ParserRequest, ...grpc.CallOption) (*proto.ParserResponse, error)
}
type parserPluginClient struct{ cc grpc.ClientConnInterface }

func NewParserPluginClient(cc grpc.ClientConnInterface) ParserPluginClient {
	return &parserPluginClient{cc: cc}
}
func (c *parserPluginClient) Parse(ctx context.Context, in *proto.ParserRequest, o ...grpc.CallOption) (*proto.ParserResponse, error) {
	out := new(proto.ParserResponse)
	err := c.cc.Invoke(ctx, "/"+parserService+"/Parse", in, out, o...)
	return out, err
}
func RegisterParserPluginServer(r grpc.ServiceRegistrar, s ParserPluginServer) {
	r.RegisterService(&parserServiceDesc, s)
}

var parserServiceDesc = grpc.ServiceDesc{ServiceName: parserService, HandlerType: (*ParserPluginServer)(nil), Methods: []grpc.MethodDesc{
	{MethodName: "Parse", Handler: parserHandler(func(s ParserPluginServer, c context.Context, in *proto.ParserRequest) (any, error) {
		return s.Parse(c, in)
	}, func() *proto.ParserRequest { return &proto.ParserRequest{} })},
}}

func parserHandler[T any](call func(ParserPluginServer, context.Context, T) (any, error), newReq func() T) grpc.MethodHandler {
	return func(srv any, ctx context.Context, dec func(any) error, interceptor grpc.UnaryServerInterceptor) (any, error) {
		in := newReq()
		if err := dec(in); err != nil {
			return nil, err
		}
		s := srv.(ParserPluginServer)
		if interceptor == nil {
			return call(s, ctx, in)
		}
		info := &grpc.UnaryServerInfo{Server: srv}
		h := func(ctx context.Context, req any) (any, error) { return call(s, ctx, req.(T)) }
		return interceptor(ctx, in, info, h)
	}
}

type WebSearchPluginServer interface {
	Search(context.Context, *proto.WebSearchRequest) (*proto.WebSearchResponse, error)
}
type WebSearchPluginClient interface {
	Search(context.Context, *proto.WebSearchRequest, ...grpc.CallOption) (*proto.WebSearchResponse, error)
}
type webSearchPluginClient struct{ cc grpc.ClientConnInterface }

func NewWebSearchPluginClient(cc grpc.ClientConnInterface) WebSearchPluginClient {
	return &webSearchPluginClient{cc: cc}
}
func (c *webSearchPluginClient) Search(ctx context.Context, in *proto.WebSearchRequest, o ...grpc.CallOption) (*proto.WebSearchResponse, error) {
	out := new(proto.WebSearchResponse)
	err := c.cc.Invoke(ctx, "/"+webSearchService+"/Search", in, out, o...)
	return out, err
}
func RegisterWebSearchPluginServer(r grpc.ServiceRegistrar, s WebSearchPluginServer) {
	r.RegisterService(&webSearchServiceDesc, s)
}

var webSearchServiceDesc = grpc.ServiceDesc{ServiceName: webSearchService, HandlerType: (*WebSearchPluginServer)(nil), Methods: []grpc.MethodDesc{
	{MethodName: "Search", Handler: searchHandler(func(s WebSearchPluginServer, c context.Context, in *proto.WebSearchRequest) (any, error) {
		return s.Search(c, in)
	}, func() *proto.WebSearchRequest { return &proto.WebSearchRequest{} })},
}}

func searchHandler[T any](call func(WebSearchPluginServer, context.Context, T) (any, error), newReq func() T) grpc.MethodHandler {
	return func(srv any, ctx context.Context, dec func(any) error, interceptor grpc.UnaryServerInterceptor) (any, error) {
		in := newReq()
		if err := dec(in); err != nil {
			return nil, err
		}
		s := srv.(WebSearchPluginServer)
		if interceptor == nil {
			return call(s, ctx, in)
		}
		info := &grpc.UnaryServerInfo{Server: srv}
		h := func(ctx context.Context, req any) (any, error) { return call(s, ctx, req.(T)) }
		return interceptor(ctx, in, info, h)
	}
}
