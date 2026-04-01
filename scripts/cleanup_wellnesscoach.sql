-- 清理 wellnesscoach agent 的所有相关数据

-- 1. 删除 wellnesscoach 相关的工作流节点
DELETE FROM workflow_node 
WHERE workflow_id IN (
    SELECT workflow_id FROM user_workflow 
    WHERE workflow_id = 'wellnesscoach-default' 
    OR name LIKE '%wellness%'
);

-- 2. 删除 wellnesscoach 相关的工作流边
DELETE FROM workflow_edge 
WHERE workflow_id IN (
    SELECT workflow_id FROM user_workflow 
    WHERE workflow_id = 'wellnesscoach-default' 
    OR name LIKE '%wellness%'
);

-- 3. 删除 wellnesscoach 工作流
DELETE FROM user_workflow 
WHERE workflow_id = 'wellnesscoach-default' 
OR name LIKE '%wellness%';

-- 4. 删除 wellnesscoach 用户自定义 agents（如果有）
DELETE FROM user_agent 
WHERE workflow_id LIKE '%wellness%' 
OR agent_id LIKE '%wellness%';

COMMIT;
