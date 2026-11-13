ALTER TABLE plume.classification_deliveries ADD COLUMN target_name text NOT NULL DEFAULT '';
ALTER TABLE plume.classification_deliveries ADD COLUMN target_endpoint text NOT NULL DEFAULT '';
UPDATE plume.classification_deliveries d SET target_name=t.name,target_endpoint=t.endpoint
 FROM plume.classification_delivery_targets t WHERE t.id=d.target_id AND t.workspace_id=d.workspace_id;
