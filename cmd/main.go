package main

import (
	"bytes"
	"cmp"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"slices"
	"time"

	badger "github.com/dgraph-io/badger/v4"
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
	badgerPath        = "/tmp/badger"
	crontab           = "59 23 * * *"
	apiTelegramServer = "https://api.telegram.org"
)

type App struct {
	log       *slog.Logger
	crn       *cron.Cron
	bt        *bot.Bot
	db        *badger.DB
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
	dbPath := os.Getenv("COLLAGIFY_BADGER_PATH")
	if dbPath == "" {
		dbPath = badgerPath
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
	err = a.initDB(badgerPath)
	if err != nil {
		return nil, err
	}

	return a, nil
}

func (a *App) initDB(dbPath string) error {
	db, err := badger.Open(badger.DefaultOptions(dbPath))
	if err != nil {
		return fmt.Errorf("init db: %w", err)
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
	log := a.log.WithGroup("cron")
	log.Info("cron task start")

	var chats []int64
	err := a.db.View(func(tx *badger.Txn) error {
		items, err := tx.Get([]byte("chats"))
		if err != nil {
			return fmt.Errorf("get: %w", err)
		}

		err = items.Value(func(val []byte) error {
			const chunkSize = 8
			for i := 0; i < len(val); i += chunkSize {
				chunkBytes := val[i : i+chunkSize]
				chatID := int64(binary.LittleEndian.Uint64(chunkBytes))
				chats = append(chats, chatID)
			}

			return nil
		})
		if err != nil {
			err = fmt.Errorf("collect: %w", err)
		}

		return err
	})
	if err != nil {
		return fmt.Errorf("fetch registered chats: %w", err)
	}

	log.Debug("chats to range", slog.Any("chats", chats))

	date := time.Now().In(moscowLoc).Format(time.DateOnly)
	var funcErr error
	for _, chatID := range chats {
		log := log.With(slog.Int64("chat_id", chatID))
		var links []string
		err := a.db.View(func(txn *badger.Txn) error {
			opts := badger.DefaultIteratorOptions
			opts.Prefix = []byte(fmt.Sprintf("%d_%s_", chatID, date))

			it := txn.NewIterator(opts)
			defer it.Close()

			for it.Rewind(); it.Valid(); it.Next() {
				value, err := it.Item().ValueCopy(nil)
				if err != nil {
					return err
				}

				links = append(links, string(value))
			}

			return nil
		})
		if err != nil {
			funcErr = errors.Join(funcErr, fmt.Errorf("reading keys by prefix: %w", err))
			continue
		}

		log.Debug("links", slog.Int("count", len(links)))

		images := make([][]byte, 0, len(links))
		for _, link := range links {
			resp, err := http.Get(link)
			if err != nil {
				funcErr = errors.Join(funcErr, fmt.Errorf("download link %s: %w", link, err))
				continue
			}
			defer resp.Body.Close()

			body, err := io.ReadAll(resp.Body)
			if err != nil {
				funcErr = errors.Join(funcErr, fmt.Errorf("reading response body: %w", err))
				continue
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
			funcErr = errors.Join(funcErr, fmt.Errorf("make collage: %w", err))
			continue
		}

		_, err = a.bt.SendPhoto(context.TODO(), &bot.SendPhotoParams{
			ChatID: chatID,
			Photo:  &models.InputFileUpload{Filename: fmt.Sprintf("collage_%s.jpg", date), Data: bytes.NewReader(collage)},
		})
		if err != nil {
			funcErr = errors.Join(funcErr, fmt.Errorf("send collage: %w", err))
		}
	}

	return funcErr
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

	/*b.SendMessage(ctx, &bot.SendMessageParams{
	ChatID: update.Message.Chat.ID,
	Text:   update.Message.Text,
	})*/
}

func (a *App) botHandleMyChatMember(ctx context.Context, r *models.ChatMemberUpdated) error {
	err := a.db.Update(func(tx *badger.Txn) error {
		k := []byte("chats")
		chats, err := tx.Get(k)
		if err != nil {
			if !errors.Is(err, badger.ErrKeyNotFound) {
				return fmt.Errorf("fetch registered chats: %w", err)
			}

			v := make([]byte, 8)
			binary.LittleEndian.PutUint64(v, uint64(r.Chat.ID))

			return tx.Set(k, v)
		}

		var existingValue []byte
		err = chats.Value(func(val []byte) error {
			existingValue = append(existingValue, val...)
			return nil
		})
		if err != nil {
			return fmt.Errorf("read existing value: %w", err)
		}

		v := make([]byte, 8)
		binary.LittleEndian.PutUint64(v, uint64(r.Chat.ID))

		newValue := append(existingValue, v...)

		return tx.Set(k, newValue)
	})
	if err != nil {
		return fmt.Errorf("failed to register chat: %w", err)
	}

	return nil
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

	err = a.db.Update(func(tx *badger.Txn) error {
		now := time.Unix(int64(m.Date), 0).In(moscowLoc)
		chatID := m.Chat.ID
		day := now.Format(time.DateOnly)
		clock := now.Format(time.TimeOnly)

		k := []byte(fmt.Sprintf("%d_%s_%s", chatID, day, clock))
		v := []byte(link)

		return tx.Set(k, v)
	})
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
