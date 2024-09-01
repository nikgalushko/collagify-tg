package main

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"slices"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/robfig/cron/v3"

	"github.com/nikgalushko/collagify-tg/pkg/image"
)

var (
	BuildTime string
	moscowLoc *time.Location
)

const (
	tmpDBPath         = "/tmp/collagify.sqlite"
	crontab           = "59 23 * * *"
	apiTelegramServer = "https://api.telegram.org"
)

type App struct {
	log       *slog.Logger
	crn       *cron.Cron
	bt        *bot.Bot
	db        *storage
	serverURL string
}

type AppArgs struct {
	Token  string
	DBPath string
	Server string
}

func NewAppArgs() (AppArgs, error) {
	token := os.Getenv("COLLAGIFY_TG_TOKEN")
	if token == "" {
		return AppArgs{}, errors.New("empty tg token")
	}
	dbPath := os.Getenv("COLLAGIFY_DB_PATH")
	if dbPath == "" {
		dbPath = tmpDBPath
	}

	return AppArgs{Token: token, DBPath: dbPath, Server: apiTelegramServer}, nil
}

func New(log *slog.Logger, args AppArgs) (*App, error) {
	a := &App{log: log, serverURL: args.Server}
	a.initCron()
	err := a.initBot(args.Token)
	if err != nil {
		return nil, err
	}
	err = a.initDB(args.DBPath)
	if err != nil {
		return nil, err
	}

	return a, nil
}

func (a *App) initDB(dbPath string) error {
	db, err := NewStorage(dbPath)
	if err != nil {
		return err
	}
	a.db = db
	return nil
}

func (a *App) initCron() {
	c := cron.New()
	c.AddFunc(crontab, func() {
		err := a.cronHandler()
		if err != nil {
			a.log.Error("cron handler", slogerr(err))
		}
	})
	a.crn = c
}

func (a *App) initBot(token string) error {
	opts := []bot.Option{
		bot.WithDefaultHandler(a.botHandler),
		bot.WithServerURL(a.serverURL),
	}

	b, err := bot.New(token, opts...)
	if err != nil {
		return fmt.Errorf("init bot: %w", err)
	}

	a.bt = b
	return nil
}

func (a *App) Start(ctx context.Context) {
	a.crn.Start()
	a.bt.Start(ctx)
}

func (a *App) Close() {
	a.crn.Stop()
	err := a.db.Close()
	if err != nil {
		a.log.Error("on close", slogerr(err))
	}
}

func (a *App) cronHandler() error {
	ctx := context.Background()

	log := a.log.WithGroup("cron")
	log.Info("cron task start")

	chats, err := a.db.Chats(ctx)
	if err != nil {
		return err
	}

	log.Debug("chats to range", slog.Any("chats", chats))

	var funcErr error
	for _, chatID := range chats {
		messages, toCollage, err := a.db.Links(ctx, chatID)
		if err != nil {
			funcErr = errors.Join(funcErr, fmt.Errorf("reading keys by prefix: %w", err))
			continue
		}

		for _, item := range toCollage {
			err := a.processCollage(chatID, item)
			if err != nil {
				funcErr = errors.Join(funcErr, err)
				continue
			}
		}

		err = a.db.DeleteMessages(ctx, messages)
		if err == nil {
			err = a.deleteMessages(ctx, chatID, messages)
		}
		if err != nil {
			funcErr = errors.Join(funcErr, err)
		}
	}

	return funcErr
}

func (a *App) deleteMessages(ctx context.Context, chatID int64, messages []int) error {
	ok, err := a.bt.DeleteMessages(ctx, &bot.DeleteMessagesParams{
		ChatID:     chatID,
		MessageIDs: messages,
	})
	if err != nil {
		return fmt.Errorf("delete messages from channel %d: %w", chatID, err)
	}

	if !ok {
		return fmt.Errorf("unexpected fail to delete messages from channel %d", chatID)
	}

	return nil
}

func (a *App) processCollage(chatID int64, item toCollage) error {
	images := make([][]byte, 0, len(item.links))
	for _, u := range item.links {
		resp, err := http.Get(u)
		if err != nil {
			return fmt.Errorf("download link %s: %w", u, err)
		}
		defer resp.Body.Close()

		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return fmt.Errorf("reading response body: %w", err)
		}

		images = append(images, body)
	}

	cols := min(5, len(images))
	rows := len(images) / cols
	if len(images)%cols != 0 {
		rows++
	}

	collage, err := image.Concat(images, rows, cols)
	if err != nil {
		return fmt.Errorf("make collage: %w", err)
	}

	_, err = a.bt.SendPhoto(context.TODO(), &bot.SendPhotoParams{
		ChatID: chatID,
		Photo: &models.InputFileUpload{
			Filename: fmt.Sprintf("collage_%s.jpg", item.date),
			Data:     bytes.NewReader(collage),
		},
	})
	if err != nil {
		return fmt.Errorf("send collage: %w", err)
	}

	return nil
}

func (a *App) botHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	if update.ChannelPost == nil && update.MyChatMember == nil {
		a.log.Warn("usupported update event", slog.Any("event", *update))
		return
	}

	if update.ChannelPost != nil {
		err := a.botHandleChannelPost(ctx, update.ChannelPost)
		if err != nil {
			a.log.Error("failed to handle new photo message", slogerr(err))
		}
	}

	if update.MyChatMember != nil {
		err := a.botHandleMyChatMember(ctx, update.MyChatMember)
		if err != nil {
			a.log.Error("failed to handle new chat registration", slogerr(err))
		}
	}
}

func (a *App) botHandleMyChatMember(ctx context.Context, r *models.ChatMemberUpdated) error {
	return a.db.RegisterChat(ctx, r.Chat.ID, time.Unix(int64(r.Date), 0).In(moscowLoc))
}

func (a *App) botHandleChannelPost(ctx context.Context, m *models.Message) error {
	if len(m.Photo) == 0 {
		a.log.Warn("message without photo")
		return nil
	}

	slices.SortFunc(m.Photo, func(a, b models.PhotoSize) int {
		return cmp.Compare(a.FileSize, b.FileSize)
	})

	largestPhoto := m.Photo[len(m.Photo)-1]
	f, err := a.bt.GetFile(ctx, &bot.GetFileParams{FileID: largestPhoto.FileID})
	if err != nil {
		return fmt.Errorf("get file info: %w", err)
	}

	link := a.bt.FileDownloadLink(f)
	a.log.Info("download file link", slog.String("url", link))

	err = a.db.RegistreLink(ctx, m.Chat.ID, int64(m.ID), time.Unix(int64(m.Date), 0).In(moscowLoc), link)
	if err != nil {
		return fmt.Errorf("save file link: %w", err)
	}

	return nil
}

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	if BuildTime == "" {
		BuildTime = "not set"
	}
	log.Info("start", slog.String("build-time", BuildTime))

	appArgs, err := NewAppArgs()
	if err != nil {
		log.Error("init app arguments", slogerr(err))
		os.Exit(1)
	}

	moscowLoc, err = loadLocation()
	if err != nil {
		log.Error("loading location 'Europe/Moscow'", slogerr(err))
		os.Exit(1)
	}

	a, err := New(log, appArgs)
	if err != nil {
		log.Error("init app", slogerr(err))
		os.Exit(1)
	}
	defer a.Close()

	a.Start(ctx)
}

func slogerr(err error) slog.Attr {
	if err == nil {
		return slog.Attr{}
	}

	return slog.String("err", err.Error())
}

func loadLocation() (*time.Location, error) {
	return time.LoadLocation("Europe/Moscow")
}
