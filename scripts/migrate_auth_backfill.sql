-- Auth + User Ownership Backfill (pure multi-user, no tenant model)
-- Usage:
--   mysql -h127.0.0.1 -uroot -p<password> <database> < scripts/migrate_auth_backfill.sql

START TRANSACTION;

-- 1) Bootstrap admin account if missing.
-- Default bootstrap credentials:
--   username: admin
--   password: admin123456
-- Change password immediately after first login.
INSERT INTO users (user_id, username, display_name, password_hash, status)
SELECT 'admin', 'admin', 'Administrator', '$2a$10$FU.MVE2YJQJhkt5.sBdYKe/PxnLmc.if2gnHuxsHBw1tLXhrQEBHu', 1
WHERE NOT EXISTS (
  SELECT 1 FROM users WHERE user_id = 'admin' OR username = 'admin'
);

-- 2) Normalize legacy workflow ownership.
UPDATE user_workflow
SET user_id = 'admin'
WHERE TRIM(IFNULL(user_id, '')) = '' OR user_id = 'default';

-- 3) Normalize legacy tool ownership.
-- Keep built-ins as system; move only empty/default ownership to admin.
UPDATE user_tool
SET user_id = 'admin'
WHERE (TRIM(IFNULL(user_id, '')) = '' OR user_id = 'default')
  AND user_id <> 'system';

-- 4) Normalize legacy agent ownership.
-- Keep built-ins as system; move only empty/default ownership to admin.
UPDATE user_agent
SET user_id = 'admin'
WHERE (TRIM(IFNULL(user_id, '')) = '' OR user_id = 'default')
  AND user_id <> 'system';

-- 5) Revoke all refresh tokens for migrated identities (defensive cleanup).
UPDATE user_refresh_token
SET revoked_at = NOW()
WHERE user_id = 'default' AND revoked_at IS NULL;

COMMIT;
