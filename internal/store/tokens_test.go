package store

import "testing"

func TestTokenizeIdent(t *testing.T) {
	cases := []struct{ in, want string }{
		{"getUserByID", "getUserByID get user by id"},
		{"get_user_name", "get_user_name get user name"},
		{"get-user", "get-user get user"},
		{"HTTPServer", "HTTPServer http server"},
		{"parseJSON2Map", "parseJSON2Map parse json2 map"},
		{"MD5Hash", "MD5Hash md5 hash"},
		{"utf8Decode", "utf8Decode utf8 decode"},
		{"$scope", "$scope scope"},
		{"__init__", "__init__ init"},
		{"Foo", "Foo"},
		{"foo", "foo"},
		{"ABC", "ABC"},
		{"x", "x"},
		{"", ""},
		{"ÜberName", "ÜberName über name"},
	}
	for _, c := range cases {
		if got := TokenizeIdent(c.in); got != c.want {
			t.Errorf("TokenizeIdent(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestMatchExpr(t *testing.T) {
	cases := []struct {
		in     string
		prefix bool
		want   string
	}{
		{"foo", true, `"foo"*`},
		{"foo", false, `"foo"`},
		{"getUser", true, `("getuser"* OR ("get"* AND "user"*))`},
		{"Server.Start", true, `"server"* AND "start"*`},
		{"$scope", false, `("$scope" OR "scope")`},
		{`a"b`, false, `"a" AND "b"`},
		{"   ", true, ""},
		{"", true, ""},
	}
	for _, c := range cases {
		if got := MatchExpr(c.in, c.prefix); got != c.want {
			t.Errorf("MatchExpr(%q,%v) = %s, want %s", c.in, c.prefix, got, c.want)
		}
	}
}
