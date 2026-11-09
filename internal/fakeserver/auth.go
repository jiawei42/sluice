package fakeserver

import "net/http"

// RequireAuth rejects requests whose Authorization header is not want.
// An empty want disables the check.
func RequireAuth(want string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if want != "" && r.Header.Get("Authorization") != want {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}
