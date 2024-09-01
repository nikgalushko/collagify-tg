package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path"
	"strings"
	"testing"
	"time"

	"github.com/go-telegram/bot/models"
	"github.com/matryer/is"
)

func TestApp(t *testing.T) {
	is := is.New(t)

	loc, err := loadLocation()
	is.NoErr(err)
	moscowLoc = loc

	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))

	server := StartServer(is)
	t.Cleanup(server.close)

	app, err := New(log, AppArgs{Server: server.Addr(), DBPath: path.Join(t.TempDir(), "collagify.sqlite"), Token: "1"})
	is.NoErr(err)
	t.Cleanup(app.Close)

	err = app.botHandleMyChatMember(context.TODO(), &models.ChatMemberUpdated{Chat: models.Chat{ID: 1337}})
	is.NoErr(err)

	err = app.botHandleChannelPost(context.TODO(), &models.Message{
		Chat: models.Chat{ID: 1337},
		Date: int(time.Date(2024, time.August, 31, 14, 19, 0, 0, loc).Unix()),
		Photo: []models.PhotoSize{
			{FileID: "red.jpeg", FileSize: 10},
			{FileID: "fake.jpeg", FileSize: 2},
		},
		ID: 8,
	})
	is.NoErr(err)

	err = app.botHandleChannelPost(context.TODO(), &models.Message{
		Chat: models.Chat{ID: 1337},
		Date: int(time.Date(2024, time.September, 1, 13, 11, 0, 0, loc).Unix()),
		Photo: []models.PhotoSize{
			{FileID: "green.jpeg", FileSize: 10},
			{FileID: "fake.jpeg", FileSize: 2},
		},
		ID: 9,
	})
	is.NoErr(err)

	err = app.cronHandler()
	is.NoErr(err)
	is.Equal([]string{"collage_2024-08-31.jpg", "collage_2024-09-01.jpg"}, server.sentPhotos)
	is.Equal("[8,9]", server.deletedMessages)

	messages, toCollage, err := app.db.Links(context.TODO(), 1337)
	is.Equal(0, len(messages))
	is.Equal(0, len(toCollage))
	is.Equal(sql.ErrNoRows, err)
}

type server struct {
	is              *is.I
	http            *httptest.Server
	sentPhotos      []string
	deletedMessages string
}

func StartServer(is *is.I) *server {
	s := &server{is: is}
	s.http = httptest.NewServer(s.handler())
	return s
}

func (s *server) close() {
	s.http.Close()
}

func (s *server) Addr() string {
	return s.http.URL
}

func (s *server) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /bot1/getMe", s.getMe)
	mux.HandleFunc("POST /bot1/getFile", s.getFile)
	mux.HandleFunc("POST /bot1/sendPhoto", s.sendPhoto)
	mux.HandleFunc("GET /file/bot1/testdir/{file}", s.downloadFile)
	mux.HandleFunc("POST /bot1/deleteMessages", s.deleteMessages)

	return mux
}

func (s *server) getMe(w http.ResponseWriter, r *http.Request) {
	w.Write([]byte(`{"ok":true,"result":{}}`))
}

func (s *server) getFile(w http.ResponseWriter, r *http.Request) {
	err := r.ParseMultipartForm(1024)
	s.is.NoErr(err)

	fileID := r.PostForm.Get("file_id")

	data, err := json.Marshal(models.File{FileID: fileID, FilePath: "testdir/" + fileID})
	s.is.NoErr(err)

	w.WriteHeader(http.StatusOK)
	err = json.NewEncoder(w).Encode(struct {
		OK     bool
		Result json.RawMessage
	}{
		OK:     true,
		Result: json.RawMessage(data),
	})
	s.is.NoErr(err)
}

func (s *server) sendPhoto(w http.ResponseWriter, r *http.Request) {
	filename, err := s.extract(r, "filename")
	s.is.NoErr(err)
	s.sentPhotos = append(s.sentPhotos, filename)

	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`{"ok":true,"result":{}}`))
}

func (s *server) downloadFile(w http.ResponseWriter, r *http.Request) {
	file := r.PathValue("file")
	data, err := os.ReadFile("testdata/" + file)
	s.is.NoErr(err)

	w.WriteHeader(http.StatusOK)
	w.Write(data)
}

func (s *server) deleteMessages(w http.ResponseWriter, r *http.Request) {
	ids, err := s.extract(r, "message_ids")
	s.is.NoErr(err)
	s.deletedMessages += ids

	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`{"ok":true,"result":true}`))
}

func (s *server) extract(r *http.Request, field string) (string, error) {
	contentType := r.Header.Get("Content-Type")
	mediaType, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		return "", err
	}

	if !strings.HasPrefix(mediaType, "multipart/") {
		return "", fmt.Errorf("invalid content type: %s", contentType)
	}

	boundary := params["boundary"]
	if boundary == "" {
		return "", fmt.Errorf("no boundary found in Content-Type")
	}

	mr := multipart.NewReader(r.Body, boundary)

	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}

		if field == "filename" {
			filename := part.FileName()
			if filename != "" {
				return filename, nil
			}
		} else {
			if part.FormName() == field {
				data, err := io.ReadAll(part)
				if err != nil {
					return "", err
				}
				return string(data), nil
			}
		}

	}

	return "", errors.New("no filed")
}
