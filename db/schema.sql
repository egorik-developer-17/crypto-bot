CREATE TABLE IF NOT EXISTS portfolio (
    id         SERIAL PRIMARY KEY,
    symbol     TEXT NOT NULL,
    name       TEXT NOT NULL,
    amount     NUMERIC(20, 8) NOT NULL,
    buy_price  NUMERIC(20, 8) NOT NULL,
    added_at   TIMESTAMP DEFAULT NOW()
);
