

-- Load the pgcrypto extension to enable UUID generation functions.
CREATE EXTENSION IF NOT EXISTS pgcrypto;

CREATE TYPE game_outcome AS ENUM ('ongoing', 'forfeit', 'true_win', 'timeout', 'crash', 'veryshort');

CREATE TABLE users (
    user_id UUID PRIMARY KEY,
    username TEXT NOT NULL UNIQUE,
    password_hash TEXT NOT NULL,
    ...
);

CREATE TABLE games (
    game_id UUID PRIMARY KEY,
    playerA_id UUID REFERENCES users(user_id),
    playerB_id UUID REFERENCES users(user_id),
    outcome game_outcome DEFAULT 'ongoing',
    game_start_time TIMESTAMPTZ DEFAULT current_timestamp,
    ...
);

CREATE INDEX idx_outcome ON games(outcome);

CREATE TABLE moves (
    move_id UUID PRIMARY KEY,
    move_counter INT NOT NULL DEFAULT SERIAL_NEXTVAL('moves_move_id_seq'),
    game_id UUID REFERENCES games(game_id),
    player_code CHAR(1) CHECK (player_code IN ('A', 'B')),
    player_game_code TEXT,
    move_description TEXT,
    move_time TIMESTAMPTZ DEFAULT current_timestamp
);

CREATE INDEX idx_moves_player_game_code ON moves(player_game_code);
CREATE INDEX idx_moves_game_id ON moves(game_id);



-- --
-- PERMISSIONS
-- --

-- Prevent public access to the users table.
REVOKE ALL ON users FROM PUBLIC;

-- Grant read and write accesses.
GRANT SELECT ON games, moves TO app_read;
GRANT SELECT(username, user_id) ON users TO app_read;

GRANT INSERT, UPDATE, DELETE ON games, moves TO app_write;
GRANT SELECT(username, user_id) ON users TO app_read;
GRANT SELECT(password_hash) ON users TO app_auth;

-- Grant execution permission on certain functions
GRANT EXECUTE ON FUNCTION add_new_game(UUID, UUID), fetch_ongoing_games(UUID, UUID), delete_game(UUID) TO app_read, app_write;
GRANT EXECUTE ON FUNCTION update_game_outcome(UUID, game_outcome), add_new_user(TEXT, TEXT) TO app_write;
-- I need to add more of this specificationing on the function level. but later!!