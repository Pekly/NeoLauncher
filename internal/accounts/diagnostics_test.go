package accounts

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestProviderDiagnosticsAreActionableAndSecretSafe(t *testing.T) {
	for _, status := range []int{http.StatusTooManyRequests, http.StatusServiceUnavailable, http.StatusForbidden} {
		for _, provider := range []string{"Microsoft", "Xbox", "Minecraft"} {
			t.Run(fmt.Sprintf("%s/%d", provider, status), func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.WriteHeader(status)
					fmt.Fprint(w, `{"error":"unexpected","error_description":"SECRET-REFLECTED-TOKEN"}`)
				}))
				defer server.Close()
				endpoint := server.URL + "/?private=SECRET-URL"
				s := &Service{client: server.Client(), endpoints: productionEndpoints()}
				switch provider {
				case "Microsoft":
					s.endpoints.token = endpoint
				case "Xbox":
					s.endpoints.xsts = endpoint
				case "Minecraft":
					s.endpoints.minecraft = endpoint
				}
				_, err := s.request(context.Background(), http.MethodPost, endpoint, "application/json", "SECRET-BEARER", []byte(`{"secret":"SECRET-BODY"}`), &struct{}{})
				if err == nil || !strings.Contains(err.Error(), provider) {
					t.Fatal("provider not identified", err)
				}
				if strings.Contains(err.Error(), "SECRET") || strings.Contains(err.Error(), server.URL) {
					t.Fatal("secret echoed", err)
				}
				if status == 503 && !strings.Contains(err.Error(), "temporarily unavailable") {
					t.Fatal(err)
				}
				if status == 429 && !strings.Contains(err.Error(), "rate limited") {
					t.Fatal(err)
				}
			})
		}
	}
	for _, code := range []string{"server_error", "temporarily_unavailable"} {
		if err := oauthError(code); !strings.Contains(err.Error(), "fresh code") {
			t.Fatal(err)
		}
	}
}
