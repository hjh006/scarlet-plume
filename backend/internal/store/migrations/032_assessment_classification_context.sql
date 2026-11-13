-- Add optional classification context without changing adopted assessment
-- revisions or treating recorded classification as complete business evidence.
DO $$
DECLARE original record; new_id bigint; context_question jsonb;
BEGIN
 FOR original IN
  SELECT t.id,t.workspace_id,t.template_key,t.title,t.kind AS check_kind,
         r.kind AS assessment_kind,r.questions
  FROM plume.check_templates t
  JOIN plume.assessment_template_revisions r ON r.revision_id=t.id
  WHERE t.workspace_id IS NULL AND t.version=1
   AND t.template_key IN('assessment-pia','assessment-cross-border','assessment-business-change')
 LOOP
  context_question := jsonb_build_object(
   'key','classification_context',
   'title','已确认分类与业务目录上下文',
   'kind','text',
   'factKey','classification_context',
   'basis','已有分类登记与评估上下文复用（组织治理建议）',
   'basisUrl',original.questions->0->>'basisUrl',
   'applicability','本次评估关联资产已有可核验的人工分类确认记录，可按来源和修订复用；缺失或失效时可留空。',
   'evidence','同一资产的分类修订、采用标准及条款引用、业务目录与发布摘要；来源、范围和观察限制。',
   'acceptance','仅作为已确认登记上下文；不替代实际处理范围、接收角色、主体数量、法定适用条件或出境路径核验。',
   'requirementCode','G01',
   'obligation','recommended',
   'required',false
  );

  INSERT INTO plume.check_templates(workspace_id,template_key,title,kind,version)
  VALUES(original.workspace_id,original.template_key,original.title,original.check_kind,2)
  RETURNING id INTO new_id;

  INSERT INTO plume.assessment_template_revisions(revision_id,kind,questions)
  VALUES(new_id,original.assessment_kind,original.questions||jsonb_build_array(context_question));

  INSERT INTO plume.check_template_items(template_revision_id,item_key,position,title,requirement_code,applicability,evidence_requirements,acceptance_criteria)
  SELECT new_id,item_key,position,title,requirement_code,applicability,evidence_requirements,acceptance_criteria
  FROM plume.check_template_items WHERE template_revision_id=original.id;

  INSERT INTO plume.check_template_items(template_revision_id,item_key,position,title,requirement_code,applicability,evidence_requirements,acceptance_criteria)
  SELECT new_id,'classification_context',COALESCE(max(position),0)+1,
   context_question->>'title',context_question->>'requirementCode',
   context_question->>'applicability',context_question->>'evidence',context_question->>'acceptance'
  FROM plume.check_template_items WHERE template_revision_id=original.id;
 END LOOP;
END $$;
