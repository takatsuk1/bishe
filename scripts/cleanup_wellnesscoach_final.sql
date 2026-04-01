-- 清理 wellnesscoach agent 的所有相关数据

-- 1. 获取 wellnesscoach 工作流的 IDs
-- 删除相关的工作流节点
DELETE FROM workflow_node 
WHERE workflow_id IN (
    SELECT workflow_id FROM user_workflow 
    WHERE workflow_id IN ('wellnesscoach-default', 'wellnesscoach')
    OR name LIKE '%wellness%'
);

-- 2. 删除工作流边
DELETE FROM workflow_edge 
WHERE workflow_id IN (
    SELECT workflow_id FROM user_workflow 
    WHERE workflow_id IN ('wellnesscoach-default', 'wellnesscoach')
    OR name LIKE '%wellness%'
);

-- 3. 删除工作流本身
DELETE FROM user_workflow 
WHERE workflow_id IN ('wellnesscoach-default', 'wellnesscoach')
OR name LIKE '%wellness%';

-- 完成
