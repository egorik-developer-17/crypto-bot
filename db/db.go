package db

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"

	_ "modernc.org/sqlite"
)

const maxPositionsPerUser = 50 // защита от переполнения БД

type Position struct {
	ID       int
	Symbol   string
	Name     string
	Amount   float64
	BuyPrice float64
}

type DB struct {
	conn *sql.DB
}

func New(path string) (*DB, error) {
	conn, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	if err := conn.Ping(); err != nil {
		return nil, fmt.Errorf("нет соединения с БД: %w", err)
	}
	d := &DB{conn: conn}
	return d, d.migrate()
}

func (d *DB) migrate() error {
	_, err := d.conn.Exec(`
		CREATE TABLE IF NOT EXISTS portfolio (
			id        INTEGER PRIMARY KEY AUTOINCREMENT,
			user_id   INTEGER NOT NULL,
			symbol    TEXT NOT NULL,
			name      TEXT NOT NULL,
			amount    REAL NOT NULL,
			buy_price REAL NOT NULL,
			added_at  DATETIME DEFAULT CURRENT_TIMESTAMP
		)
	`)
	if err != nil {
		return err
	}
	// Миграция для старых БД без user_id — ошибку игнорируем намеренно
	_, _ = d.conn.Exec(`ALTER TABLE portfolio ADD COLUMN user_id INTEGER NOT NULL DEFAULT 0`)

	// Таблица для Bybit API-ключей (один аккаунт на пользователя)
	_, err = d.conn.Exec(`
		CREATE TABLE IF NOT EXISTS bybit_keys (
			user_id    INTEGER PRIMARY KEY,
			api_key    TEXT NOT NULL,
			api_secret TEXT NOT NULL,
			added_at   DATETIME DEFAULT CURRENT_TIMESTAMP
		)
	`)
	return err
}

// --- Bybit ключи ---

func (d *DB) SaveBybitKeys(userID int64, apiKey, apiSecret string) error {
	_, err := d.conn.Exec(`
		INSERT INTO bybit_keys (user_id, api_key, api_secret)
		VALUES (?, ?, ?)
		ON CONFLICT(user_id) DO UPDATE SET api_key = excluded.api_key, api_secret = excluded.api_secret, added_at = CURRENT_TIMESTAMP
	`, userID, apiKey, apiSecret)
	if err != nil {
		return errors.New("не удалось сохранить ключи")
	}
	return nil
}

func (d *DB) GetBybitKeys(userID int64) (apiKey, apiSecret string, err error) {
	err = d.conn.QueryRow(
		`SELECT api_key, api_secret FROM bybit_keys WHERE user_id = ?`, userID,
	).Scan(&apiKey, &apiSecret)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", nil // ключей нет — не ошибка
	}
	return apiKey, apiSecret, err
}

func (d *DB) DeleteBybitKeys(userID int64) error {
	res, err := d.conn.Exec(`DELETE FROM bybit_keys WHERE user_id = ?`, userID)
	if err != nil {
		return errors.New("не удалось удалить ключи")
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return errors.New("Bybit аккаунт не подключён")
	}
	return nil
}

func (d *DB) AddPosition(userID int64, symbol, name string, amount, buyPrice float64) error {
	// Лимит позиций на пользователя
	var count int
	if err := d.conn.QueryRow(
		`SELECT COUNT(*) FROM portfolio WHERE user_id = ?`, userID,
	).Scan(&count); err != nil {
		return errors.New("ошибка проверки портфеля")
	}
	if count >= maxPositionsPerUser {
		return fmt.Errorf("достигнут лимит позиций (%d). Удалите старые перед добавлением новых", maxPositionsPerUser)
	}

	_, err := d.conn.Exec(
		`INSERT INTO portfolio (user_id, symbol, name, amount, buy_price) VALUES (?, ?, ?, ?, ?)`,
		userID, symbol, name, amount, buyPrice,
	)
	if err != nil {
		return errors.New("не удалось сохранить позицию")
	}
	return nil
}

// RemoveBySymbol удаляет позицию пользователя по символу монеты
func (d *DB) RemoveBySymbol(userID int64, symbol string) (string, error) {
	var name string
	err := d.conn.QueryRow(
		`SELECT name FROM portfolio WHERE user_id = ? AND (symbol = ? OR name = ?) COLLATE NOCASE LIMIT 1`,
		userID, strings.ToLower(symbol), symbol,
	).Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("монета «%s» не найдена в вашем портфеле", symbol)
	}
	if err != nil {
		return "", errors.New("ошибка поиска позиции")
	}

	if _, err = d.conn.Exec(
		`DELETE FROM portfolio WHERE user_id = ? AND (symbol = ? OR name = ?) COLLATE NOCASE`,
		userID, strings.ToLower(symbol), symbol,
	); err != nil {
		return "", errors.New("не удалось удалить позицию")
	}
	return name, nil
}

func (d *DB) GetPortfolio(userID int64) ([]Position, error) {
	rows, err := d.conn.Query(
		`SELECT ROW_NUMBER() OVER (ORDER BY added_at) AS num, symbol, name, amount, buy_price
		 FROM portfolio WHERE user_id = ? ORDER BY added_at`,
		userID,
	)
	if err != nil {
		return nil, errors.New("не удалось загрузить портфель")
	}
	defer rows.Close()

	var positions []Position
	for rows.Next() {
		var p Position
		if err := rows.Scan(&p.ID, &p.Symbol, &p.Name, &p.Amount, &p.BuyPrice); err != nil {
			return nil, errors.New("ошибка чтения данных")
		}
		positions = append(positions, p)
	}
	return positions, rows.Err()
}
