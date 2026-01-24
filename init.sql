CREATE TABLE IF NOT EXISTS prices (
    id BIGINT PRIMARY KEY,
    name TEXT NOT NULL,
    category TEXT NOT NULL,
    price BIGINT NOT NULL,
    create_date DATE NOT NULL
);