package ai

import (
	"io"
	"net/http"

	"github.com/anthropics/anthropic-sdk-go/option"
)

// activityMiddleware signals on activity whenever the response body yields bytes. The SDK's stream reader drops SSE
// ping events, so a host that sends only pings while it writes a tool call (sent whole at the end) would otherwise
// look silent to the idle timer even though the connection is alive.
func activityMiddleware(activity chan<- struct{}) option.Middleware {
	return func(req *http.Request, next option.MiddlewareNext) (*http.Response, error) {
		resp, err := next(req)
		if err == nil && resp != nil && resp.Body != nil {
			resp.Body = &activityBody{ReadCloser: resp.Body, activity: activity}
		}
		return resp, err
	}
}

type activityBody struct {
	io.ReadCloser
	activity chan<- struct{}
}

func (b *activityBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if n > 0 {
		// Non-blocking: one pending signal is enough to reset the idle timer.
		select {
		case b.activity <- struct{}{}:
		default:
		}
	}
	return n, err
}
