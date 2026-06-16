-- +goose Up

-- Ensure api_keys has user_id column (in case migration 004 partially failed)
ALTER TABLE api_keys ADD COLUMN IF NOT EXISTS user_id BIGINT REFERENCES users(id) ON DELETE CASCADE;

-- Fill any NULL user_id rows and then enforce NOT NULL
-- +goose StatementBegin
DO $$
DECLARE v_uid BIGINT;
BEGIN
  SELECT id INTO v_uid FROM users ORDER BY id ASC LIMIT 1;
  IF v_uid IS NOT NULL THEN
    UPDATE api_keys SET user_id = v_uid WHERE user_id IS NULL;
  ELSE
    DELETE FROM api_keys WHERE user_id IS NULL;
  END IF;
END $$;
-- +goose StatementEnd

ALTER TABLE api_keys ALTER COLUMN user_id SET NOT NULL;

-- Drop whatever primary key exists and recreate as composite (user_id, key_name)
-- +goose StatementBegin
DO $$
BEGIN
  -- Drop existing PK (might be single-column key_name or already composite)
  IF EXISTS (
    SELECT 1 FROM pg_constraint
    WHERE conrelid = 'api_keys'::regclass AND contype = 'p'
  ) THEN
    EXECUTE (
      SELECT 'ALTER TABLE api_keys DROP CONSTRAINT ' || conname
      FROM pg_constraint
      WHERE conrelid = 'api_keys'::regclass AND contype = 'p'
    );
  END IF;

  -- Add composite PK
  ALTER TABLE api_keys ADD PRIMARY KEY (user_id, key_name);
END $$;
-- +goose StatementEnd

-- +goose Down
SELECT 1;
