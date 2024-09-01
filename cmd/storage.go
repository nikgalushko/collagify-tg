package main

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

const (
	chatsTable = `
		create table if not exists chats (
			chat_id integer not null primary key,
			timestamp integer not null
		);
	`
	linksTable = `
		create table if not exists links (
			chat_id integer,
			timestamp integer not null,
			url text not null,
			message_id integer not null
		);
	`
)

type storage struct {
	mu sync.RWMutex
	db *sql.DB
}

func NewStorage(path string) (*storage, error) {
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		return nil, fmt.Errorf("open db file: %w", err)
	}

	if _, err := db.Exec(`PRAGMA journal_mode = WAL;`); err != nil {
		return nil, err
	}
	if _, err := db.Exec(`PRAGMA synchronous = normal;`); err != nil {
		return nil, err
	}
	if _, err := db.Exec(`PRAGMA temp_store = memory;`); err != nil {
		return nil, err
	}

	if _, err := db.Exec(chatsTable); err != nil {
		return nil, fmt.Errorf("create chats table: %w", err)
	}
	if _, err := db.Exec(linksTable); err != nil {
		return nil, fmt.Errorf("create links table: %w", err)
	}

	return &storage{db: db}, nil
}

func (s *storage) RegisterChat(ctx context.Context, chatID int64, date time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.db.ExecContext(ctx, `insert into chats (chat_id, timestamp) values(?,?)`, chatID, date.Unix())
	if err != nil {
		return fmt.Errorf("register chat: %w", err)
	}

	return err
}

func (s *storage) RegistreLink(ctx context.Context, chatID, messageID int64, datetime time.Time, link string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.db.ExecContext(ctx, `insert into links (chat_id, timestamp, url, message_id) values (?,?,?,?)`,
		chatID, datetime.Unix(), link, messageID,
	)
	if err != nil {
		return fmt.Errorf("register new link: %w", err)
	}

	return nil
}

func (s *storage) Chats(ctx context.Context) ([]int64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	rows, err := s.db.QueryContext(ctx, `select chat_id from chats`)
	if err != nil {
		return nil, fmt.Errorf("select chats: %w", err)
	}
	defer rows.Close()

	var chats []int64
	for rows.Next() {
		var chatID int64
		err := rows.Scan(&chatID)
		if err != nil {
			return nil, fmt.Errorf("scan chat: %w", err)
		}
		chats = append(chats, chatID)
	}

	return chats, nil
}

type toCollage struct {
	date  string
	links []string
}

func (s *storage) Links(ctx context.Context, chatID int64) ([]int64, []toCollage, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	rows, err := s.db.QueryContext(ctx, `select timestamp, url, message_id from links where chat_id = ? order by timestamp asc`, chatID)
	if err != nil {
		return nil, nil, fmt.Errorf("select links: %w", err)
	}
	defer rows.Close()

	var (
		messages     []int64
		toCollageArr []toCollage
		prevDate     string
		i            = -1
	)
	for rows.Next() {
		var (
			messageID int64
			link      string
			timestamp int64
		)
		err := rows.Scan(&timestamp, &link, &messageID)
		if err != nil {
			return nil, nil, fmt.Errorf("scan links: %w", err)
		}

		messages = append(messages, messageID)
		date := time.Unix(timestamp, 0).Format(time.DateOnly)
		if prevDate != date {
			toCollageArr = append(toCollageArr, toCollage{date: date})
			prevDate = date
			i++
		}
		toCollageArr[i].links = append(toCollageArr[i].links, link)
	}

	return messages, toCollageArr, nil
}

func (s *storage) Close() error {
	return s.db.Close()
}
