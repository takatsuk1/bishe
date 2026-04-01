ALTER TABLE workflow_node
    ADD COLUMN pre_input TEXT NULL COMMENT '节点预处理输入模板' AFTER task_type;

UPDATE workflow_node
SET pre_input = condition_expr
WHERE pre_input IS NULL AND condition_expr IS NOT NULL;

ALTER TABLE workflow_node
    DROP COLUMN condition_expr;
