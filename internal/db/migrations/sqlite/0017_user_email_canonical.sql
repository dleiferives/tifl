ALTER TABLE users ADD COLUMN email_canonical TEXT;

UPDATE users
SET email_canonical = lower(email)
WHERE email_canonical IS NULL;

CREATE UNIQUE INDEX idx_users_email_canonical
    ON users(email_canonical);
