-- Enforce single-role model: users.role_code is the source of truth.

-- 1) Ensure users.role_code exists.
ALTER TABLE `users`
    ADD COLUMN IF NOT EXISTS `role_code` VARCHAR(64) NOT NULL DEFAULT 'user' COMMENT '角色编码(单角色模型)' AFTER `password_hash`;

-- 2) Backfill users.role_code from user_role (if table/data exists).
-- Priority: admin(1) > operator(2) > user(3) > viewer(4) > others(100)
UPDATE `users` u
LEFT JOIN (
    SELECT t.user_id,
           SUBSTRING_INDEX(
               GROUP_CONCAT(
                   t.role_code
                   ORDER BY CASE t.role_code
                       WHEN 'admin' THEN 1
                       WHEN 'operator' THEN 2
                       WHEN 'user' THEN 3
                       WHEN 'viewer' THEN 4
                       ELSE 100
                   END,
                   t.id ASC
               ),
               ',',
               1
           ) AS effective_role
    FROM user_role t
    GROUP BY t.user_id
) ur ON ur.user_id = u.user_id
SET u.role_code = IFNULL(NULLIF(TRIM(ur.effective_role), ''), 'user');

-- 3) Add index for role filtering/querying.
CREATE INDEX `idx_role_code` ON `users` (`role_code`);

-- 4) Optional cleanup after code cutover.
-- DROP TABLE IF EXISTS `user_role`;
