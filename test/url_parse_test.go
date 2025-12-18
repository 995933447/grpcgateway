package test

import (
	"fmt"
	"net/url"
	"testing"
)

func TestParseUrl(t *testing.T) {
	u, err := url.Parse("testschema://echo.Echo")
	if err != nil {
		t.Fatal(err)
	}
	fmt.Println(u.Scheme)
}
