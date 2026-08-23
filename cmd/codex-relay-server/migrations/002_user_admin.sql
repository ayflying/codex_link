ALTER TABLE users ADD COLUMN is_admin BOOLEAN NOT NULL DEFAULT FALSE AFTER username_key;

UPDATE users
SET is_admin = TRUE
WHERE id = (
  SELECT id
  FROM (
    SELECT id
    FROM users
    ORDER BY created_at ASC, id ASC
    LIMIT 1
  ) AS oldest_user
);
