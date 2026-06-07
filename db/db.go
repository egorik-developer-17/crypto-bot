package db

import (
	"database/sql"
	"fmt"
	"strings"

	_ "modernc.org/sqlite"
)

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
	_, _ = d.conn.Exec(`ALTER TABLE portfolio ADD COLUMN user_id INTEGER NOT NULL DEFAULT 0`)
	return nil
}

func (d *DB) AddPosition(userID int64, symbol, name string, amount, buyPrice float64) error {
	_, err := d.conn.Exec(
		`INSERT INTO portfolio (user_id, symbol, name, amount, buy_price) VALUES (?, ?, ?, ?, ?)`,
		userID, symbol, name, amount, buyPrice,
	)
	return err
}

// RemoveBySymbol удаляет позицию пользователя по символу монеты (например "btc")
func (d *DB) RemoveBySymbol(userID int64, symbol string) (string, error) {
	// Ищем запись чтобы вернуть имя монеты в ответе
	var name string
	err := d.conn.QueryRow(
		`SELECT name FROM portfolio WHERE user_id = ? AND (symbol = ? OR name = ?) COLLATE NOCASE LIMIT 1`,
		userID, strings.ToLower(symbol), symbol,
	).Scan(&name)
	if err == sql.ErrNoRows {
		return "", fmt.Errorf("монета «%s» не найдена в вашем портфеле", symbol)
	}
	if err != nil {
		return "", err
	}

	_, err = d.conn.Exec(
		`DELETE FROM portfolio WHERE user_id = ? AND (symbol = ? OR name = ?) COLLATE NOCASE`,
		userID, strings.ToLower(symbol), symbol,
	)
	return name, err
}

func (d *DB) GetPortfolio(userID int64) ([]Position, error) {
	// Нумерация позиций — через ROW_NUMBER чтобы всегда была последовательной с 1
	rows, err := d.conn.Query(
		`SELECT ROW_NUMBER() OVER (ORDER BY added_at) AS num, symbol, name, amount, buy_price
		 FROM portfolio WHERE user_id = ? ORDER BY added_at`,
		userID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var positions []Position
	for rows.Next() {
		var p Position
		if err := rows.Scan(&p.ID, &p.Symbol, &p.Name, &p.Amount, &p.BuyPrice); err != nil {
			return nil, err
		}
		positions = append(positions, p)
	}
	return positions, rows.Err()
}
