package server

import (
	"context"
	"net/http"
	"time"
)

type desktopTransportKey struct{}

// TrustedDesktopHandler is attached only to Wails' internal asset transport.
func (r *Router) TrustedDesktopHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		r.ServeHTTP(w, req.WithContext(context.WithValue(req.Context(), desktopTransportKey{}, true)))
	})
}
func (r *Router) BrowserLaunchURL(base string) string {
	grant := newAuthToken()
	r.launchMu.Lock()
	for key, exp := range r.launches {
		if time.Now().After(exp) {
			delete(r.launches, key)
		}
	}
	r.launches[grant] = time.Now().Add(2 * time.Minute)
	r.launchMu.Unlock()
	return base + "#mm_launch=" + grant
}
