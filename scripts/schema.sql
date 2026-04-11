-- 用户自定义工作流表
CREATE TABLE IF NOT EXISTS `user_workflow` (
    `id` BIGINT UNSIGNED NOT NULL AUTO_INCREMENT COMMENT '主键ID',
    `workflow_id` VARCHAR(64) NOT NULL COMMENT '工作流唯一标识',
    `user_id` VARCHAR(128) NOT NULL COMMENT '用户ID',
    `name` VARCHAR(255) NOT NULL COMMENT '工作流名称',
    `description` TEXT COMMENT '工作流描述',
    `start_node_id` VARCHAR(64) NOT NULL COMMENT '起始节点ID',
    `status` TINYINT NOT NULL DEFAULT 1 COMMENT '状态: 1-启用, 0-禁用',
    `created_at` DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
    `updated_at` DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
    PRIMARY KEY (`id`),
    UNIQUE KEY `uk_workflow_id` (`workflow_id`),
    KEY `idx_user_id` (`user_id`),
    KEY `idx_created_at` (`created_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='用户自定义工作流表';

-- 工作流节点表
CREATE TABLE IF NOT EXISTS `workflow_node` (
    `id` BIGINT UNSIGNED NOT NULL AUTO_INCREMENT COMMENT '主键ID',
    `workflow_id` VARCHAR(64) NOT NULL COMMENT '工作流ID',
    `node_id` VARCHAR(64) NOT NULL COMMENT '节点唯一标识',
    `node_type` VARCHAR(32) NOT NULL COMMENT '节点类型: start, end, pre_input, condition, loop, chat_model, tool',
    `agent_id` VARCHAR(64) DEFAULT NULL COMMENT '所属Agent ID',
    `task_type` VARCHAR(64) DEFAULT NULL COMMENT '任务类型',
    `pre_input` TEXT DEFAULT NULL COMMENT '节点预处理输入模板',
    `loop_config` JSON DEFAULT NULL COMMENT '循环配置(loop类型节点)',
    `metadata` JSON DEFAULT NULL COMMENT '节点元数据(UI位置、标签等)',
    `created_at` DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
    `updated_at` DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
    PRIMARY KEY (`id`),
    UNIQUE KEY `uk_workflow_node` (`workflow_id`, `node_id`),
    KEY `idx_workflow_id` (`workflow_id`),
    KEY `idx_node_type` (`node_type`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='工作流节点表';

-- 工作流边表
CREATE TABLE IF NOT EXISTS `workflow_edge` (
    `id` BIGINT UNSIGNED NOT NULL AUTO_INCREMENT COMMENT '主键ID',
    `workflow_id` VARCHAR(64) NOT NULL COMMENT '工作流ID',
    `from_node_id` VARCHAR(64) NOT NULL COMMENT '源节点ID',
    `to_node_id` VARCHAR(64) NOT NULL COMMENT '目标节点ID',
    `label` VARCHAR(64) DEFAULT NULL COMMENT '边标签(true/false等)',
    `mapping` JSON DEFAULT NULL COMMENT '数据映射配置',
    `sort_order` INT NOT NULL DEFAULT 0 COMMENT '排序顺序',
    `created_at` DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
    `updated_at` DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
    PRIMARY KEY (`id`),
    KEY `idx_workflow_id` (`workflow_id`),
    KEY `idx_from_node` (`workflow_id`, `from_node_id`),
    KEY `idx_to_node` (`workflow_id`, `to_node_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='工作流边表';

-- 用户自定义工具表
CREATE TABLE IF NOT EXISTS `user_tool` (
    `id` BIGINT UNSIGNED NOT NULL AUTO_INCREMENT COMMENT '主键ID',
    `tool_id` VARCHAR(64) NOT NULL COMMENT '工具唯一标识',
    `user_id` VARCHAR(128) NOT NULL COMMENT '用户ID',
    `name` VARCHAR(128) NOT NULL COMMENT '工具名称',
    `description` TEXT COMMENT '工具描述',
    `tool_type` VARCHAR(32) NOT NULL COMMENT '工具类型: http, mcp, function',
    `config` JSON NOT NULL COMMENT '工具配置(HTTP/MCP配置)',
    `parameters` JSON DEFAULT NULL COMMENT '参数定义',
    `output_parameters` JSON DEFAULT NULL COMMENT '输出参数定义',
    `status` TINYINT NOT NULL DEFAULT 1 COMMENT '状态: 1-启用, 0-禁用',
    `created_at` DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
    `updated_at` DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
    PRIMARY KEY (`id`),
    UNIQUE KEY `uk_tool_id` (`tool_id`),
    KEY `idx_user_id` (`user_id`),
    KEY `idx_tool_type` (`tool_type`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='用户自定义工具表';

-- 用户自定义Agent表
CREATE TABLE IF NOT EXISTS `user_agent` (
    `id` BIGINT UNSIGNED NOT NULL AUTO_INCREMENT COMMENT '主键ID',
    `agent_id` VARCHAR(64) NOT NULL COMMENT 'Agent唯一标识',
    `user_id` VARCHAR(128) NOT NULL COMMENT '用户ID',
    `name` VARCHAR(128) NOT NULL COMMENT 'Agent名称',
    `description` TEXT COMMENT 'Agent描述',
    `workflow_id` VARCHAR(64) NOT NULL COMMENT '关联的工作流ID',
    `status` VARCHAR(32) NOT NULL DEFAULT 'draft' COMMENT '状态: draft-草稿, testing-测试中, published-已发布, stopped-已停止',
    `port` INT DEFAULT NULL COMMENT '分配的端口',
    `process_pid` INT DEFAULT NULL COMMENT '进程PID',
    `code_path` VARCHAR(512) DEFAULT NULL COMMENT '生成的代码路径',
    `published_at` DATETIME DEFAULT NULL COMMENT '发布时间',
    `created_at` DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
    `updated_at` DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
    PRIMARY KEY (`id`),
    UNIQUE KEY `uk_agent_id` (`agent_id`),
    KEY `idx_user_id` (`user_id`),
    KEY `idx_status` (`status`),
    KEY `idx_workflow_id` (`workflow_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='用户自定义Agent表';

-- 用户账号表
CREATE TABLE IF NOT EXISTS `users` (
    `id` BIGINT UNSIGNED NOT NULL AUTO_INCREMENT COMMENT '主键ID',
    `user_id` VARCHAR(64) NOT NULL COMMENT '用户唯一标识',
    `username` VARCHAR(128) NOT NULL COMMENT '登录用户名',
    `display_name` VARCHAR(128) DEFAULT NULL COMMENT '展示名称',
    `password_hash` VARCHAR(255) NOT NULL COMMENT '密码哈希',
    `role_code` VARCHAR(64) NOT NULL DEFAULT 'user' COMMENT '角色编码(单角色模型)',
    `status` TINYINT NOT NULL DEFAULT 1 COMMENT '状态: 1-启用, 0-禁用',
    `created_at` DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
    `updated_at` DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
    PRIMARY KEY (`id`),
    UNIQUE KEY `uk_user_id` (`user_id`),
    UNIQUE KEY `uk_username` (`username`),
    KEY `idx_role_code` (`role_code`),
    KEY `idx_status` (`status`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='用户账号表';

-- 角色表
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

-- 用户刷新令牌表
CREATE TABLE IF NOT EXISTS `user_refresh_token` (
    `id` BIGINT UNSIGNED NOT NULL AUTO_INCREMENT COMMENT '主键ID',
    `token_id` VARCHAR(64) NOT NULL COMMENT '令牌ID',
    `user_id` VARCHAR(64) NOT NULL COMMENT '用户ID',
    `token_hash` VARCHAR(128) NOT NULL COMMENT '刷新令牌哈希',
    `expires_at` DATETIME NOT NULL COMMENT '过期时间',
    `revoked_at` DATETIME DEFAULT NULL COMMENT '撤销时间',
    `created_at` DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
    PRIMARY KEY (`id`),
    UNIQUE KEY `uk_token_id` (`token_id`),
    UNIQUE KEY `uk_token_hash` (`token_hash`),
    KEY `idx_user_id` (`user_id`),
    KEY `idx_expires_at` (`expires_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='用户刷新令牌表';
