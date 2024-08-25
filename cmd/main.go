package main

import (
	"bytes"
	"cmp"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net/http"
	"slices"
	"time"

	badger "github.com/dgraph-io/badger/v4"
	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/robfig/cron"

	"github.com/nikgalushko/collagify-tg/pkg/image"
)

type App struct {
	log   *slog.Logger
	token string
	crn   *cron.Cron
	bt    *bot.Bot
	db    *badger.DB
}

func New() *App {
	a := new(App)
	a.initCron()
	a.initBot()
	a.initDB()

	return a
}

func (a *App) initDB() {
	db, err := badger.Open(badger.DefaultOptions("/tmp/badger"))
	if err != nil {
		log.Fatal(err)
	}

	a.db = db
}

func (a *App) initCron() {
	c := cron.New()
	c.AddFunc("27 17 * * *", a.cronHandler)
	a.crn = c
}

func (a *App) initBot() {
	opts := []bot.Option{
		bot.WithDefaultHandler(a.botHandler),
	}

	b, err := bot.New(a.token, opts...)
	if err != nil {
		panic(err)
	}

	a.bt = b
}

func (a *App) Start(ctx context.Context) {
	a.crn.Start()
	a.bt.Start(ctx)
}

func (a *App) Close() error {
	a.crn.Stop()
	return a.db.Close()
}

func (a *App) cronHandler() {
	log.Println("[INFO] cron task start")

	var chats []int64
	err := a.db.View(func(tx *badger.Txn) error {
		items, err := tx.Get([]byte("chats"))
		if err != nil {
			return fmt.Errorf("get : %w", err)
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
		log.Println("[ERROR] fetch registered chats", err)
	}

	log.Println("[DEBUG] chats", chats)

	date := time.Now().Format(time.DateOnly)
	for _, chatID := range chats {
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
			log.Println("[ERROR] reading keys by prefix", err)
			continue
		}

		fmt.Println(len(links))

		images := make([][]byte, 0, len(links))
		for _, link := range links {
			resp, err := http.Get(link)
			if err != nil {
				log.Println("[ERROR] downloading link", err)
				continue
			}
			defer resp.Body.Close()

			body, err := io.ReadAll(resp.Body)
			if err != nil {
				log.Println("[ERROR] reading response body", err)
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
			log.Println("[ERROR] make collage", err)
			continue
		}

		_, err = a.bt.SendPhoto(context.TODO(), &bot.SendPhotoParams{
			ChatID: chatID,
			Photo:  &models.InputFileUpload{Filename: fmt.Sprintf("collage_%s.jpg", date), Data: bytes.NewReader(collage)},
		})
		if err != nil {
			log.Println("[ERROR] send collage", err)
		}
	}
}

func (a *App) botHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	if update.ChannelPost == nil && update.MyChatMember == nil {
		log.Printf("[WARN] usupported update event: %+v\n", *update)
		return
	}

	if update.ChannelPost != nil {
		a.botHandleChannelPost(ctx, b, update.ChannelPost)
	}

	if update.MyChatMember != nil {
		a.botHandleMyChatMember(ctx, b, update.MyChatMember)
	}

	/*b.SendMessage(ctx, &bot.SendMessageParams{
	ChatID: update.Message.Chat.ID,
	Text:   update.Message.Text,
	})*/
}

func (a *App) botHandleMyChatMember(ctx context.Context, b *bot.Bot, r *models.ChatMemberUpdated) {
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
		log.Println("[ERROR] failed to register chat", err)
	}
}

func (a *App) botHandleChannelPost(ctx context.Context, b *bot.Bot, m *models.Message) {
	if len(m.Photo) == 0 {
		log.Println("[WARN] message without photo")
		return
	}

	slices.SortFunc(m.Photo, func(a, b models.PhotoSize) int {
		return cmp.Compare(a.FileSize, b.FileSize)
	})

	largestPhoto := m.Photo[len(m.Photo)-1]
	f, err := b.GetFile(ctx, &bot.GetFileParams{FileID: largestPhoto.FileID})
	if err != nil {
		log.Println("[ERROR] get file info", err)
		return
	}

	link := b.FileDownloadLink(f)
	log.Println("download file link", link)

	err = a.db.Update(func(tx *badger.Txn) error {
		now := time.Now()
		chatID := m.Chat.ID
		day := now.Format(time.DateOnly)
		clock := now.Format(time.TimeOnly)

		k := []byte(fmt.Sprintf("%d_%s_%d", chatID, day, clock))
		v := []byte(link)

		return tx.Set(k, v)
	})
	if err != nil {
		log.Println("[ERROR] save file link", err)
	}
}

func main() {
	//ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	//defer cancel()

	a := New()
	defer a.Close()

	a.cronHandler()

	//a.Start(ctx)
}
