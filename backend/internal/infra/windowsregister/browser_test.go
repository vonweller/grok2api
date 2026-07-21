package windowsregister

import "testing"

func TestBrowserCookieSelection(t *testing.T) {
	cookies := []BrowserCookie{
		{Name: "other", Value: "x"},
		{Name: "sso", Value: "wanted", Domain: ".x.ai", Path: "/"},
	}
	value, ok := authenticationCookie(cookies)
	if !ok || value != "wanted" {
		t.Fatalf("value=%q ok=%v", value, ok)
	}
}

func TestBrowserCookieSelectionPrefersSSO(t *testing.T) {
	cookies := []BrowserCookie{
		{Name: "sso-rw", Value: "read-write"},
		{Name: "sso", Value: "primary"},
	}
	value, ok := authenticationCookie(cookies)
	if !ok || value != "primary" {
		t.Fatalf("value=%q ok=%v", value, ok)
	}
}
