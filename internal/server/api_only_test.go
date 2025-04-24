package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/assert"
)

// Use the existing MockTxBuilder that's already defined in server_test.go

// setupAPITestServer creates a server instance for testing
func setupAPITestServer(apiOnly bool, corsAllowed, corsHeaders, corsMethods string) *Server {
	mockBuilder := new(MockTxBuilder)
	// We'll set expectations in each test case as needed

	config := &Config{
		network:     "testnet",
		symbol:      "ETH",
		httpPort:    8080,
		interval:    1440,
		payout:      1.0,
		proxyCount:  0,
		apiOnly:     apiOnly,
		corsAllowed: corsAllowed,
		corsHeaders: corsHeaders,
		corsMethods: corsMethods,
	}

	return NewServer(mockBuilder, config)
}

func TestAPIOnlyMode(t *testing.T) {
	tests := []struct {
		name          string
		apiOnly       bool
		path          string
		method        string
		expectedCode  int
		checkCORS     bool
		corsAllowed   string
		corsHeaders   string
		corsMethods   string
		requestOrigin string
	}{
		{
			name:         "normal_mode_frontend_access",
			apiOnly:      false,
			path:         "/",
			method:       "GET",
			expectedCode: http.StatusOK,
		},
		{
			name:         "normal_mode_api_access",
			apiOnly:      false,
			path:         "/api/info",
			method:       "GET",
			expectedCode: http.StatusOK,
		},
		{
			name:         "api_only_mode_frontend_blocked",
			apiOnly:      true,
			path:         "/",
			method:       "GET",
			expectedCode: http.StatusNotFound,
		},
		{
			name:         "api_only_mode_static_assets_blocked",
			apiOnly:      true,
			path:         "/static/js/main.js",
			method:       "GET",
			expectedCode: http.StatusNotFound,
		},
		{
			name:         "api_only_mode_api_available",
			apiOnly:      true,
			path:         "/api/info",
			method:       "GET",
			expectedCode: http.StatusOK,
		},
		{
			name:          "api_only_mode_with_cors_wildcard",
			apiOnly:       true,
			path:          "/api/info",
			method:        "GET",
			expectedCode:  http.StatusOK,
			checkCORS:     true,
			corsAllowed:   "*",
			corsHeaders:   "Content-Type",
			corsMethods:   "GET,POST",
			requestOrigin: "https://example.com",
		},
		{
			name:          "api_only_mode_with_cors_specific_origin",
			apiOnly:       true,
			path:          "/api/info",
			method:        "GET",
			expectedCode:  http.StatusOK,
			checkCORS:     true,
			corsAllowed:   "https://example.com,https://test.com",
			corsHeaders:   "Content-Type,Authorization",
			corsMethods:   "GET,POST,OPTIONS",
			requestOrigin: "https://example.com",
		},
		{
			name:          "api_only_mode_preflight_request",
			apiOnly:       true,
			path:          "/api/info",
			method:        "OPTIONS",
			expectedCode:  http.StatusOK,
			checkCORS:     true,
			corsAllowed:   "*",
			corsHeaders:   "Content-Type",
			corsMethods:   "GET,POST,OPTIONS",
			requestOrigin: "https://example.com",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create a server instance with the appropriate configuration
			server := setupAPITestServer(
				tt.apiOnly,
				tt.corsAllowed,
				tt.corsHeaders,
				tt.corsMethods,
			)

			// Set the Sender expectation only for API calls that need it
			if strings.HasPrefix(tt.path, "/api/") && tt.method != "OPTIONS" {
				// Get the mock builder from server and set expectations
				mockBuilder := server.TxBuilder.(*MockTxBuilder)
				mockBuilder.On("Sender").Return(common.HexToAddress("0xd8dA6BF26964aF9D7eEd9e03E53415D37aA96045"))
			}

			// Create a test request
			req := httptest.NewRequest(tt.method, tt.path, nil)
			if tt.requestOrigin != "" {
				req.Header.Set("Origin", tt.requestOrigin)
			}

			// Create a response recorder
			rr := httptest.NewRecorder()

			// Set up the router and serve the request
			router := server.setupRouter()
			router.ServeHTTP(rr, req)

			// Check the response status code
			assert.Equal(t, tt.expectedCode, rr.Code, "Status code mismatch")

			// Check CORS headers if needed
			if tt.checkCORS {
				if tt.corsAllowed == "*" {
					assert.Equal(t, "*", rr.Header().Get("Access-Control-Allow-Origin"), "CORS wildcard origin mismatch")
				} else if tt.requestOrigin != "" {
					// For specific origins, check if the response origin matches the request
					expectedOrigin := ""
					for _, origin := range strings.Split(tt.corsAllowed, ",") {
						if origin == tt.requestOrigin {
							expectedOrigin = origin
							break
						}
					}
					assert.Equal(t, expectedOrigin, rr.Header().Get("Access-Control-Allow-Origin"), "CORS specific origin mismatch")
				}

				assert.Equal(t, tt.corsMethods, rr.Header().Get("Access-Control-Allow-Methods"), "CORS methods mismatch")
				assert.Equal(t, tt.corsHeaders, rr.Header().Get("Access-Control-Allow-Headers"), "CORS headers mismatch")
			}

			// If we had set an expectation on the mock, verify it was met
			if mockTx, ok := server.TxBuilder.(*MockTxBuilder); ok {
				mockTx.AssertExpectations(t)
			}
		})
	}
}
