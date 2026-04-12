CREATE TABLE IF NOT EXISTS `bazi_consultation_record` (
    `id` BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    `user_id` VARCHAR(128) NOT NULL DEFAULT '',
    `task_id` VARCHAR(128) NOT NULL DEFAULT '',
    `query_text` TEXT NULL,
    `request_json` LONGTEXT NULL,
    `tool_plan_json` LONGTEXT NULL,
    `tool_result_json` LONGTEXT NULL,
    `final_response` LONGTEXT NULL,
    `created_at` DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    `updated_at` DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    PRIMARY KEY (`id`),
    KEY `idx_bazi_user_id` (`user_id`),
    KEY `idx_bazi_task_id` (`task_id`),
    KEY `idx_bazi_created_at` (`created_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
