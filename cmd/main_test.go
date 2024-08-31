package main

import (
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/matryer/is"
)

func TestApp(t *testing.T) {
	is := is.New(t)
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Println(r.URL.String())
		if r.URL.String() == "/bot1/getMe" {
			w.Write([]byte(`{"ok":true,"result":{}}`))
			return
		}
		fmt.Fprint(w, "Hello, World")
	}))
	t.Cleanup(server.Close)

	app, err := New(log, AppArgs{
		Server: server.URL,
		DBPath: t.TempDir(),
		Token:  "1",
	})
	is.NoErr(err)
	t.Cleanup(app.Close)
}
