-- Add RBAC tables and seed default roles
CREATE TABLE IF NOT EXISTS `role` (
    `id` BIGINT UNSIGNED NOT NULL AUTO_INCREMENT COMMENT '主键ID',
    `role_code` VARCHAR(64) NOT NULL COMMENT '角色编码',
    `role_name` VARCHAR(128) NOT NULL COMMENT '角色名称',
    `description` VARCHAR(255) DEFAULT NULL COMMENT '角色描述',
    `status` TINYINT NOT NULL DEFAULT 1 COMMENT '状态: 1-启用, 0-禁用',
    `created_at` DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
    `updated_at` DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
    PRIMARY KEY (`id`),
    UNIQUE KEY `uk_role_code` (`role_code`),
    KEY `idx_role_status` (`status`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='角色表';

ALTER TABLE `users`
    ADD COLUMN IF NOT EXISTS `role_code` VARCHAR(64) NOT NULL DEFAULT 'user' COMMENT '角色编码(单角色模型)' AFTER `password_hash`;

INSERT INTO `role` (`role_code`, `role_name`, `description`, `status`) VALUES
    ('viewer', 'Viewer', 'Read-only role', 1),
    ('user', 'User', 'Default registered user', 1),
    ('operator', 'Operator', 'Can operate system resources', 1),
    ('admin', 'Administrator', 'Full access', 1)
ON DUPLICATE KEY UPDATE
    `role_name` = VALUES(`role_name`),
    `description` = VALUES(`description`),
    `status` = VALUES(`status`),
    `updated_at` = CURRENT_TIMESTAMP;

-- Backfill existing users from user_role if present, otherwise keep default `user`
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
