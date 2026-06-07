-- +goose Up

-- Add user_id to api_keys and assets
ALTER TABLE api_keys ADD COLUMN user_id BIGINT REFERENCES users(id) ON DELETE CASCADE;
ALTER TABLE assets ADD COLUMN user_id BIGINT REFERENCES users(id) ON DELETE CASCADE;

-- Assign existing rows to the first user, or delete orphaned rows if no users exist
-- +goose StatementBegin
DO $$
DECLARE v_uid BIGINT;
BEGIN
  SELECT id INTO v_uid FROM users ORDER BY id ASC LIMIT 1;
  IF v_uid IS NOT NULL THEN
    UPDATE api_keys SET user_id = v_uid WHERE user_id IS NULL;
    UPDATE assets SET user_id = v_uid WHERE user_id IS NULL;
  ELSE
    DELETE FROM api_keys;
    DELETE FROM assets;
  END IF;
END $$;
-- +goose StatementEnd

-- Make NOT NULL and update api_keys primary key to be per-user
ALTER TABLE api_keys ALTER COLUMN user_id SET NOT NULL;
ALTER TABLE api_keys DROP CONSTRAINT api_keys_pkey;
ALTER TABLE api_keys ADD PRIMARY KEY (user_id, key_name);

ALTER TABLE assets ALTER COLUMN user_id SET NOT NULL;
CREATE INDEX assets_user_id_idx ON assets(user_id);

-- +goose Down
DROP INDEX IF EXISTS assets_user_id_idx;
ALTER TABLE assets DROP COLUMN IF EXISTS user_id;
ALTER TABLE api_keys DROP CONSTRAINT IF EXISTS api_keys_pkey;
ALTER TABLE api_keys ADD PRIMARY KEY (key_name);
ALTER TABLE api_keys DROP COLUMN IF EXISTS user_id;
