package cloudserver

import (
	"bytes"
	"net/http"
	"strconv"
	"strings"
)

// prefixRewrite wraps a handler that was mounted behind http.StripPrefix and
// re-adds the stripped prefix to everything the browser will interpret as an
// absolute path: redirect headers, cookie paths, and root-relative URLs inside
// HTML bodies. Without this, handlers that emit absolute paths (the dashboard)
// send the browser to URLs outside the ingress route.
func prefixRewrite(prefix string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rw := &prefixRewriteWriter{inner: w, prefix: prefix}
		next.ServeHTTP(rw, r)
		rw.finish()
	})
}

type prefixRewriteWriter struct {
	inner       http.ResponseWriter
	prefix      string
	status      int
	wroteHeader bool
	buffering   bool
	buf         bytes.Buffer
}

var rewriteHeaderNames = []string{"Location", "Content-Location", "HX-Redirect", "HX-Location", "HX-Push-Url"}

func (rw *prefixRewriteWriter) Header() http.Header {
	return rw.inner.Header()
}

func (rw *prefixRewriteWriter) WriteHeader(status int) {
	if rw.wroteHeader {
		return
	}
	rw.wroteHeader = true
	rw.status = status
	h := rw.inner.Header()
	for _, name := range rewriteHeaderNames {
		if v := h.Get(name); strings.HasPrefix(v, "/") && !strings.HasPrefix(v, rw.prefix+"/") {
			h.Set(name, rw.prefix+v)
		}
	}
	cookies := h["Set-Cookie"]
	for i, c := range cookies {
		if !strings.Contains(c, "Path="+rw.prefix) {
			cookies[i] = strings.Replace(c, "Path=/", "Path="+rw.prefix+"/", 1)
		}
	}
	if strings.HasPrefix(h.Get("Content-Type"), "text/html") {
		// Buffer HTML so root-relative URLs can be rewritten before sending;
		// the declared length no longer matches, so recompute it on finish.
		rw.buffering = true
		h.Del("Content-Length")
		return
	}
	rw.inner.WriteHeader(status)
}

func (rw *prefixRewriteWriter) Write(b []byte) (int, error) {
	if !rw.wroteHeader {
		rw.WriteHeader(http.StatusOK)
	}
	if rw.buffering {
		return rw.buf.Write(b)
	}
	return rw.inner.Write(b)
}

func (rw *prefixRewriteWriter) Flush() {
	if rw.buffering {
		return
	}
	if f, ok := rw.inner.(http.Flusher); ok {
		f.Flush()
	}
}

func (rw *prefixRewriteWriter) finish() {
	if !rw.buffering {
		return
	}
	// Handlers only emit root-relative URLs under /dashboard; rewriting just
	// that path avoids mangling unrelated `"/` sequences like self-closing tags.
	body := bytes.ReplaceAll(rw.buf.Bytes(), []byte(`"/dashboard`), []byte(`"`+rw.prefix+`/dashboard`))
	rw.inner.Header().Set("Content-Length", strconv.Itoa(len(body)))
	rw.inner.WriteHeader(rw.status)
	rw.inner.Write(body)
}
