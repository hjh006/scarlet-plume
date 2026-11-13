-- Publish an additive revision; adopted assessments retain their original revision.
DO $$
DECLARE old_id bigint; new_id bigint;
BEGIN
 SELECT id INTO old_id FROM plume.check_templates
 WHERE workspace_id IS NULL AND template_key='assessment-ai' AND version=1;
 INSERT INTO plume.check_templates(workspace_id,template_key,title,kind,version)
 SELECT workspace_id,template_key,title,kind,2 FROM plume.check_templates WHERE id=old_id
 RETURNING id INTO new_id;
 INSERT INTO plume.assessment_template_revisions(revision_id,kind,questions)
 SELECT new_id,kind,questions || '[{"key":"application_configuration","title":"应用/API采集配置","kind":"text","factKey":"application_configuration","basis":"企业AI应用配置核对","basisUrl":"https://www.cac.gov.cn/2023-07/13/c_1690898327029107.htm","applicability":"已接入配置API且明确绑定该活动资产的AI应用。","evidence":"来源配置、采集时间、字段完整值与缺失项。","acceptance":"仅记录API声明的配置；不替代供应商条款、实际处理地域、合法性与适用义务核验。未接入时可留空。","requirementCode":"X01","obligation":"recommended","required":false}]'::jsonb
 FROM plume.assessment_template_revisions WHERE revision_id=old_id;
 INSERT INTO plume.check_template_items(template_revision_id,item_key,position,title,requirement_code,applicability,evidence_requirements,acceptance_criteria)
 SELECT new_id,item_key,position,title,requirement_code,applicability,evidence_requirements,acceptance_criteria
 FROM plume.check_template_items WHERE template_revision_id=old_id;
 INSERT INTO plume.check_template_items(template_revision_id,item_key,position,title,requirement_code,applicability,evidence_requirements,acceptance_criteria)
 VALUES(new_id,'application_configuration',8,'应用/API采集配置','X01','已接入配置API且明确绑定该活动资产的AI应用。','来源配置、采集时间、字段完整值与缺失项。','仅记录API声明的配置，不替代条款、实际处理与适用义务核验。');
END $$;
