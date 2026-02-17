CREATE TABLE IF NOT EXISTS prices (
    id BIGSERIAL PRIMARY KEY,
    name TEXT NOT NULL,
    category TEXT NOT NULL,
    price BIGINT NOT NULL CHECK (price > 0),
    create_date DATE NOT NULL,
    CONSTRAINT prices_unique_row UNIQUE (name, category, price, create_date)
);
