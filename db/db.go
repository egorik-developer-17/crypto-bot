package db

import (
	"database/sql"
	"fmt"

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
	// Создаём таблицу с user_id
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
	// Миграция: добавляем user_id если таблица уже существовала без него
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

func (d *DB) RemovePosition(userID int64, id int) error {
	res, err := d.conn.Exec(`DELETE FROM portfolio WHERE id = ? AND user_id = ?`, id, userID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("позиция #%d не найдена", id)
	}
	return nil
}

func (d *DB) GetPortfolio(userID int64) ([]Position, error) {
	rows, err := d.conn.Query(
		`SELECT id, symbol, name, amount, buy_price FROM portfolio WHERE user_id = ? ORDER BY added_at`,
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
