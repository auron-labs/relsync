-- +goose Up
CREATE TABLE activity (
    id INTEGER PRIMARY KEY,
    at INTEGER NOT NULL,
    level TEXT NOT NULL,
    item_id INTEGER REFERENCES items (id) ON DELETE SET NULL,
    message TEXT NOT NULL
);

-- +goose Down
DROP TABLE activity;
