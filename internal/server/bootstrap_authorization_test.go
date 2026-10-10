package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestBootstrapRequiresAuthorization(t *testing.T) {
	router := NewRouter(nil, nil)
	unauthorized := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/bootstrap.js", nil)
	req.Host = "127.0.0.1"
	router.ServeHTTP(unauthorized, req)
	if unauthorized.Code != 401 {
		t.Fatalf("unauthorized bootstrap=%d", unauthorized.Code)
	}
	launch := router.BrowserLaunchURL("http://localhost:7912/chat.html")
	parsed, _ := url.Parse(launch)
	grant, _ := url.ParseQuery(parsed.Fragment)
	request := func(credential string, token string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/api/bootstrap.js", nil)
		req.Host = "127.0.0.1"
		req.Header.Set("X-MM-Launch", credential)
		req.Header.Set("X-MM-Token", token)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		return w
	}
	w := request(grant.Get("mm_launch"), "")
	if w.Code != 200 {
		t.Fatalf("launch failed %d %s", w.Code, w.Body.String())
	}
	var result map[string]string
	json.Unmarshal(w.Body.Bytes(), &result)
	if result["token"] != router.AuthToken() {
		t.Fatal("wrong token")
	}
	if request(grant.Get("mm_launch"), "").Code != 401 {
		t.Fatal("launch credential reused")
	}
	if request("", result["token"]).Code != 200 {
		t.Fatal("authorized refresh failed")
	}
}
