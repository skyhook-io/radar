package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	sdkauth "github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/modelcontextprotocol/go-sdk/oauthex"
	"golang.org/x/net/html"

	"github.com/skyhook-io/radar/internal/auth"
)

func TestMCPOAuthSDKIntegration(t *testing.T) {
	for _, deployment := range []struct {
		basePath   string
		prefixOnly bool
	}{{}, {basePath: "/tools/radar"}, {basePath: "/tools/radar", prefixOnly: true}} {
		basePath := deployment.basePath
		for _, resourcePath := range []string{"/mcp", "/mcp-readonly"} {
			t.Run(fmt.Sprintf("%s%s/prefixOnly=%t", basePath, resourcePath, deployment.prefixOnly), func(t *testing.T) {
				t.Setenv("RADAR_CLOUD_MODE", "false")
				router := chi.NewRouter()
				httpServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if deployment.prefixOnly && !strings.HasPrefix(r.URL.Path, basePath+"/") {
						http.NotFound(w, r)
						return
					}
					router.ServeHTTP(w, r)
				}))
				defer httpServer.Close()
				cfg := auth.Config{
					Mode: "oidc", Secret: "mcp-integration-test-secret", CookieTTL: time.Hour,
					OIDCRedirectURL: httpServer.URL + basePath + "/auth/callback",
				}
				cfg.Defaults()
				oauthServer, err := auth.NewMCPOAuthServer(cfg, basePath, []string{"/mcp", "/mcp-readonly"})
				if err != nil {
					t.Fatal(err)
				}
				mcpServer := mcp.NewServer(&mcp.Implementation{Name: "oauth-test", Version: "1"}, nil)
				mcp.AddTool(mcpServer, &mcp.Tool{Name: "identity"}, func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, *auth.User, error) {
					user := auth.UserFromContext(ctx)
					if user == nil {
						return nil, nil, fmt.Errorf("authenticated identity missing from MCP context")
					}
					return nil, user, nil
				})
				handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return mcpServer }, &mcp.StreamableHTTPOptions{Stateless: true})
				s := &Server{router: router, basePath: basePath, authConfig: cfg, mcpOAuth: oauthServer, mcpHandler: handler, mcpReadOnlyHandler: handler}
				s.setupRoutes()

				browser := &http.Client{Transport: httpServer.Client().Transport}
				browser.Jar, err = cookiejar.New(nil)
				if err != nil {
					t.Fatal(err)
				}
				browser.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
				serverURL, _ := url.Parse(httpServer.URL)
				browser.Jar.SetCookies(serverURL, auth.CreateSessionCookie(&auth.User{Username: "alice@example.com", Groups: []string{"developers"}}, "browser-session", "", cfg.Secret, time.Hour, true))

				const redirectURI = "http://127.0.0.1:43129/callback"
				var clientID string
				loginCount := 0
				oauthClient, err := sdkauth.NewAuthorizationCodeHandler(&sdkauth.AuthorizationCodeHandlerConfig{
					Client: httpServer.Client(),
					DynamicClientRegistrationConfig: &sdkauth.DynamicClientRegistrationConfig{Metadata: &oauthex.ClientRegistrationMetadata{
						ClientName: "SDK integration test", RedirectURIs: []string{redirectURI},
						TokenEndpointAuthMethod: "none", GrantTypes: []string{"authorization_code", "refresh_token"}, ResponseTypes: []string{"code"},
					}},
					AuthorizationCodeFetcher: func(ctx context.Context, args *sdkauth.AuthorizationArgs) (*sdkauth.AuthorizationResult, error) {
						loginCount++
						authURL, err := url.Parse(args.URL)
						if err != nil {
							return nil, err
						}
						clientID = authURL.Query().Get("client_id")
						return approveMCPConsent(ctx, browser, args.URL, redirectURI)
					},
				})
				if err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				client := mcp.NewClient(&mcp.Implementation{Name: "oauth-test-client", Version: "1"}, nil)
				session, err := client.Connect(ctx, &mcp.StreamableClientTransport{
					Endpoint: httpServer.URL + basePath + resourcePath, HTTPClient: httpServer.Client(), OAuthHandler: oauthClient,
				}, nil)
				if err != nil {
					t.Fatalf("SDK discovery, registration, consent and initialize: %v", err)
				}
				defer session.Close()
				listed, err := session.ListTools(ctx, nil)
				if err != nil || len(listed.Tools) != 1 || listed.Tools[0].Name != "identity" {
					t.Fatalf("tools/list = %v, %v", listed, err)
				}
				result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "identity", Arguments: map[string]any{}})
				if err != nil || result.IsError {
					t.Fatalf("tools/call = %v, %v", result, err)
				}
				identity, err := json.Marshal(result.StructuredContent)
				if err != nil || !strings.Contains(string(identity), "alice@example.com") || !strings.Contains(string(identity), "developers") {
					t.Fatalf("MCP identity = %s, %v", identity, err)
				}

				source, err := oauthClient.TokenSource(ctx)
				if err != nil {
					t.Fatal(err)
				}
				token, err := source.Token()
				if err != nil || token.RefreshToken == "" {
					t.Fatalf("missing refresh token: %v", err)
				}
				oldAccess, oldRefresh := token.AccessToken, token.RefreshToken
				// Expire the client's cached token to exercise its real automatic refresh.
				token.Expiry = time.Now().Add(-time.Minute)
				if _, err := session.ListTools(ctx, nil); err != nil {
					t.Fatalf("tools/list after automatic refresh: %v", err)
				}
				refreshed, err := source.Token()
				if err != nil || refreshed.AccessToken == oldAccess || refreshed.RefreshToken == oldRefresh || loginCount != 1 {
					t.Fatalf("refresh did not rotate tokens without another login (logins=%d): %v", loginCount, err)
				}

				otherResource := "/mcp"
				if resourcePath == otherResource {
					otherResource = "/mcp-readonly"
				}
				for _, path := range []string{otherResource, "/api/auth/me", "/api/namespaces"} {
					req, _ := http.NewRequestWithContext(ctx, http.MethodGet, httpServer.URL+basePath+path, nil)
					req.Header.Set("Authorization", "Bearer "+refreshed.AccessToken)
					resp, err := httpServer.Client().Do(req)
					if err != nil {
						t.Fatal(err)
					}
					body, _ := io.ReadAll(resp.Body)
					resp.Body.Close()
					if path != "/api/auth/me" && resp.StatusCode != http.StatusUnauthorized {
						t.Fatalf("MCP token accepted for %s: %d %s", path, resp.StatusCode, body)
					}
					if path == "/api/auth/me" && strings.Contains(string(body), "alice@example.com") {
						t.Fatalf("MCP token authenticated a REST request: %s", body)
					}
				}
				resp, err := httpServer.Client().PostForm(httpServer.URL+basePath+"/auth/mcp/token", url.Values{
					"grant_type": {"refresh_token"}, "client_id": {clientID}, "refresh_token": {oldRefresh},
				})
				if err != nil {
					t.Fatal(err)
				}
				resp.Body.Close()
				if resp.StatusCode != http.StatusBadRequest {
					t.Fatalf("refresh token replay status = %d, want 400", resp.StatusCode)
				}
			})
		}
	}
}

func approveMCPConsent(ctx context.Context, browser *http.Client, authURL, redirectURI string) (*sdkauth.AuthorizationResult, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, authURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := browser.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("consent GET returned %d, expected a consent page", resp.StatusCode)
	}
	form := url.Values{}
	tokenizer := html.NewTokenizer(resp.Body)
	for {
		tt := tokenizer.Next()
		if tt == html.ErrorToken {
			if err := tokenizer.Err(); err != io.EOF {
				return nil, err
			}
			break
		}
		if tt != html.StartTagToken && tt != html.SelfClosingTagToken {
			continue
		}
		token := tokenizer.Token()
		if token.Data != "input" {
			continue
		}
		attrs := map[string]string{}
		for _, attr := range token.Attr {
			attrs[attr.Key] = attr.Val
		}
		if attrs["type"] == "hidden" && attrs["name"] != "" {
			form.Set(attrs["name"], attrs["value"])
		}
	}
	form.Set("decision", "allow")
	req, err = http.NewRequestWithContext(ctx, http.MethodPost, authURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	origin, _ := url.Parse(authURL)
	req.Header.Set("Origin", origin.Scheme+"://"+origin.Host)
	approved, err := browser.Do(req)
	if err != nil {
		return nil, err
	}
	defer approved.Body.Close()
	if approved.StatusCode != http.StatusFound && approved.StatusCode != http.StatusSeeOther {
		body, _ := io.ReadAll(approved.Body)
		return nil, fmt.Errorf("consent POST returned %d: %s", approved.StatusCode, body)
	}
	location, err := approved.Location()
	if err != nil {
		return nil, err
	}
	if !strings.HasPrefix(location.String(), redirectURI+"?") || location.Query().Get("code") == "" {
		return nil, fmt.Errorf("consent did not redirect to the registered client callback")
	}
	return &sdkauth.AuthorizationResult{Code: location.Query().Get("code"), State: location.Query().Get("state")}, nil
}
