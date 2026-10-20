package main

import (
	"reflect"
	"testing"
)

func TestBrowserURL(t *testing.T) {
	cases := map[string]string{
		":8080":          "http://localhost:8080",
		"0.0.0.0:8080":   "http://localhost:8080",
		"[::]:8080":      "http://localhost:8080",
		"127.0.0.1:9000": "http://127.0.0.1:9000",
		"arena.lan:80":   "http://arena.lan:80",
	}
	for in, want := range cases {
		if got := browserURL(in); got != want {
			t.Errorf("browserURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSplitList(t *testing.T) {
	got := splitList(" https://a.example , ,https://b.example")
	want := []string{"https://a.example", "https://b.example"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("splitList = %q, want %q", got, want)
	}
	if splitList("") != nil {
		t.Fatal("empty flag should give no origins")
	}
}
