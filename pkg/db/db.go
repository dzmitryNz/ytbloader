package db

import (
	"database/sql"
	"fmt"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

type DB struct {
	conn *sql.DB
}

type Download struct {
	ID          int64
	URL         string
	Title       string
	Status      string
	OutputPath  string
	CreatedAt   time.Time
	UpdatedAt   time.Time
	MessengerID string
	UserID      int64
	ChatID      int64
	SendFile    bool
}

type Task struct {
	ID          int64
	Title       string
	Description string
	Status      string
	Priority    int
	MessengerID string
	UserID      int64
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

type Channel struct {
	ID          int64
	URL         string
	Name        string
	MessengerID string
	UserID      int64
	ChatID      int64
	LastCheck   time.Time
	CreatedAt   time.Time
}

type Video struct {
	ID        int64
	ChannelID int64
	URL       string
	Title     string
	Duration  int
	Published time.Time
	Notified  bool
	CreatedAt time.Time
}

func New(dbPath string) (*DB, error) {
	conn, err := sql.Open("sqlite3", dbPath+"?_journal_mode=WAL")
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	db := &DB{conn: conn}
	if err := db.migrate(); err != nil {
		return nil, fmt.Errorf("failed to migrate database: %w", err)
	}

	return db, nil
}

func (db *DB) Close() error {
	return db.conn.Close()
}

func (db *DB) migrate() error {
	queries := []string{
		`CREATE TABLE IF NOT EXISTS downloads (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			url TEXT NOT NULL,
			title TEXT,
			status TEXT NOT NULL DEFAULT 'pending',
			output_path TEXT,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			messenger_id TEXT,
			user_id INTEGER,
			chat_id INTEGER DEFAULT 0,
			send_file INTEGER DEFAULT 0
		)`,
		`CREATE TABLE IF NOT EXISTS tasks (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			title TEXT NOT NULL,
			description TEXT,
			status TEXT NOT NULL DEFAULT 'open',
			priority INTEGER DEFAULT 0,
			messenger_id TEXT,
			user_id INTEGER,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE TABLE IF NOT EXISTS channels (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			url TEXT NOT NULL,
			name TEXT,
			messenger_id TEXT,
			user_id INTEGER,
			chat_id INTEGER DEFAULT 0,
			last_check DATETIME DEFAULT CURRENT_TIMESTAMP,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			UNIQUE(url, user_id, messenger_id)
		)`,
		`CREATE TABLE IF NOT EXISTS videos (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			channel_id INTEGER,
			url TEXT NOT NULL,
			title TEXT,
			duration INTEGER DEFAULT 0,
			published DATETIME,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			FOREIGN KEY(channel_id) REFERENCES channels(id)
		)`,
	}

	for _, q := range queries {
		if _, err := db.conn.Exec(q); err != nil {
			return err
		}
	}

	return nil
}

func (db *DB) CreateDownload(d *Download) error {
	sendFile := 0
	if d.SendFile {
		sendFile = 1
	}
	result, err := db.conn.Exec(
		`INSERT INTO downloads (url, title, status, output_path, messenger_id, user_id, chat_id, send_file) 
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		d.URL, d.Title, d.Status, d.OutputPath, d.MessengerID, d.UserID, d.ChatID, sendFile,
	)
	if err != nil {
		return err
	}
	d.ID, _ = result.LastInsertId()
	return nil
}

func (db *DB) UpdateDownloadStatus(id int64, status string) error {
	_, err := db.conn.Exec(
		`UPDATE downloads SET status = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
		status, id,
	)
	return err
}

func (db *DB) GetDownload(id int64) (*Download, error) {
	d := &Download{}
	var sendFile int
	err := db.conn.QueryRow(
		`SELECT id, url, title, status, output_path, created_at, updated_at, messenger_id, user_id, chat_id, send_file 
		 FROM downloads WHERE id = ?`, id,
	).Scan(&d.ID, &d.URL, &d.Title, &d.Status, &d.OutputPath, &d.CreatedAt, &d.UpdatedAt, &d.MessengerID, &d.UserID, &d.ChatID, &sendFile)
	if err != nil {
		return nil, err
	}
	d.SendFile = sendFile == 1
	return d, nil
}

func (db *DB) CreateTask(t *Task) error {
	result, err := db.conn.Exec(
		`INSERT INTO tasks (title, description, status, priority, messenger_id, user_id) 
		 VALUES (?, ?, ?, ?, ?, ?)`,
		t.Title, t.Description, t.Status, t.Priority, t.MessengerID, t.UserID,
	)
	if err != nil {
		return err
	}
	t.ID, _ = result.LastInsertId()
	return nil
}

func (db *DB) UpdateTaskStatus(id int64, status string) error {
	_, err := db.conn.Exec(
		`UPDATE tasks SET status = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
		status, id,
	)
	return err
}

func (db *DB) GetTask(id int64) (*Task, error) {
	t := &Task{}
	err := db.conn.QueryRow(
		`SELECT id, title, description, status, priority, messenger_id, user_id, created_at, updated_at 
		 FROM tasks WHERE id = ?`, id,
	).Scan(&t.ID, &t.Title, &t.Description, &t.Status, &t.Priority, &t.MessengerID, &t.UserID, &t.CreatedAt, &t.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return t, nil
}

func (db *DB) ListTasks(messengerID string, userID int64) ([]Task, error) {
	rows, err := db.conn.Query(
		`SELECT id, title, description, status, priority, messenger_id, user_id, created_at, updated_at 
		 FROM tasks WHERE messenger_id = ? AND user_id = ? ORDER BY created_at DESC`,
		messengerID, userID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tasks []Task
	for rows.Next() {
		var t Task
		if err := rows.Scan(&t.ID, &t.Title, &t.Description, &t.Status, &t.Priority, &t.MessengerID, &t.UserID, &t.CreatedAt, &t.UpdatedAt); err != nil {
			return nil, err
		}
		tasks = append(tasks, t)
	}

	return tasks, nil
}

func (db *DB) CreateChannel(c *Channel) error {
	result, err := db.conn.Exec(
		`INSERT OR IGNORE INTO channels (url, name, messenger_id, user_id, chat_id) 
		 VALUES (?, ?, ?, ?, ?)`,
		c.URL, c.Name, c.MessengerID, c.UserID, c.ChatID,
	)
	if err != nil {
		return err
	}
	c.ID, _ = result.LastInsertId()
	return nil
}

func (db *DB) GetChannel(id int64) (*Channel, error) {
	c := &Channel{}
	err := db.conn.QueryRow(
		`SELECT id, url, name, messenger_id, user_id, chat_id, last_check, created_at 
		 FROM channels WHERE id = ?`, id,
	).Scan(&c.ID, &c.URL, &c.Name, &c.MessengerID, &c.UserID, &c.ChatID, &c.LastCheck, &c.CreatedAt)
	if err != nil {
		return nil, err
	}
	return c, nil
}

func (db *DB) GetChannelByURL(url string, messengerID string, userID int64) (*Channel, error) {
	c := &Channel{}
	err := db.conn.QueryRow(
		`SELECT id, url, name, messenger_id, user_id, chat_id, last_check, created_at 
		 FROM channels WHERE url = ? AND messenger_id = ? AND user_id = ?`, url, messengerID, userID,
	).Scan(&c.ID, &c.URL, &c.Name, &c.MessengerID, &c.UserID, &c.ChatID, &c.LastCheck, &c.CreatedAt)
	if err != nil {
		return nil, err
	}
	return c, nil
}

func (db *DB) ListChannels(messengerID string, userID int64) ([]Channel, error) {
	rows, err := db.conn.Query(
		`SELECT id, url, name, messenger_id, user_id, chat_id, last_check, created_at 
		 FROM channels WHERE messenger_id = ? AND user_id = ? ORDER BY created_at DESC`,
		messengerID, userID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var channels []Channel
	for rows.Next() {
		var c Channel
		if err := rows.Scan(&c.ID, &c.URL, &c.Name, &c.MessengerID, &c.UserID, &c.ChatID, &c.LastCheck, &c.CreatedAt); err != nil {
			return nil, err
		}
		channels = append(channels, c)
	}

	return channels, nil
}

func (db *DB) DeleteChannel(id int64) error {
	_, err := db.conn.Exec(`DELETE FROM channels WHERE id = ?`, id)
	return err
}

func (db *DB) UpdateChannelLastCheck(id int64) error {
	_, err := db.conn.Exec(
		`UPDATE channels SET last_check = CURRENT_TIMESTAMP WHERE id = ?`, id,
	)
	return err
}

func (db *DB) UpsertVideo(v *Video) error {
	result, err := db.conn.Exec(
		`INSERT OR IGNORE INTO videos (channel_id, url, title, duration, published) 
		 VALUES (?, ?, ?, ?, ?)`,
		v.ChannelID, v.URL, v.Title, v.Duration, v.Published,
	)
	if err != nil {
		return err
	}
	v.ID, _ = result.LastInsertId()
	return nil
}

func (db *DB) ListNewVideos(channelID int64, since time.Time) ([]Video, error) {
	rows, err := db.conn.Query(
		`SELECT id, channel_id, url, title, duration, published, created_at 
		 FROM videos WHERE channel_id = ? AND created_at > ? ORDER BY published DESC`,
		channelID, since,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var videos []Video
	for rows.Next() {
		var v Video
		if err := rows.Scan(&v.ID, &v.ChannelID, &v.URL, &v.Title, &v.Duration, &v.Published, &v.CreatedAt); err != nil {
			return nil, err
		}
		videos = append(videos, v)
	}

	return videos, nil
}

func (db *DB) ListAllVideos(channelID int64, limit, offset int) ([]Video, error) {
	rows, err := db.conn.Query(
		`SELECT id, channel_id, url, title, duration, published, created_at 
		 FROM videos WHERE channel_id = ? ORDER BY published DESC LIMIT ? OFFSET ?`,
		channelID, limit, offset,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var videos []Video
	for rows.Next() {
		var v Video
		if err := rows.Scan(&v.ID, &v.ChannelID, &v.URL, &v.Title, &v.Duration, &v.Published, &v.CreatedAt); err != nil {
			return nil, err
		}
		videos = append(videos, v)
	}

	return videos, nil
}

func (db *DB) CountVideos(channelID int64) (int, error) {
	var count int
	err := db.conn.QueryRow(
		`SELECT COUNT(*) FROM videos WHERE channel_id = ?`, channelID,
	).Scan(&count)
	return count, err
}