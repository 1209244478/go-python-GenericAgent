package auth

import (
	"database/sql"
	"fmt"
	"time"

	_ "github.com/go-sql-driver/mysql"
	_ "modernc.org/sqlite"
	"golang.org/x/crypto/bcrypt"
)

type User struct {
	ID        int64  `json:"id"`
	Email     string `json:"email"`
	Password  string `json:"-"`
	Name      string `json:"name"`
	CreatedAt string `json:"created_at"`
}

type UserStore struct {
	db   *sql.DB
	drv  string
}

type DBConfig struct {
	Driver   string `json:"driver"`
	DSN      string `json:"dsn"`
	SQLitePath string `json:"sqlite_path"`
}

func NewUserStore(cfg DBConfig) (*UserStore, error) {
	var db *sql.DB
	var drv string
	var err error

	if cfg.Driver == "mysql" && cfg.DSN != "" {
		drv = "mysql"
		db, err = sql.Open("mysql", cfg.DSN+"?parseTime=true&charset=utf8mb4")
		if err != nil {
			return nil, fmt.Errorf("open mysql: %w", err)
		}
		db.SetMaxOpenConns(10)
		db.SetMaxIdleConns(5)
		if pingErr := db.Ping(); pingErr != nil {
			return nil, fmt.Errorf("ping mysql: %w", pingErr)
		}
	} else {
		drv = "sqlite"
		dbPath := cfg.SQLitePath
		if dbPath == "" {
			dbPath = "users.db"
		}
		db, err = sql.Open("sqlite", dbPath+"?_journal_mode=WAL&_busy_timeout=5000")
		if err != nil {
			return nil, fmt.Errorf("open sqlite: %w", err)
		}
	}

	s := &UserStore{db: db, drv: drv}
	if err := s.migrate(); err != nil {
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return s, nil
}

func (s *UserStore) migrate() error {
	if s.drv == "mysql" {
		_, err := s.db.Exec(`
			CREATE TABLE IF NOT EXISTS users (
				id         BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
				email      VARCHAR(255)    NOT NULL UNIQUE,
				password   VARCHAR(255)    NOT NULL,
				name       VARCHAR(100)    NOT NULL DEFAULT '',
				created_at DATETIME        NOT NULL DEFAULT CURRENT_TIMESTAMP
			) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4
		`)
		if err != nil {
			return err
		}
		s.db.Exec(`CREATE INDEX idx_users_email ON users(email)`)
		return nil
	}

	_, err := s.db.Exec(`
		CREATE TABLE IF NOT EXISTS users (
			id         INTEGER PRIMARY KEY AUTOINCREMENT,
			email      TEXT    NOT NULL UNIQUE,
			password   TEXT    NOT NULL,
			name       TEXT    NOT NULL DEFAULT '',
			created_at TEXT    NOT NULL DEFAULT (datetime('now'))
		)
	`)
	if err != nil {
		return err
	}
	s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_users_email ON users(email)`)
	return nil
}

func (s *UserStore) Create(email, password, name string) (*User, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return nil, fmt.Errorf("hash password: %w", err)
	}

	result, err := s.db.Exec(
		"INSERT INTO users (email, password, name) VALUES (?, ?, ?)",
		email, string(hash), name,
	)
	if err != nil {
		return nil, fmt.Errorf("insert user: %w", err)
	}

	id, _ := result.LastInsertId()
	return &User{
		ID:        id,
		Email:     email,
		Name:      name,
		CreatedAt: time.Now().Format("2006-01-02 15:04:05"),
	}, nil
}

func (s *UserStore) GetByEmail(email string) (*User, error) {
	u := &User{}
	err := s.db.QueryRow(
		"SELECT id, email, password, name, created_at FROM users WHERE email = ?",
		email,
	).Scan(&u.ID, &u.Email, &u.Password, &u.Name, &u.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return u, nil
}

func (s *UserStore) GetByID(id int64) (*User, error) {
	u := &User{}
	err := s.db.QueryRow(
		"SELECT id, email, password, name, created_at FROM users WHERE id = ?",
		id,
	).Scan(&u.ID, &u.Email, &u.Password, &u.Name, &u.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return u, nil
}

func (s *UserStore) VerifyPassword(u *User, password string) bool {
	err := bcrypt.CompareHashAndPassword([]byte(u.Password), []byte(password))
	return err == nil
}

func (s *UserStore) Close() error {
	return s.db.Close()
}
