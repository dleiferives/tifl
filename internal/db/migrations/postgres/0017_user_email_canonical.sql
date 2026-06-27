ALTER TABLE users ADD COLUMN email_canonical TEXT;

UPDATE users
SET email_canonical = lower(email)
WHERE email_canonical IS NULL;

ALTER TABLE users ALTER COLUMN email_canonical SET NOT NULL;

CREATE UNIQUE INDEX idx_users_email_canonical
    ON users(email_canonical);
