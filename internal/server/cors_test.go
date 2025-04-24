package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCorsMiddleware(t *testing.T) {
	tests := []struct {
		name           string
		allowed        string
		headers        string
		methods        string
		requestOrigin  string
		expectedOrigin string
	}{
		{
			name:           "wildcard_origin",
			allowed:        "*",
			headers:        "Content-Type",
			methods:        "GET,POST",
			requestOrigin:  "https://example.com",
			expectedOrigin: "*",
		},
		{
			name:           "specific_origin_match",
			allowed:        "https://example.com,https://test.com",
			headers:        "Content-Type",
			methods:        "GET,POST",
			requestOrigin:  "https://example.com",
			expectedOrigin: "https://example.com",
		},
		{
			name:           "specific_origin_no_match",
			allowed:        "https://example.com,https://test.com",
			headers:        "Content-Type",
			methods:        "GET,POST",
			requestOrigin:  "https://other.com",
			expectedOrigin: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create a test handler that will be called after the middleware
			testHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
			})

			// Create the middleware
			middleware := NewCorsMiddleware(tt.allowed, tt.headers, tt.methods)

			// Create a test request with the specified origin
			req := httptest.NewRequest("GET", "/api/info", nil)
			if tt.requestOrigin != "" {
				req.Header.Set("Origin", tt.requestOrigin)
			}

			// Create a response recorder
			rr := httptest.NewRecorder()

			// Call the middleware with our test handler
			middleware.ServeHTTP(rr, req, testHandler)

			// Check the CORS headers
			if got := rr.Header().Get("Access-Control-Allow-Origin"); got != tt.expectedOrigin {
				t.Errorf("Access-Control-Allow-Origin = %q, want %q", got, tt.expectedOrigin)
			}

			if got := rr.Header().Get("Access-Control-Allow-Methods"); got != tt.methods {
				t.Errorf("Access-Control-Allow-Methods = %q, want %q", got, tt.methods)
			}

			if got := rr.Header().Get("Access-Control-Allow-Headers"); got != tt.headers {
				t.Errorf("Access-Control-Allow-Headers = %q, want %q", got, tt.headers)
			}
		})
	}

	// Test OPTIONS request handling
	t.Run("options_request", func(t *testing.T) {
		// Create a test handler that will be called after the middleware
		testHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// This should not be called for OPTIONS requests
			t.Error("Handler was called for OPTIONS request")
		})

		// Create the middleware
		middleware := NewCorsMiddleware("*", "Content-Type", "GET,POST,OPTIONS")

		// Create a test OPTIONS request
		req := httptest.NewRequest("OPTIONS", "/api/info", nil)
		req.Header.Set("Origin", "https://example.com")

		// Create a response recorder
		rr := httptest.NewRecorder()

		// Call the middleware with our test handler
		middleware.ServeHTTP(rr, req, testHandler)

		// Check the response status code
		if status := rr.Code; status != http.StatusOK {
			t.Errorf("Status code = %d, want %d", status, http.StatusOK)
		}
	})
}
