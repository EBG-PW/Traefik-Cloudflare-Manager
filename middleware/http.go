package middleware

import (
	"net/http"
	"time"

	"traefik-cloudflare-manager/lib"
)

const requestBodyReadTimeout = 30 * time.Second

func SecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "same-origin")
		next.ServeHTTP(w, r)
	})
}

func LimitBody(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, lib.MaxRequestBodySize)
		next.ServeHTTP(w, r)
	})
}

func LimitBodyReadTime(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body == nil || (r.ContentLength == 0 && len(r.TransferEncoding) == 0) {
			next.ServeHTTP(w, r)
			return
		}
		controller := http.NewResponseController(w)
		if err := controller.SetReadDeadline(time.Now().Add(requestBodyReadTimeout)); err == nil {
			defer func() { _ = controller.SetReadDeadline(time.Time{}) }()
		}
		next.ServeHTTP(w, r)
	})
}
