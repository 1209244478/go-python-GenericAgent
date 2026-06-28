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
	UserType  string `json:"user_type"` // "admin" 或 "user"
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
				user_type  VARCHAR(20)     NOT NULL DEFAULT 'user',
				created_at DATETIME        NOT NULL DEFAULT CURRENT_TIMESTAMP
			) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4
		`)
		if err != nil {
			return err
		}
		s.db.Exec(`CREATE INDEX idx_users_email ON users(email)`)

		_, err = s.db.Exec(`
			CREATE TABLE IF NOT EXISTS sessions (
				id         BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
				user_id    BIGINT UNSIGNED NOT NULL,
				name       VARCHAR(200)    NOT NULL DEFAULT 'default',
				created_at DATETIME        NOT NULL DEFAULT CURRENT_TIMESTAMP,
				INDEX idx_sessions_user (user_id),
				FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
			) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4
		`)
		return err
	}

	_, err := s.db.Exec(`
		CREATE TABLE IF NOT EXISTS users (
			id         INTEGER PRIMARY KEY AUTOINCREMENT,
			email      TEXT    NOT NULL UNIQUE,
			password   TEXT    NOT NULL,
			name       TEXT    NOT NULL DEFAULT '',
			user_type  TEXT    NOT NULL DEFAULT 'user',
			created_at TEXT    NOT NULL DEFAULT (datetime('now'))
		)
	`)
	if err != nil {
		return err
	}
	s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_users_email ON users(email)`)

	_, err = s.db.Exec(`
		CREATE TABLE IF NOT EXISTS sessions (
			id         INTEGER PRIMARY KEY AUTOINCREMENT,
			user_id    INTEGER NOT NULL,
			name       TEXT    NOT NULL DEFAULT 'default',
			created_at TEXT    NOT NULL DEFAULT (datetime('now'))
		)
	`)
	if err != nil {
		return err
	}
	s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_sessions_user ON sessions(user_id)`)
	return nil
}

func (s *UserStore) Create(email, password, name, userType string) (*User, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return nil, fmt.Errorf("hash password: %w", err)
	}

	// 默认为普通用户
	if userType == "" {
		userType = "user"
	}

	result, err := s.db.Exec(
		"INSERT INTO users (email, password, name, user_type) VALUES (?, ?, ?, ?)",
		email, string(hash), name, userType,
	)
	if err != nil {
		return nil, fmt.Errorf("insert user: %w", err)
	}

	id, _ := result.LastInsertId()
	return &User{
		ID:        id,
		Email:     email,
		Name:      name,
		UserType:  userType,
		CreatedAt: time.Now().Format("2006-01-02 15:04:05"),
	}, nil
}

func (s *UserStore) GetByEmail(email string) (*User, error) {
	u := &User{}
	err := s.db.QueryRow(
		"SELECT id, email, password, name, user_type, created_at FROM users WHERE email = ?",
		email,
	).Scan(&u.ID, &u.Email, &u.Password, &u.Name, &u.UserType, &u.CreatedAt)
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
		"SELECT id, email, password, name, user_type, created_at FROM users WHERE id = ?",
		id,
	).Scan(&u.ID, &u.Email, &u.Password, &u.Name, &u.UserType, &u.CreatedAt)
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

type Session struct {
	ID        int64  `json:"id"`
	UserID    int64  `json:"user_id"`
	Name      string `json:"name"`
	CreatedAt string `json:"created_at"`
}

func (s *UserStore) CreateSession(userID int64, name string) (*Session, error) {
	if name == "" {
		name = "default"
	}
	result, err := s.db.Exec(
		"INSERT INTO sessions (user_id, name) VALUES (?, ?)",
		userID, name,
	)
	if err != nil {
		return nil, err
	}
	id, _ := result.LastInsertId()
	return &Session{
		ID:        id,
		UserID:    userID,
		Name:      name,
		CreatedAt: time.Now().Format("2006-01-02 15:04:05"),
	}, nil
}

func (s *UserStore) ListSessions(userID int64) ([]Session, error) {
	rows, err := s.db.Query(
		"SELECT id, user_id, name, created_at FROM sessions WHERE user_id = ? ORDER BY id",
		userID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var sessions []Session
	for rows.Next() {
		var sess Session
		if err := rows.Scan(&sess.ID, &sess.UserID, &sess.Name, &sess.CreatedAt); err != nil {
			continue
		}
		sessions = append(sessions, sess)
	}
	return sessions, nil
}

func (s *UserStore) GetSession(userID, sessionID int64) (*Session, error) {
	sess := &Session{}
	err := s.db.QueryRow(
		"SELECT id, user_id, name, created_at FROM sessions WHERE id = ? AND user_id = ?",
		sessionID, userID,
	).Scan(&sess.ID, &sess.UserID, &sess.Name, &sess.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return sess, nil
}

func (s *UserStore) DeleteSession(userID, sessionID int64) error {
	_, err := s.db.Exec("DELETE FROM sessions WHERE id = ? AND user_id = ?", sessionID, userID)
	return err
}

func (s *UserStore) EnsureDefaultSession(userID int64) (*Session, error) {
	rows, err := s.db.Query(
		"SELECT id, user_id, name, created_at FROM sessions WHERE user_id = ? ORDER BY id LIMIT 1",
		userID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	if rows.Next() {
		var s2 Session
		if err := rows.Scan(&s2.ID, &s2.UserID, &s2.Name, &s2.CreatedAt); err != nil {
			return nil, err
		}
		return &s2, nil
	}

	return s.CreateSession(userID, "default")
}
