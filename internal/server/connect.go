package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/daidr/Ariadne/internal/domainerr"
	ariadnev1 "github.com/daidr/Ariadne/internal/gen/proto/ariadne/v1"
)

const (
	rpcPrefix            = "/api/rpc"
	rpcMaxMessageBytes   = 80 * 1024 * 1024
	rpcStreamWriteWindow = 10 * time.Second
)

type fileCookieContextKey struct{}
type responseControllerContextKey struct{}

func (api *API) rpcHandler() http.Handler {
	stripped := http.StripPrefix(rpcPrefix, api.rpcMux)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie := api.auth.FileSessionCookie(r)
		rc := http.NewResponseController(w)
		_ = rc.SetWriteDeadline(time.Time{})
		guard := &streamWriteGuard{rc: rc}
		ctx := context.WithValue(r.Context(), fileCookieContextKey{}, cookie)
		ctx = context.WithValue(ctx, responseControllerContextKey{}, guard)
		stripped.ServeHTTP(w, r.WithContext(ctx))
		// Clear any bounded close deadline once Connect has flushed its response so
		// healthy keep-alive connections never inherit a stale deadline on reuse.
		if !guard.writeFailed {
			_ = rc.SetWriteDeadline(time.Time{})
		}
	})
}

type streamWriteGuard struct {
	rc          *http.ResponseController
	writeFailed bool
}

func (api *API) streamWriteGuard(ctx context.Context) *streamWriteGuard {
	if guard, ok := ctx.Value(responseControllerContextKey{}).(*streamWriteGuard); ok && guard != nil {
		return guard
	}
	return &streamWriteGuard{}
}

func (g *streamWriteGuard) Send(send func() error) error {
	if g.rc != nil {
		if err := g.rc.SetWriteDeadline(time.Now().Add(rpcStreamWriteWindow)); err != nil && !errors.Is(err, http.ErrNotSupported) {
			g.writeFailed = true
			return err
		}
	}
	if err := send(); err != nil {
		// Leave the current (or expired) write deadline intact so Connect's error
		// frame / EndStreamResponse flush cannot block indefinitely on a stalled peer.
		g.writeFailed = true
		return err
	}
	if g.rc != nil {
		_ = g.rc.SetWriteDeadline(time.Time{})
	}
	return nil
}

// ArmCloseDeadline ensures Connect's final EndStreamResponse write has a bounded
// write deadline when exiting a streaming RPC with an error (e.g. ErrSlowConsumer),
// without extending an already-expired deadline after a failed Send.
func (g *streamWriteGuard) ArmCloseDeadline() {
	if g.rc != nil && !g.writeFailed {
		_ = g.rc.SetWriteDeadline(time.Now().Add(rpcStreamWriteWindow))
	}
}

func (api *API) mountConnect(
	service protoreflect.ServiceDescriptor,
	mount func(...connect.HandlerOption) (string, http.Handler),
) {
	if service == nil {
		panic("mountConnect requires a non-nil protobuf service descriptor")
	}
	pattern, handler := mount(api.connectHandlerOptions()...)
	api.rpcMux.Handle(pattern, handler)
	methods := service.Methods()
	for i := range methods.Len() {
		method := methods.Get(i)
		api.rpcProcedures = append(api.rpcProcedures, fmt.Sprintf("/%s/%s", service.FullName(), method.Name()))
	}
}

func (api *API) connectHandlerOptions() []connect.HandlerOption {
	return []connect.HandlerOption{
		connect.WithReadMaxBytes(rpcMaxMessageBytes),
		connect.WithInterceptors(
			api.connectAuthInterceptor(),
			api.connectServerInterceptor(),
		),
	}
}

type connectAuthInterceptor struct {
	api *API
}

func (api *API) connectAuthInterceptor() connect.Interceptor {
	return &connectAuthInterceptor{api: api}
}

func (i *connectAuthInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		if !i.api.auth.ValidBearerHeader(req.Header()) {
			return nil, i.api.toConnectError(domainerr.New("error.unauthorized", nil))
		}
		return next(ctx, req)
	}
}

func (i *connectAuthInterceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

func (i *connectAuthInterceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		if !i.api.auth.ValidBearerHeader(conn.RequestHeader()) {
			return i.api.toConnectError(domainerr.New("error.unauthorized", nil))
		}
		return next(ctx, conn)
	}
}

type connectServerInterceptor struct {
	api *API
}

func (api *API) connectServerInterceptor() connect.Interceptor {
	return &connectServerInterceptor{api: api}
}

func (i *connectServerInterceptor) applyResponseHeaders(ctx context.Context, header http.Header) {
	header.Set("Cache-Control", "no-store")
	header.Set("X-Ariadne-Server-Time", strconv.FormatInt(i.api.now().UnixMilli(), 10))
	if cookie, ok := ctx.Value(fileCookieContextKey{}).(*http.Cookie); ok && cookie != nil {
		header.Add("Set-Cookie", cookie.String())
	}
}

func (i *connectServerInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		resp, err := next(ctx, req)
		if err != nil {
			return nil, i.api.toConnectError(err)
		}
		if resp != nil {
			i.applyResponseHeaders(ctx, resp.Header())
		}
		return resp, nil
	}
}

func (i *connectServerInterceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

func (i *connectServerInterceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		i.applyResponseHeaders(ctx, conn.ResponseHeader())
		if err := next(ctx, conn); err != nil {
			return i.api.toConnectError(err)
		}
		return nil
	}
}

func (api *API) toConnectError(err error) error {
	if err == nil {
		return nil
	}
	var connectErr *connect.Error
	switch {
	case errors.As(err, &connectErr):
	case errors.Is(err, context.Canceled):
		connectErr = connect.NewError(connect.CodeCanceled, err)
	case errors.Is(err, context.DeadlineExceeded):
		connectErr = connect.NewError(connect.CodeDeadlineExceeded, err)
	default:
		var localized *domainerr.Error
		code := connect.CodeInternal
		if errors.As(err, &localized) {
			switch localized.Key {
			case "error.unauthorized":
				code = connect.CodeUnauthenticated
			case "error.notFound", "error.priceUnknown":
				code = connect.CodeNotFound
			case "error.artifactConflict", "updates.error.busy", "updates.error.unavailable":
				code = connect.CodeAborted
			case "error.modelCheck", "error.priceCatalog":
				code = connect.CodeUnavailable
			default:
				code = connect.CodeInvalidArgument
			}
		} else {
			api.report(err)
			localized = domainerr.New("error.invalidBody", object{"reason": "internal"})
		}
		connectErr = connect.NewError(code, errors.New(localized.Key))
		detailMsg := &ariadnev1.LocalizedError{
			Key:    localized.Key,
			Params: encodeErrorParams(localized.Params),
		}
		if detail, detailErr := connect.NewErrorDetail(detailMsg); detailErr == nil {
			connectErr.AddDetail(detail)
		}
	}
	connectErr.Meta().Set("Cache-Control", "no-store")
	connectErr.Meta().Set("X-Ariadne-Server-Time", strconv.FormatInt(api.now().UnixMilli(), 10))
	return connectErr
}

func encodeErrorParams(params map[string]any) map[string]*ariadnev1.ErrorParamValue {
	if len(params) == 0 {
		return nil
	}
	encoded := make(map[string]*ariadnev1.ErrorParamValue, len(params))
	for key, raw := range params {
		switch value := raw.(type) {
		case string:
			encoded[key] = &ariadnev1.ErrorParamValue{
				Value: &ariadnev1.ErrorParamValue_StringValue{StringValue: value},
			}
		case bool:
			encoded[key] = &ariadnev1.ErrorParamValue{
				Value: &ariadnev1.ErrorParamValue_BoolValue{BoolValue: value},
			}
		case int:
			encoded[key] = &ariadnev1.ErrorParamValue{
				Value: &ariadnev1.ErrorParamValue_NumberValue{NumberValue: float64(value)},
			}
		case int32:
			encoded[key] = &ariadnev1.ErrorParamValue{
				Value: &ariadnev1.ErrorParamValue_NumberValue{NumberValue: float64(value)},
			}
		case int64:
			encoded[key] = &ariadnev1.ErrorParamValue{
				Value: &ariadnev1.ErrorParamValue_NumberValue{NumberValue: float64(value)},
			}
		case float32:
			encoded[key] = &ariadnev1.ErrorParamValue{
				Value: &ariadnev1.ErrorParamValue_NumberValue{NumberValue: float64(value)},
			}
		case float64:
			encoded[key] = &ariadnev1.ErrorParamValue{
				Value: &ariadnev1.ErrorParamValue_NumberValue{NumberValue: value},
			}
		default:
			if raw != nil {
				encoded[key] = &ariadnev1.ErrorParamValue{
					Value: &ariadnev1.ErrorParamValue_StringValue{StringValue: fmt.Sprint(raw)},
				}
			}
		}
	}
	return encoded
}
