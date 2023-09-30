

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
-- FUNCTIONS
-- --

-- Function to register a new user and return their UUID.
CREATE OR REPLACE FUNCTION add_new_user(p_username TEXT, p_password_hash TEXT) 
RETURNS UUID AS $$
DECLARE
    new_user_id UUID;
BEGIN
    INSERT INTO users (user_id, username, password_hash)
    VALUES (uuid_generate_v4(), p_username, p_password_hash)
    RETURNING user_id INTO new_user_id;

    RETURN new_user_id;
END;
$$ LANGUAGE plpgsql;

-- Function to modify the outcome of an existing game
CREATE OR REPLACE FUNCTION update_game_outcome(p_game_id UUID, p_outcome game_outcome) 
RETURNS void AS $$
BEGIN
    UPDATE games 
    SET outcome = p_outcome 
    WHERE game_id = p_game_id;
END;
$$ LANGUAGE plpgsql;

-- Function to initiate a new game.
CREATE OR REPLACE FUNCTION add_new_game(p_playerA_id UUID, p_playerB_id UUID) 
RETURNS void AS $$
BEGIN
    INSERT INTO games (game_id, playerA_id, playerB_id)
    VALUES (uuid_generate_v4(), p_playerA_id, p_playerB_id);
END;
$$ LANGUAGE plpgsql;


-- Function to fetch all moves associated with a partular ID or either of a pair of player ID values.
CREATE OR REPLACE FUNCTION fetch_ongoing_games(p_game_id UUID DEFAULT NULL, p_player_id UUID DEFAULT NULL) 
RETURNS TABLE(game_id UUID, playerA_id UUID, playerB_id UUID, outcome game_outcome, game_start_time TIMESTAMPTZ) AS $$
BEGIN
    RETURN QUERY
    SELECT * FROM games 
    WHERE (game_id = p_game_id OR playerA_id = p_player_id OR playerB_id = p_player_id) 
    AND outcome = 'ongoing';
END;
$$ LANGUAGE plpgsql;

-- Function to delete a specified game from 'games'.
CREATE OR REPLACE FUNCTION delete_game(p_game_id UUID) 
RETURNS void AS $$
BEGIN
    DELETE FROM games WHERE game_id = p_game_id;
END;
$$ LANGUAGE plpgsql;

-- Function to fetch all moves associated with a particular gameID.
CREATE OR REPLACE FUNCTION fetch_all_moves_for_game(p_game_id UUID) 
RETURNS TABLE(move_id INT, game_id UUID, player_id UUID, player_code CHAR(1), player_game_code TEXT, move_description TEXT, move_time TIMESTAMPTZ) AS $$
BEGIN
    RETURN QUERY
    SELECT move_id, game_id, player_id, player_code, player_game_code, move_description, move_time
    FROM moves 
    WHERE game_id = p_game_id;
END;
$$ LANGUAGE plpgsql;

-- Function to fetch all moves associated with a particular playerID which will be unique to 1 specific game 
-- (e.g. 9000989 - A == the ID for player-A in game #9000989)
CREATE OR REPLACE FUNCTION fetch_1players_moves_for_a_game(p_player_game_code UUID) 
RETURNS TABLE(move_id INT, game_id UUID, player_id UUID, player_code CHAR(1), player_game_code TEXT, move_description TEXT, move_time TIMESTAMPTZ) AS $$
BEGIN
    RETURN QUERY
    SELECT move_id, game_id, player_id, player_code, player_game_code, move_description, move_time
    FROM moves 
    WHERE game_id = p_player_game_code;
END;
$$ LANGUAGE plpgsql;

-- Function to add a new mmove.
CREATE OR REPLACE FUNCTION add_new_move(p_game_id UUID, p_player_code CHAR, p_move_description TEXT) 
RETURNS void AS $$
DECLARE
    next_move_counter INT;
    generated_player_game_code TEXT;
BEGIN
    -- Determine the next move number for the specified game
    SELECT COALESCE(MAX(move_counter), 0) + 1 INTO next_move_counter FROM moves WHERE game_id = p_game_id;

    -- Generate the player_game_code by concatenating the game_id with the player_code
    generated_player_game_code := p_game_id::TEXT || '-' || p_player_code;

    INSERT INTO moves (game_id, player_code, player_game_code, move_description, move_counter)
    VALUES (p_game_id, p_player_code, generated_player_game_code, p_move_description, next_move_counter);
END;
$$ LANGUAGE plpgsql;

-- Function to fetch the last 3 moves for a particular game WITH TIMESTAMPS
CREATE OR REPLACE FUNCTION fetch_latest_moves_for_game(p_game_id UUID) 
RETURNS TABLE(player_game_code TEXT, move_counter INT, move_description TEXT, formatted_move_time TEXT) AS $$
BEGIN
    RETURN QUERY
    SELECT 
        m.player_game_code, 
        m.move_counter, 
        m.move_description, 
        TO_CHAR(m.move_time AT TIME ZONE 'UTC', 'HH24:MI:SS:MS DD-Mon-YYYY') AS formatted_move_time
    FROM moves m
    WHERE m.game_id = p_game_id
    ORDER BY m.move_time DESC
    LIMIT 3;
END;
$$ LANGUAGE plpgsql;


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